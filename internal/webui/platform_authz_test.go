package webui

import "testing"

// The rule table, pinned. These are the platform-scope rules in force; a
// change here is a change to who may do what on the deployment, which is the
// kind of change that should be impossible to make by accident.
func TestPlatformRules(t *testing.T) {
	admin := grants{state: standingKnown, admin: true, canCreate: true}
	granted := grants{state: standingKnown, canCreate: true}
	plain := grants{state: standingKnown}
	noRow := grants{state: standingNoRow}

	for _, c := range []struct {
		name  string
		act   PlatformAction
		g     grants
		allow bool
		rule  string
	}{
		{"管理员可以邀请", PlatformInvite, admin, true, "admin"},
		{"开通了建项目的人不能邀请", PlatformInvite, granted, false, "not-admin"},
		{"普通人不能邀请", PlatformInvite, plain, false, "not-admin"},

		{"管理员可以建项目", PlatformCreateProject, admin, true, "admin"},
		{"开通过的人可以建项目", PlatformCreateProject, granted, true, "granted"},
		{"没开通的人不能建项目", PlatformCreateProject, plain, false, "no-create-grant"},

		// The compatibility rule, and the reason it is a rule rather than an
		// accident: an identity with no account row predates this grant.
		// Denying it would withdraw something existing deployments already
		// do, on upgrade, silently.
		{"没有账号行的身份不受建项目授权约束", PlatformCreateProject, noRow, true, "no-account-row"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := platformDecision(c.act, c.g)
			if d.Allow != c.allow {
				t.Errorf("allow = %v, want %v (rule %q, reason %q)", d.Allow, c.allow, d.Rule, d.Reason)
			}
			if d.Rule != c.rule {
				t.Errorf("rule = %q, want %q", d.Rule, c.rule)
			}
		})
	}
}

// The store being unreachable must never widen anyone's authority.
//
// This is the whole point of separating "no account row" from "could not
// read the account row". They used to be one value, so a database hiccup
// landed in the compatibility branch below and let ANY caller create
// projects — an outage that hands out permissions. A control plane that
// cannot be read is allowed to stop work; it is not allowed to grant it.
func TestAnUnreadableStoreDeniesEverything(t *testing.T) {
	down := grants{state: standingUnavailable}
	for _, a := range []PlatformAction{PlatformInvite, PlatformCreateProject} {
		d := platformDecision(a, down)
		if d.Allow {
			t.Errorf("%s was permitted while the account store was unreadable (rule %q)", a, d.Rule)
		}
		if d.Rule != "store-unavailable" {
			t.Errorf("%s: rule = %q, want store-unavailable", a, d.Rule)
		}
	}
}

// Including for administrators.
//
// "Let admins through during an outage" is the tempting exception and it is
// self-defeating: administrator is precisely the claim that cannot be
// checked while the store is down, so the exception is available to anyone
// asserting it.
func TestAnOutageDoesNotSpareAdministrators(t *testing.T) {
	// admin: true is deliberately set — a caller who WAS an admin, whose
	// standing simply cannot be confirmed right now.
	d := platformDecision(PlatformInvite, grants{state: standingUnavailable, admin: true})
	if d.Allow {
		t.Fatal("an unverifiable admin claim was honoured during a store outage")
	}
}

// Fail-closed is only survivable if something still works.
//
// The deployment token is that something: it is resolved before the store is
// consulted, so an operator can still act when the store is down — which is
// exactly when somebody needs to. If this test fails, fail-closed has become
// a way to lock the operator out of their own deployment during an incident.
func TestTheBreakGlassPathSurvivesAnOutage(t *testing.T) {
	for _, a := range []PlatformAction{PlatformInvite, PlatformCreateProject} {
		d := platformDecision(a, grants{state: standingBootstrap})
		if !d.Allow {
			t.Errorf("%s: the deployment token was refused — there is now no way in during an incident", a)
		}
	}
}

// A deleted account whose cookie outlived it is refused, and says so.
//
// Separate from the outage reason because the two want different responses
// from whoever reads the log: one is "fix the database", the other is
// "this is working as intended".
func TestADeletedAccountIsRefusedByName(t *testing.T) {
	d := platformDecision(PlatformCreateProject, grants{state: standingDeleted, admin: true})
	if d.Allow {
		t.Fatal("an account that no longer exists was allowed to create a project")
	}
	if d.Rule != "account-deleted" {
		t.Errorf("rule = %q, want account-deleted", d.Rule)
	}
}

// An identity with no account row must NOT be waved through the invite door
// the way it is through the project door.
//
// The two doors differ on purpose and the difference is easy to lose: the
// create-project grant is new and has to stay backward compatible, while
// "only administrators invite" has always been true and has no legacy to
// preserve. A refactor that unified them would open the wrong one.
func TestNoAccountRowDoesNotBecomeAnInviter(t *testing.T) {
	if d := platformDecision(PlatformInvite, grants{state: standingNoRow}); d.Allow {
		t.Fatalf("an identity with no account row was allowed to invite: rule %q", d.Rule)
	}
}

// Every decision explains itself.
//
// This is the property that separates a control plane from a pile of checks:
// a bare `if p.Admin` can be correct and still leave "why can't he get in"
// answerable only by reading code. The reason is also what the audit line
// carries, so an empty one is a hole in the record.
func TestEveryDecisionCarriesAReason(t *testing.T) {
	for _, a := range []PlatformAction{PlatformInvite, PlatformCreateProject, "made-up"} {
		for _, st := range []standing{
			standingNoRow, standingKnown, standingUnavailable,
			standingDeleted, standingNoAccountSystem, standingBootstrap,
		} {
			for _, adm := range []bool{true, false} {
				d := platformDecision(a, grants{state: st, admin: adm, canCreate: adm})
				if d.Reason == "" {
					t.Errorf("%s state=%d admin=%v: decided %v with no reason", a, st, adm, d.Allow)
				}
				if d.Rule == "" {
					t.Errorf("%s state=%d admin=%v: decided %v with no rule named", a, st, adm, d.Allow)
				}
			}
		}
	}
}

// A verb the table was never taught is refused, not permitted.
//
// The alternative — an unknown action falling through to allow — is the
// worst possible way to discover that somebody added a verb and forgot the
// table, because nothing fails until it is exploited.
func TestAnUnknownActionIsRefused(t *testing.T) {
	d := platformDecision("something-nobody-implemented", grants{state: standingKnown, admin: true})
	if d.Allow {
		t.Fatal("an unknown action was permitted — and to an admin, so no test would ever have caught it")
	}
	if d.Rule != "unknown-action" {
		t.Errorf("rule = %q, want unknown-action", d.Rule)
	}
}

// Err is nil exactly when the decision allows.
func TestErrMirrorsTheDecision(t *testing.T) {
	if err := allow("r", "fine").Err(); err != nil {
		t.Errorf("an allowing decision produced an error: %v", err)
	}
	d := deny("r", "拦住了")
	if err := d.Err(); err == nil {
		t.Fatal("a denial produced no error")
	} else if err.Error() != "拦住了" {
		t.Errorf("the error lost the reason: %q", err.Error())
	}
}
