package webui

import (
	"context"
	"fmt"

	"github.com/zippo1908/agentcell/internal/identity"
)

// Platform-scope authorization.
//
// Cell-scope decisions have had one door since the beginning: `can`, reached
// through `authorize`, with no handler deciding for itself. Platform scope —
// who may bring a project onto this deployment, who may invite — did not.
// The two checks that needed it were written inline where they were needed,
// and they drifted, in the way two copies of a rule always do: "is this
// person an administrator" was read from the signed session cookie when
// inviting and from the database when creating a project. Withdrawing
// somebody's admin therefore took effect immediately for one and only at
// their next login for the other, and nothing anywhere said so.
//
// This is the platform-scope twin of `can`. It is deliberately not a policy
// engine: two verbs and three rules fit in a table a person can read in one
// screen, and a table that can be read is worth more than a language that
// has to be learned. What it does buy is the two properties that a scattered
// `if p.Admin` cannot have no matter how correct each copy is —
//
//   - one place the answer comes from, so there is one place to change it
//   - an answer that says WHY, so "why can't he get in" is a line in a log
//     rather than an afternoon spent reading handlers
//
// Those two are what separate an authorization control plane from a
// collection of checks. Policy-as-data and a scope hierarchy come after, and
// both go behind this seam without touching a caller.
type PlatformAction string

const (
	// PlatformCreateProject: bring a new project onto the deployment.
	PlatformCreateProject PlatformAction = "create-project"
	// PlatformInvite: admit a new person to the deployment.
	PlatformInvite PlatformAction = "invite"
)

// Decision is an answer with its reason attached.
//
// Allow is what the caller enforces. Rule names the rule that decided, for
// the audit line. Reason is what the person is told — and it is set on
// allow as well as on deny, because the interesting audit question is
// usually "how did he get in", not "why was he stopped".
type Decision struct {
	Allow  bool
	Rule   string
	Reason string
}

func allow(rule, reason string) Decision {
	return Decision{Allow: true, Rule: rule, Reason: reason}
}

func deny(rule, reason string) Decision {
	return Decision{Allow: false, Rule: rule, Reason: reason}
}

// Err renders a denial as an error for the HTTP layer. Nil when allowed, so
// a caller can write `if err := d.Err(); err != nil`.
func (d Decision) Err() error {
	if d.Allow {
		return nil
	}
	return fmt.Errorf("%s", d.Reason)
}

// grants is what the platform knows about one principal's standing.
//
// known distinguishes "this identity has an account row that says no" from
// "this identity has no row at all". They are not the same answer: an OIDC
// user or a static token predates the create-projects grant and has no row
// to carry it, and quietly withdrawing something those deployments already
// do would be an upgrade that breaks them.
type grants struct {
	admin     bool
	canCreate bool
	known     bool
}

// platformDecision is the whole rule table, as a pure function.
//
// Pure so it can be tested as a table rather than through a database and an
// HTTP request — the rules are the part worth pinning down, and a test that
// needs a live store to check "may a non-admin invite" tests the store.
func platformDecision(a PlatformAction, g grants) Decision {
	switch a {
	case PlatformInvite:
		if g.admin {
			return allow("admin", "管理员可以邀请人")
		}
		return deny("not-admin", "只有管理员能邀请人")

	case PlatformCreateProject:
		if !g.known {
			return allow("no-account-row",
				"这个身份没有账号行,「创建项目」的授权不适用于它")
		}
		if g.admin {
			return allow("admin", "管理员可以创建项目")
		}
		if g.canCreate {
			return allow("granted", "你的账号开通了「创建项目」")
		}
		return deny("no-create-grant",
			"你的账号还没有开通「创建项目」;找管理员开通,或者请项目维护者把你加进已有项目")
	}
	// An action nobody taught this table about is refused rather than
	// waved through: a new verb that silently permits everyone is the
	// worst way to find out the table was not updated.
	return deny("unknown-action", "未知的操作")
}

// grantsOf resolves a principal's standing, preferring the database.
//
// The database is asked because it is current; the session's own claim is
// the fallback for identities that have no row (OIDC, static token) and for
// the moment the store cannot be reached. Preferring the cookie instead is
// what made revocation take effect at different times in different places.
func (h *Handler) grantsOf(ctx context.Context, p identity.Principal) grants {
	db := h.accountsDB()
	if db == nil || p.Kind != identity.KindUser {
		return grants{admin: p.Admin}
	}
	u, _, err := db.UserByEmail(ctx, p.Email)
	if err != nil {
		// Fall back to what the session asserts rather than locking the
		// deployment out of inviting anybody because the store hiccuped.
		return grants{admin: p.Admin}
	}
	return grants{admin: u.Admin, canCreate: u.CanCreate || u.Admin, known: true}
}

// canPlatform answers a platform-scope question. The only door.
func (h *Handler) canPlatform(ctx context.Context, p identity.Principal, a PlatformAction) Decision {
	// The bootstrap credential is an administrator by definition: it is how
	// an operator sets a deployment up before any person exists.
	if p.Kind == identity.KindToken {
		return allow("bootstrap-token", "部署令牌按定义是管理员")
	}
	if h.accountsDB() == nil {
		return allow("no-account-system", "这个部署没有账号体系,不区分角色")
	}
	return platformDecision(a, h.grantsOf(ctx, p))
}
