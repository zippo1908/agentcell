package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// runSession is PID 1 of a session pod: carve a worktree out of the shared
// object store, then hand the task to the agent CLI. The pod's exit —
// clean, crashed or killed — always leads to the settle job; this applet
// never cleans up after itself by design.
func runSession() error {
	id := os.Getenv(runtimeapi.EnvSessionID)
	base := os.Getenv(runtimeapi.EnvBaseBranch)
	if id == "" {
		return fmt.Errorf("%s not set", runtimeapi.EnvSessionID)
	}
	var argv []string
	if err := json.Unmarshal([]byte(os.Getenv("AGENTCELL_AGENT_ARGV")), &argv); err != nil || len(argv) == 0 {
		return fmt.Errorf("AGENTCELL_AGENT_ARGV invalid: %v", err)
	}
	if err := ensureAskpass(); err != nil {
		return err
	}
	if err := waitForRepo(5 * time.Minute); err != nil {
		return err
	}

	// The pod runs as its owner (ADR-0009), so our own uid is the identity
	// everything private belongs to — no need to be told it.
	uid := int64(os.Getuid())
	if err := ensurePrivateHome(uid); err != nil {
		return err
	}
	// After HOME points at the private tree, so the trust lands in this
	// user's own git config rather than a shared one.
	trustAllRepos()
	wt := ids.WorktreePath(uid, id)
	// One worktree per repository. For a single-repo project this is exactly
	// the old single call: the worktree IS the session directory.
	for _, rp := range reposFromEnv() {
		b := rp.Branch
		if b == "" {
			b = base
		}
		if err := prepareWorktreeFor(ids.WorktreeDirFor(uid, id, rp.Path), id, b, rp.Path); err != nil {
			return fmt.Errorf("repo %q: %w", rp.Name, err)
		}
	}

	// When the user asked to watch this session live, serve the preview from
	// this pod. The worktree is private to its owner, so the anchor cannot
	// serve it — the resident preview for a followed session belongs to the
	// user's own runtime (ADR-0009). The Cell's preview Service selects this
	// pod for as long as it is the followed one.
	var previewArgv []string
	if raw := os.Getenv(runtimeapi.EnvPreviewCmd); raw != "" {
		if err := json.Unmarshal([]byte(raw), &previewArgv); err == nil && len(previewArgv) > 0 {
			stop := make(chan os.Signal, 1)
			signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
			go supervisePreview(previewArgv, wt, stop)
		}
	}

	if err := writeAgentConfig(); err != nil {
		return err
	}
	if err := writeAccountCredential(); err != nil {
		return err
	}
	if err := writeLibrary(wt); err != nil {
		return err
	}

	if os.Getenv(runtimeapi.EnvResident) == "1" {
		return runResident(uid, id, wt, argv)
	}

	fmt.Printf("session %s: running %v in %s\n", id, argv, wt)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = wt
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = nil
	if err := cmd.Run(); err != nil {
		// Non-zero agent exit still settles; surface the cause in the log.
		return fmt.Errorf("agent exited: %w", err)
	}
	return nil
}

