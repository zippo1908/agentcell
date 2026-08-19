package webui

import (
	"testing"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/identity"
)

// mentionFixture builds a project whose members are real accounts, which is
// the only situation in which addressing somebody by name can work.
func mentionFixture(t *testing.T, emails ...string) (*Handler, *acv1.Cell) {
	t.Helper()
	a := accountsFixture(t)
	ctx := t.Context()
	cell := &acv1.Cell{}
	for _, e := range emails {
		id := identity.Principal{Subject: identity.UserSubject(e)}.ID()
		if err := a.DB.CreateUser(ctx, id, e, "", "h", false, false); err != nil {
			t.Fatal(err)
		}
		cell.Spec.Members = append(cell.Spec.Members, acv1.Member{UserID: id, Role: acv1.RoleMember})
	}
	return &Handler{Auth: &Authenticator{Accounts: a}}, cell
}

func idOf(email string) string {
	return identity.Principal{Subject: identity.UserSubject(email)}.ID()
}

// The whole point: somebody can be addressed by the name they are known by.
//
// Before this, a mention had to be the hashed user id — so in practice
// nobody ever addressed anybody, and the feature was unreachable rather than
// broken, which is harder to notice.
func TestAPersonCanBeMentionedByTheNameOnTheirAddress(t *testing.T) {
	h, cell := mentionFixture(t, "zhumingze@us.tinci.com")

	got := h.resolveMentions(t.Context(), cell, "@zhumingze 看一下这个")

	if len(got) != 1 || got[0] != idOf("zhumingze@us.tinci.com") {
		t.Fatalf("want the member resolved, got %v", got)
	}
}

// A whole address is ONE mention, not a name followed by pieces of a domain.
//
// The obvious version of this test — "@li@cn.tinci.com resolves to Li" —
// passes even when the address is torn apart, because the first fragment
// happens to be Li. So the project here also has a member called "cn", and
// the address must not reach them: that only holds if the token is taken
// whole.
func TestAWholeAddressIsOneMentionNotItsFragments(t *testing.T) {
	h, cell := mentionFixture(t, "li@cn.tinci.com", "cn@tinci.com")

	got := h.resolveMentions(t.Context(), cell, "@li@cn.tinci.com 看一下")

	if len(got) != 1 || got[0] != idOf("li@cn.tinci.com") {
		t.Fatalf("want exactly Li and nobody else, got %v", got)
	}
}

// Two colleagues sharing a local part must not make "@li" a coin toss.
// Delivering to the wrong person is worse than not delivering.
func TestAnAmbiguousNameReachesNobody(t *testing.T) {
	h, cell := mentionFixture(t, "li@us.tinci.com", "li@cn.tinci.com")

	if got := h.resolveMentions(t.Context(), cell, "@li 帮个忙"); len(got) != 0 {
		t.Fatalf("an ambiguous name must not pick somebody, got %v", got)
	}
	// ...but the full address still reaches exactly one of them.
	got := h.resolveMentions(t.Context(), cell, "@li@cn.tinci.com 帮个忙")
	if len(got) != 1 || got[0] != idOf("li@cn.tinci.com") {
		t.Fatalf("the unambiguous form must still work, got %v", got)
	}
}

func TestSomebodyOutsideTheProjectIsNotReachable(t *testing.T) {
	h, cell := mentionFixture(t, "in@tinci.com")
	// An account exists, but is not on this project.
	if err := h.Auth.Accounts.DB.CreateUser(t.Context(),
		idOf("out@tinci.com"), "out@tinci.com", "", "h", false, false); err != nil {
		t.Fatal(err)
	}

	if got := h.resolveMentions(t.Context(), cell, "@out 看一下"); len(got) != 0 {
		t.Fatalf("a non-member must not be addressable, got %v", got)
	}
}

// The rule this file's subject has claimed from the start: an @ that matched
// nothing says so. It was true for the agent and quietly false for people.
func TestAnUnmatchedNameIsReported(t *testing.T) {
	h, cell := mentionFixture(t, "zhumingze@us.tinci.com")
	cell.Name = "shop"
	body := "@zhumingze @nosuchperson @bot 看一下"

	resolved := h.resolveMentions(t.Context(), cell, body)
	miss := h.unresolvedMentions(t.Context(), cell, body, resolved)

	if len(miss) != 1 || miss[0] != "nosuchperson" {
		t.Fatalf("want exactly the unknown name reported, got %v", miss)
	}
}

// The project's own name and the bot aliases are how the agent is addressed;
// reporting them as missing people would make every dispatch noisy.
func TestAddressingTheAgentIsNotAMissingPerson(t *testing.T) {
	h, cell := mentionFixture(t, "zhumingze@us.tinci.com")
	cell.Name = "shop"

	for _, body := range []string{"@shop 改一下", "@bot 改一下", "@agent 改一下", "@ai 改一下"} {
		if miss := h.unresolvedMentions(t.Context(), cell, body, nil); len(miss) != 0 {
			t.Errorf("%q reported %v as missing people", body, miss)
		}
	}
}

// An open project has no member list, so nothing can be judged wrong.
func TestAnOpenProjectReportsNothing(t *testing.T) {
	h, cell := mentionFixture(t)

	if miss := h.unresolvedMentions(t.Context(), cell, "@whoever 看一下", nil); len(miss) != 0 {
		t.Fatalf("an open project has nobody to be wrong about, got %v", miss)
	}
}
