// Package egress decides which destinations an agent may reach, and records
// who reached them.
//
// The platform's containment story was asymmetric. A great deal of design
// went into what an agent can bring BACK — output leaves only as a branch,
// through the broker, create-only, and a human must have looked at it
// (ADR-0005, ADR-0006, ADR-0011). What an agent can SEND OUT was
// unrestricted: every pod in a cell namespace could open a connection to any
// host on 443, which is what model APIs, forges and package mirrors need.
//
// For an ordinary SaaS that asymmetry is tolerable, because the way data
// leaves is a person deciding to leak it. An agent platform has a path no
// SaaS has:
//
//	a poisoned README, issue or web page
//	  -> instructions the agent treats as instructions
//	  -> the agent reads the workspace
//	  -> one HTTPS request to anywhere
//
// The branch is the SANCTIONED exit. It was never the only one.
//
// The answer is not to close 443 — model APIs, git and package managers all
// have to go out. It is to make going out pass through something that can
// name what it allows and record what it did.
package egress

import (
	"net"
	"strings"
)

// Rule is one allowed destination.
//
// Host is either an exact name (`api.anthropic.com`) or a wildcard
// (`*.anthropic.com`), which matches any subdomain but NOT the apex — an
// allowlist that silently included the parent domain would be a different,
// larger grant than the one somebody wrote down.
type Rule struct {
	Host string
	// Port, 0 meaning 443 only. Named explicitly because "we allowed
	// github.com" and "we allowed anything github.com happens to listen on"
	// are different statements.
	Port int
	// Why this is here, carried into the audit line so a reader of the log
	// does not have to go and find the config to understand a decision.
	Note string
}

// Policy is the whole allowlist plus how strictly to apply it.
type Policy struct {
	Rules []Rule
	// Observe reports rather than refuses.
	//
	// Turning this off is the moment every agent's outbound call starts
	// depending on the list being complete, so the list is DISCOVERED in
	// observe mode first — from the audit log of a real working week —
	// rather than guessed. A wrong list in enforce mode does not degrade
	// gracefully: model calls fail, and the platform stops working.
	Observe bool
}

// Verdict is what the proxy decided and why.
type Verdict struct {
	Allow bool
	// Rule that matched, empty when nothing did.
	Rule string
	// Reason, always set — the audit line's explanation.
	Reason string
	// Observed is true when this was refused by policy but let through
	// because the policy is still in observe mode. The audit line has to
	// say so, or a log full of "allowed" reads as a list that is complete
	// when it is nothing of the kind.
	Observed bool
}

// Check decides whether a CONNECT to host:port is permitted.
//
// host is taken from the CONNECT request line, which is what the client
// asked for by name. The proxy then resolves and dials that same name, so
// the destination is whatever the name points at — a client cannot ask for
// one host and be connected to another.
func (p Policy) Check(host string, port int) Verdict {
	h := normalizeHost(host)
	if h == "" {
		return Verdict{Reason: "空的目标主机"}
	}
	// A literal IP defeats the entire point of a name-based allowlist:
	// `CONNECT 203.0.113.7:443` reaches anywhere without ever naming it.
	// Refused even in observe mode, because there is nothing to learn from
	// it — an address is not a destination anybody can review later.
	if net.ParseIP(h) != nil {
		return Verdict{Reason: "不允许直接连 IP:域名白名单对地址无效,请用主机名"}
	}
	for _, r := range p.Rules {
		if !hostMatches(r.Host, h) {
			continue
		}
		want := r.Port
		if want == 0 {
			want = 443
		}
		if port != want {
			continue
		}
		reason := "允许:" + r.Host
		if r.Note != "" {
			reason += "(" + r.Note + ")"
		}
		return Verdict{Allow: true, Rule: r.Host, Reason: reason}
	}
	if p.Observe {
		return Verdict{
			Allow:    true,
			Observed: true,
			Reason:   "不在白名单里,但出口策略仍处于观察模式,已放行并记录",
		}
	}
	return Verdict{Reason: "不在出口白名单里"}
}

// normalizeHost lowercases, strips a trailing dot and drops any zone or
// brackets around a literal address.
func normalizeHost(h string) string {
	h = strings.TrimSpace(strings.ToLower(h))
	h = strings.TrimSuffix(h, ".")
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	return h
}

// hostMatches applies one rule to one already-normalized host.
func hostMatches(rule, host string) bool {
	rule = normalizeHost(rule)
	if rule == "" {
		return false
	}
	if suffix, ok := strings.CutPrefix(rule, "*."); ok {
		// Any subdomain, at any depth, but never the apex itself.
		return strings.HasSuffix(host, "."+suffix) && host != suffix
	}
	return host == rule
}

// Attribution is who made the request.
//
// This is the half that makes an egress log worth keeping. "Something in the
// cluster called an API" is a firewall line. "This person's session, in this
// project, called this API" is an audit trail — and it is the same identity
// the rest of the platform authorizes against, so the two can be read
// together.
type Attribution struct {
	// PrincipalID is the allocated principal id (ADR-0016), not a login.
	PrincipalID string
	Cell        string
	Session     string
	// Pod and IP are kept for the case where nothing else resolved: an
	// unattributed line is still evidence, and pretending it belongs to
	// nobody would be worse than saying which pod it came from.
	Pod string
	IP  string
}

// Known reports whether this request could be traced to a person.
func (a Attribution) Known() bool { return a.PrincipalID != "" }