// writeAgentConfig lays down the config file a CLI needs before it will use
// the endpoint this session was dispatched at.
//
// It is written rather than passed as flags because that is what the CLI
// reads; and it is written per session, into the session's own state
// directory, so one of a user's sessions cannot repoint another's. Failing
// here is fatal on purpose: the alternative is a run that looks healthy
// while talking to the wrong vendor's endpoint with the wrong model.
func writeAgentConfig() error {
	raw := os.Getenv(runtimeapi.EnvAgentConfig)
	if raw == "" {
		return nil
	}
	var cfg runtimeapi.AgentConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil || cfg.Path == "" {
		return fmt.Errorf("%s invalid: %v", runtimeapi.EnvAgentConfig, err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o700); err != nil {
		return fmt.Errorf("agent config dir: %w", err)
	}
	// Expand the key HERE, not in the control plane: the rendered content
	// travels in the pod spec, and a literal key there is readable by anyone
	// who can read pods. Only this variable is expanded — a config template
	// is not a shell, and letting it reach for arbitrary environment would
	// turn an operator's config file into an exfiltration primitive.
	content := os.Expand(cfg.Content, func(k string) string {
		if k == "AGENTCELL_API_KEY" {
			return os.Getenv(k)
		}
		return "${" + k + "}"
	})
	// 0600: it now carries the credential itself.
	if err := os.WriteFile(cfg.Path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("agent config: %w", err)
	}
	fmt.Printf("session: wrote agent config %s\n", cfg.Path)
	return nil
}

// writeAccountCredential unpacks a connected account login into the CLI's
// state directory.
//
// This is what makes "connect your Kimi account once" mean every session,
// rather than every session asking for a key. The tar arrives by Secret
// reference, so it was never in the pod spec; it lands under the session's
// own state directory, 0700, which is where the CLI looks and where nothing
// belonging to another user can reach.
func writeAccountCredential() error {
	blob := os.Getenv(runtimeapi.EnvAccount)
	if blob == "" {
		return nil
	}
	home := os.Getenv("KIMI_CODE_HOME")
	if home == "" {
		// Nowhere to put it is not an error worth failing a session over:
		// the runner may simply not be the one this credential is for.
		return nil
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	cmd := exec.Command("sh", "-c", "base64 -d | tar xzf - -C "+shellQuote(home))
	cmd.Stdin = strings.NewReader(blob)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("account credential: %v: %s", err, out)
	}
	// Set the modes here rather than trusting the ones the tar carries. A tar
	// preserves whatever it captured, so the permissions of a live login
	// would be decided by the modes that happened to exist in a helper pod
	// three steps away — a place nobody thinks about when reasoning about
	// who can read a credential. Deciding them at the point of use makes the
	// answer readable in one file.
	if err := tighten(filepath.Join(home, "credentials")); err != nil {
		return err
	}
	// The device identity is part of the credential; it is read back on
	// every request, so it has to be as private as the token it
	// accompanies.
	if err := os.Chmod(filepath.Join(home, "device_id"), 0o600); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Println("session: connected account credential installed")
	return nil
}

// writeLibrary unpacks the project's readable files into the worktree.
//
// It lands at .agentcell/library/ INSIDE the worktree, so the agent finds
// it with the same Read and Grep it uses for code — no new tool to learn,
// no lookup API to remember, and `grep -r` over the specification works
// the way anybody would expect.
//
// Written fresh on every window open rather than synced: the library is
// small, the console is the source of truth, and a stale copy of a
// specification is worse than no copy — an agent quoting last week's
// requirement is confidently wrong, which is the failure mode with the
// highest cost here.
func writeLibrary(dir string) error {
	blob := os.Getenv(runtimeapi.EnvLibrary)
	if blob == "" {
		return nil
	}
	dest := filepath.Join(dir, ".agentcell", "library")
	if err := os.RemoveAll(dest); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	cmd := exec.Command("sh", "-c", "base64 -d | tar xzf - -C "+shellQuote(dest))
	cmd.Stdin = strings.NewReader(blob)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Not fatal. A session that cannot start because a document failed
		// to unpack is a worse outcome than a session without the
		// documents, and the agent can be told.
		fmt.Printf("session: 项目文件没能展开(%v: %s)\n", err, out)
		return nil
	}
	fmt.Println("session: 项目文件在 .agentcell/library/")
	return nil
}

// tighten makes a credential directory readable only by its owner.
func tighten(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// The CLI may name its directory something else; nothing to
			// tighten is not a failure.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		mode := fs.FileMode(0o600)
		if d.IsDir() {
			mode = 0o700
		}
		return os.Chmod(p, mode)
	})
}

