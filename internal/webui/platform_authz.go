package webui

import (
	"context"
	"errors"
	"fmt"

	"github.com/zippo1908/agentcell/internal/identity"
	"github.com/zippo1908/agentcell/internal/store"
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
// engine: two verbs and a handful of rules fit in a table a person can read
// in one screen, and a table that can be read is worth more than a language
// that has to be learned. What it does buy is the two properties that a
// scattered `if p.Admin` cannot have no matter how correct each copy is —
//
//   - one place the answer comes from, so there is one place to change it
//   - an answer that says WHY, so "why can't he get in" is a line in a log
//     rather than an afternoon spent reading handlers
//
// Policy-as-data and a scope hierarchy come after, and both go behind this
// seam without touching a caller.
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

// standing is what the platform was able to learn about a caller — and,
// crucially, whether it was able to learn anything at all.
//
// The distinction that matters is between the last two. "This identity has
// no account row" is a FACT about a deployment that predates the
// create-projects grant, and permitting it is a deliberate compatibility
// rule. "The account store could not be read" is a FAILURE, and permitting
// on a failure means an unreachable database silently widens everybody's
// authority — the one thing an authorization system may never do. Those two
// used to be the same value here, so a store hiccup fell straight through
// the compatibility branch and let anyone create projects.
//
// The control plane being unavailable is allowed to stop work. It is not
// allowed to grant it.
type standing int

const (
	standingNoRow           standing = iota // no account row: OIDC, static token
	standingKnown                           // a row was read
	standingUnavailable                     // the store could not be read — deny
	standingDeleted                         // the account is gone — deny
	standingNoAccountSystem                 // this deployment has no accounts at all
	standingBootstrap                       // the deployment token: break-glass
)

// grants is what the platform knows about one principal's standing.
type grants struct {
	state     standing
	admin     bool
	canCreate bool
}

// platformDecision is the whole rule table, as a pure function.
//
// Pure so it can be tested as a table rather than through a database and an
// HTTP request — including the states that are hard to produce on purpose,
// like "the store is down". The rules are the part worth pinning down, and
// a test that needs a live store to check "may a non-admin invite" is
// testing the store.
func platformDecision(a PlatformAction, g grants) Decision {
	// States that answer the same way for every verb, resolved first.
	switch g.state {
	case standingBootstrap:
		// The break-glass path, and deliberately the ONLY one: the
		// deployment token is checked before the store is consulted, so it
		// keeps working when the store does not. That is what makes
		// fail-closed survivable — an operator can still get in and fix
		// the thing that broke.
		return allow("bootstrap-token", "部署令牌按定义是管理员")
	case standingNoAccountSystem:
		return allow("no-account-system", "这个部署没有账号体系,不区分角色")
	case standingUnavailable:
		// Fail closed. Note this refuses the deployment's administrators
		// too: an outage that lets admins keep working is an outage that
		// lets anyone claiming to be one keep working, since the claim is
		// exactly what cannot be checked right now.
		return deny("store-unavailable",
			"账号库暂时读不到,平台级操作已停用;这是有意的——权限不能因为控制面故障而放宽")
	case standingDeleted:
		// A live session whose account row is gone. Distinguished from the
		// state above because the two want different operator responses:
		// one is "the database is broken", the other is "this account was
		// removed and the cookie outlived it".
		return deny("account-deleted", "这个账号已经不存在了")
	}

	switch a {
	case PlatformInvite:
		if g.state == standingKnown && g.admin {
			return allow("admin", "管理员可以邀请人")
		}
		return deny("not-admin", "只有管理员能邀请人")

	case PlatformCreateProject:
		if g.state == standingNoRow {
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
// The database is asked because it is current. Preferring the session cookie
// instead is what made revocation take effect at different times in
// different places — and falling back to it when the database is unreachable
// would be worse still, because the cookie asserts an authority that nothing
// is able to check at that moment.
func (h *Handler) grantsOf(ctx context.Context, p identity.Principal) grants {
	if p.Kind == identity.KindToken {
		return grants{state: standingBootstrap}
	}
	db := h.accountsDB()
	if db == nil {
		return grants{state: standingNoAccountSystem}
	}
	if p.Kind != identity.KindUser {
		// OIDC and anything else without a row: nothing to read, and its
		// absence is a fact rather than a failure.
		return grants{state: standingNoRow}
	}
	u, _, err := db.UserByEmail(ctx, p.Email)
	if errors.Is(err, store.ErrNotFound) {
		return grants{state: standingDeleted}
	}
	if err != nil {
		return grants{state: standingUnavailable}
	}
	return grants{state: standingKnown, admin: u.Admin, canCreate: u.CanCreate || u.Admin}
}

// canPlatform answers a platform-scope question. The only door.
func (h *Handler) canPlatform(ctx context.Context, p identity.Principal, a PlatformAction) Decision {
	return platformDecision(a, h.grantsOf(ctx, p))
}
