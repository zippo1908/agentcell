package main

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// ADR-0005 v2: action-level boundary. The broker inspects a git-receive-pack
// (push) request and permits only what a session is allowed to do — push its
// own session/* branch — rejecting anything else (the base branch, arbitrary
// refs, ref deletions). This is enforced at the broker, so a workload cannot
// bypass it even with a valid credential.

const maxPushInspectBytes = 100 << 20 // 100 MiB cap for policy inspection

type refUpdate struct {
	Old, New, Ref string
}

// zeroOID is the all-zero object id git uses for create (old) and delete (new).
func isZeroOID(s string) bool {
	for _, c := range s {
		if c != '0' {
			return false
		}
	}
	return len(s) > 0
}

// parseReceivePackCommands extracts the ref-update commands from the pkt-line
// prefix of a git-receive-pack body (everything before the flush-pkt and the
// PACK data). It is a pure function so the policy is unit-testable.
func parseReceivePackCommands(body []byte) ([]refUpdate, error) {
	var cmds []refUpdate
	pos := 0
	for pos+4 <= len(body) {
		n, err := hexInt(body[pos : pos+4])
		if err != nil {
			return nil, fmt.Errorf("bad pkt-line length: %w", err)
		}
		if n == 0 { // flush-pkt: end of commands, PACK follows
			return cmds, nil
		}
		if n < 4 || pos+n > len(body) {
			return nil, fmt.Errorf("pkt-line length %d out of range", n)
		}
		payload := body[pos+4 : pos+n]
		pos += n

		// First command carries "\x00<capabilities>"; drop it. Then trim \n.
		if i := bytes.IndexByte(payload, 0); i >= 0 {
			payload = payload[:i]
		}
		line := strings.TrimRight(string(payload), "\n")
		fields := strings.SplitN(line, " ", 3)
		if len(fields) != 3 {
			// Non-command pkt-line (e.g. "shallow ..."); ignore.
			continue
		}
		cmds = append(cmds, refUpdate{Old: fields[0], New: fields[1], Ref: fields[2]})
	}
	return cmds, nil
}

func hexInt(b []byte) (int, error) {
	dst := make([]byte, 2)
	if _, err := hex.Decode(dst, b); err != nil {
		return 0, err
	}
	return int(dst[0])<<8 | int(dst[1]), nil
}

// refPolicyError describes why a push was rejected.
type refPolicyError struct{ reason string }

func (e *refPolicyError) Error() string { return e.reason }

// checkRefPolicy binds a push to exactly one branch: the caller's own
// session. Every command must target refs/heads/session/<sessionID> (no
// other ref, no deletions). sessionID is derived from the unforgeable pod
// identity, so a settle pod cannot push to another session's branch.
func checkRefPolicy(cmds []refUpdate, sessionID string) error {
	if len(cmds) == 0 {
		return &refPolicyError{"push contained no ref updates"}
	}
	want := "refs/heads/session/" + sessionID
	for _, c := range cmds {
		if c.Ref != want {
			return &refPolicyError{fmt.Sprintf(
				"ref %q is not permitted: this session may push only %q", c.Ref, want)}
		}
		if isZeroOID(c.New) {
			return &refPolicyError{fmt.Sprintf("deleting %q is not permitted", c.Ref)}
		}
	}
	return nil
}

// enforcePushPolicy reads a receive-pack body (handling gzip), validates its
// commands against the caller's session id, and returns a replacement body
// that streams the ORIGINAL bytes unchanged so the proxy forwards a
// byte-identical request.
func enforcePushPolicy(body io.Reader, gzipped bool, sessionID string) (io.Reader, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxPushInspectBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxPushInspectBytes {
		return nil, &refPolicyError{"push too large for policy inspection"}
	}
	inspect := raw
	if gzipped {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("gunzip push body: %w", err)
		}
		if inspect, err = io.ReadAll(io.LimitReader(zr, maxPushInspectBytes)); err != nil {
			return nil, fmt.Errorf("read gunzipped push body: %w", err)
		}
	}
	cmds, err := parseReceivePackCommands(inspect)
	if err != nil {
		return nil, err
	}
	if err := checkRefPolicy(cmds, sessionID); err != nil {
		return nil, err
	}
	return bytes.NewReader(raw), nil
}