// runResident hosts the agent in a tmux server the owner can attach to, and
// keeps the slot alive after it finishes.
//
// The socket lives in the owner's private tree, never in the shared
// /tmp/tmux-<uid> that tmux picks by default. That default is the whole
// problem: it is derived from the uid, so on a shared volume two users could
// end up naming the same socket, and anything that could reach it could
// attach to somebody else's terminal. Application-level separation would not
// help — the socket is the authority.
//
// The agent runs as a command typed into a shell rather than as the window's
// process, so the window survives it. That is what makes "look at what it
// did, then tell it one more thing" possible in the same context instead of
// a fresh session that has to rediscover everything.
func runResident(uid int64, id, wt string, argv []string) error {
	// Say why, once, in a sentence an operator can act on. Without this the
	// pod simply fails and the Session reports "agent finished (Failed)",
	// which points at the agent rather than at the image.
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("resident sessions need tmux, and it is not in this Cell's image (%s): "+
			"install it in the devbox image, or dispatch without resident", os.Getenv("AGENTCELL_IMAGE"))
	}
	sock := ids.TmuxSocket(uid)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return fmt.Errorf("tmux socket dir: %w", err)
	}
	if err := os.Chmod(filepath.Dir(sock), 0o700); err != nil {
		return err
	}
	window := ids.TmuxWindow(id)
	if out, err := exec.Command("tmux", "-S", sock, "new-session", "-d",
		"-s", window, "-c", wt).CombinedOutput(); err != nil {
		return fmt.Errorf("tmux new-session: %v: %s", err, out)
	}
	// The marker is how anything outside the pod can tell "still working"
	// from "waiting for you" without a Kubernetes token in here (ADR-0005).
	done := runtimeapi.DoneMarker
	_ = os.Remove(done)
	line := shellJoin(argv) + "; printf '%s' \"$?\" > " + shellQuote(done)
	if out, err := exec.Command("tmux", "-S", sock, "send-keys", "-t", window,
		line, "Enter").CombinedOutput(); err != nil {
		return fmt.Errorf("tmux send-keys: %v: %s", err, out)
	}
	fmt.Printf("session %s: resident in tmux %s (socket %s), window %s\n", id, window, sock, window)

	// PID 1 from here on: hold the pod so the slot stays attachable. The
	// controller ends it — TTL, an explicit settle, or the Cell going away —
	// which is what keeps mandatory settle intact.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	for {
		select {
		case <-stop:
			fmt.Println("session: stop requested, leaving the worktree for settle")
			return nil
		case <-time.After(30 * time.Second):
			if err := exec.Command("tmux", "-S", sock, "has-session", "-t", window).Run(); err != nil {
				// The user killed the window: nothing left to attach to, so
				// stop holding the slot and let settle run.
				fmt.Println("session: tmux window gone, releasing the slot")
				return nil
			}
		}
	}
}

// shellJoin renders argv as one command line for send-keys. The agent's own
// arguments are operator- and task-supplied, so they are quoted rather than
// pasted: a task containing a semicolon must not become two commands.
func shellJoin(argv []string) string {
	out := make([]string, 0, len(argv))
	for _, a := range argv {
		out = append(out, shellQuote(a))
	}
	return strings.Join(out, " ")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func waitForRepo(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(ids.RepoPath, ".git")); err == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("repo not cloned after %s (anchor still starting?)", timeout)
}

// ensurePrivateHome creates this user's private tree on the shared project
// volume (ADR-0009).
//
// 0700 is the whole mechanism: the volume is shared with every other user's
// pods, but those pods run as different uids, so the mode bits are what
// actually withhold one person's worktrees, CLI state, transcripts and tmux
// sockets from another. Nothing here may be group- or world-readable, even
// by accident — hence the explicit Chmod: MkdirAll honours the umask and
// would silently leave 0755 where the umask is the usual 022.
func ensurePrivateHome(uid int64) error {
	home := ids.UserHome(uid)
	// The parent is project-layer infrastructure and must be created as
	// such. Letting MkdirAll create it implicitly gives it the 0700 of
	// whichever user arrives first and locks every other user out of the
	// whole tree — a failure that only appears once two people work in one
	// Cell, which is exactly when it matters.
	//
	// Sticky, because /workspace is world-writable: without it any user can
	// delete another's private directory. They still cannot read it, but
	// "cannot read" is not the whole property worth having.
	if err := ensureSharedParent(filepath.Dir(home)); err != nil {
		return err
	}
	for _, dir := range []string{home, filepath.Join(home, "worktrees"), filepath.Join(home, "home")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("private home %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("private home %s: %w", dir, err)
		}
	}
	// Point the agent CLI at it: Codex/Claude/pi all keep configuration,
	// credentials and transcripts under $HOME, and those are exactly the
	// things that must not be shared.
	return os.Setenv("HOME", filepath.Join(home, "home"))
}

// ensureSharedParent creates the directory that holds every user's private
// tree: group-writable so each user can make their own, sticky so only the
// owner can remove it.
//
// Chmod failures are tolerated: if the directory already exists and belongs
// to the project identity, we are not its owner and cannot chmod it — but it
// already has the mode we want, so there is nothing to fix.
func ensureSharedParent(dir string) error {
	if err := os.MkdirAll(dir, 0o775); err != nil {
		return fmt.Errorf("users directory %s: %w", dir, err)
	}
	_ = os.Chmod(dir, 0o775|os.ModeSticky)
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o070 == 0 {
		return fmt.Errorf("users directory %s is %v — not group-writable, so other users cannot create their own trees", dir, info.Mode())
	}
	return nil
}

// runTell types another instruction into this session's tmux window.
//
// It runs INSIDE the session pod, as the session's own user, so it needs no
// credential and cannot address anyone else's tmux: the socket path is
// derived from the uid it is running as, and that socket is 0700.
//
// The text arrives as one argv element and is handed to tmux as one
// argument. It is never spliced into a shell string — it is user input, and a
// semicolon in a task description must stay a semicolon rather than becoming
// the start of another command.
func runTell(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("tell: need <session-id> [-say] <argv...>")
	}
	id, args := args[0], args[1:]
	sock := ids.TmuxSocket(int64(os.Getuid()))

	// -say means the window already holds a running agent, so this is
	// something to TYPE AT IT — not a command to run.
	//
	// The difference is the whole conversation. Sending `kimi -p "…"` to a
	// window that has Kimi open in it would either queue as keystrokes the
	// agent reads as text, or start a second agent inside the first. What a
	// person does here is type a sentence and press Enter, and that is
	// exactly what this does.
	if args[0] == "-say" {
		if len(args) < 2 {
			return fmt.Errorf("tell -say: need the text")
		}
		text := strings.Join(args[1:], " ")
		w := ids.TmuxWindow(id)
		// -l: literal. The text is somebody's sentence; a leading dash or a
		// word tmux would read as a key name must stay a word.
		if out, err := tmux(sock, "send-keys", "-t", w, "-l", text); err != nil {
			return fmt.Errorf("tmux send-keys: %v: %s", err, out)
		}
		// Enter separately, after a beat: some CLIs read the line the
		// instant they see a newline, and a full-screen editor that has not
		// finished processing the paste would submit half a sentence.
		time.Sleep(150 * time.Millisecond)
		if out, err := tmux(sock, "send-keys", "-t", w, "Enter"); err != nil {
			return fmt.Errorf("tmux send-keys Enter: %v: %s", err, out)
		}
		fmt.Println("said")
		return nil
	}

	if err := sendCommand(sock, ids.TmuxWindow(id), args, true, id, ""); err != nil {
		return err
	}
	fmt.Println("sent")
	return nil
}

// runAttach attaches the caller's terminal to this session's tmux window.
//
// Everything it needs — the socket path and the window name — is derived
// inside the pod from the uid it runs as and the session id in the
// environment. That is deliberate: an operator attaching should not have to
// know, or be told, another user's private paths.
func runAttach(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("attach: need <session-id> [viewer-id]")
	}
	id := args[0]
	// One grouped session PER VIEWER, not per session.
	//
	// Sharing v-<session> between browsers made any single tab closing kill
	// the view for everybody else watching — and, because a tmux client is
	// counted at the SERVER, it also made "somebody is watching session A"
	// read as "somebody is watching every session this user has", so a
	// sibling session could never go idle.
	viewer := ""
	if len(args) == 2 {
		viewer = args[1]
	}
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("attach: tmux is not in this image")
	}
	sock := ids.TmuxSocket(int64(os.Getuid()))
	window := ids.TmuxWindow(id)

	// A session's terminal is a WINDOW inside this user's one tmux server,
	// and `attach -t` addresses sessions, not windows. Attaching to the
	// holder directly would also mean every viewer shares one cursor: two
	// people looking at two different sessions would fight over which window
	// is current.
	//
	// So each session gets a grouped session — same windows, independent
	// current-window — which is exactly the tmux idiom for this. It is
	// created on demand and reused by later viewers of the same session.
	//
	// Deliberately NOT destroy-unattached: setting it here destroys the
	// session immediately, because at this instant nobody is attached yet.
	// The grouped sessions are one per session id, hold no windows of their
	// own, and go away with the runtime pod — cheap enough that reaping them
	// is not worth a race.
	view := viewName(id, viewer)
	if out, err := tmux(sock, "new-session", "-d", "-s", view, "-t", ids.TmuxHolder); err != nil &&
		!strings.Contains(out, "duplicate session") {
		return fmt.Errorf("attach: %v: %s", err, out)
	}
	if out, err := tmux(sock, "select-window", "-t", view+":"+window); err != nil {
		return fmt.Errorf("attach: this session has no window open (%s)", strings.TrimSpace(out))
	}
	// Replace this process so the terminal is tmux's, not ours.
	return syscall.Exec(tmuxBin, []string{"tmux", "-S", sock, "attach", "-t", view}, os.Environ())
}

// runDetach tears down a viewer's grouped session.
//
// It exists because a tmux client does NOT die when the exec stream that
// carried it goes away: closing the browser tab left `tmux attach` running
// in the pod forever. That is not merely untidy — "is anybody watching" is
// the signal reclamation hangs off, so a leaked client pins the session
// awake permanently and nothing is ever reclaimed.
//
// Killing the grouped session is safe: it owns no windows of its own, only a
// view onto the holder's.
func runDetach(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("detach: need <session-id> [viewer-id]")
	}
	viewer := ""
	if len(args) == 2 {
		viewer = args[1]
	}
	sock := ids.TmuxSocket(int64(os.Getuid()))
	// Kills only THIS viewer's grouped session, so closing one tab does not
	// throw everybody else out.
	out, err := tmux(sock, "kill-session", "-t", viewName(args[0], viewer))
	if err != nil && !strings.Contains(out, "can't find session") {
		return fmt.Errorf("detach: %v: %s", err, out)
	}
	fmt.Println("detached")
	return nil
}

// viewName is one viewer's window onto one session. Prefixed by session so
// "is anybody watching THIS session" is answerable by prefix, and suffixed
// per viewer so tabs do not share a fate.
func viewName(id, viewer string) string {
	if viewer == "" {
		return "v-" + id
	}
	return "v-" + id + "-" + viewer
}

// runBranches reports the project's branches, read from the checkout itself.
//
// From the repository rather than from a forge API, because the forge is
// whatever this deployment happens to use — GitHub, GitLab, something
// self-hosted — and a branch list should not depend on which. The anchor
// already holds a real clone; asking it is both accurate and free.
//
// One line per branch: name<TAB>ahead<TAB>behind<TAB>when<TAB>subject.
// Ahead/behind are measured against the base branch, which is the only
// comparison that answers the question people actually have — is this
// merged, and how far has it drifted.
func runBranches(args []string) error {
	base := "main"
	if len(args) == 1 && args[0] != "" {
		base = args[0]
	}
	// Every repository, each against ITS OWN base branch. A project group's
	// repositories are separate on the forge and need not agree on what the
	// base is called — a project with a `master` and a `main` is ordinary —
	// so one shared answer would be wrong for at least one of them.
	repos := reposFromEnv()
	for _, rp := range repos {
		b := rp.Branch
		if b == "" {
			b = base
		}
		name := ""
		if len(repos) > 1 {
			name = rp.Name
		}
		if err := branchesOf(ids.RepoDir(rp.Path), b, name); err != nil {
			return err
		}
	}
	return nil
}

// branchesOf prints one repository's branches. The repository column is
// empty for a single-repo project, so that output is unchanged.
func branchesOf(dir, base, repoName string) error {
	if err := ensureRepoTrusted(dir); err != nil {
		return err
	}
	// Refresh first: the anchor's clone is long-lived, and a branch pushed by
	// a settle job minutes ago is exactly the one somebody is looking for.
	_, _ = gitOut(dir, "fetch", "--prune", "--quiet")

	out, err := gitOut(dir, "for-each-ref", "--sort=-committerdate",
		"--format=%(refname:short)%09%(committerdate:relative)%09%(contents:subject)",
		"refs/remotes/origin")
	if err != nil {
		return fmt.Errorf("branches: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		name := strings.TrimPrefix(parts[0], "origin/")
		if name == "HEAD" {
			continue
		}
		ahead, behind := "0", "0"
		if name != base {
			if c, err := gitOut(dir, "rev-list", "--left-right", "--count",
				"origin/"+base+"..."+parts[0]); err == nil {
				if f := strings.Fields(strings.TrimSpace(c)); len(f) == 2 {
					behind, ahead = f[0], f[1]
				}
			}
		}
		when, subject := "", ""
		if len(parts) > 1 {
			when = parts[1]
		}
		if len(parts) > 2 {
			subject = parts[2]
		}
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", name, ahead, behind, when, subject, repoName)
	}
	return nil
}
