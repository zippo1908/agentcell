package egress

import "testing"

func enforcing(rules ...Rule) Policy { return Policy{Rules: rules} }

// A literal address defeats a name-based allowlist entirely, so it is
// refused even while the policy is still only observing.
//
// `CONNECT 203.0.113.7:443` reaches anywhere without naming anything, and
// there is nothing to learn from letting it through: an address is not a
// destination anybody can review later. If this test fails, the allowlist is
// decoration — one line bypasses all of it.
func TestALiteralAddressIsAlwaysRefused(t *testing.T) {
	for _, p := range []Policy{
		enforcing(Rule{Host: "*.anthropic.com"}),
		{Rules: []Rule{{Host: "*.anthropic.com"}}, Observe: true},
	} {
		for _, ip := range []string{"203.0.113.7", "8.8.8.8", "::1", "[2001:db8::1]"} {
			if v := p.Check(ip, 443); v.Allow {
				t.Errorf("observe=%v: CONNECT to %s was allowed — the allowlist is bypassable", p.Observe, ip)
			}
		}
	}
}

// A wildcard covers subdomains and NOT the apex.
//
// Writing `*.example.com` and silently getting `example.com` too is a larger
// grant than the one somebody wrote down, and the difference matters: the
// apex is very often the thing serving user content.
func TestWildcardDoesNotReachTheApex(t *testing.T) {
	p := enforcing(Rule{Host: "*.anthropic.com"})
	if v := p.Check("api.anthropic.com", 443); !v.Allow {
		t.Error("a subdomain was refused by its own wildcard")
	}
	if v := p.Check("a.b.anthropic.com", 443); !v.Allow {
		t.Error("a nested subdomain was refused")
	}
	if v := p.Check("anthropic.com", 443); v.Allow {
		t.Error("the apex was allowed by a *. rule — that is a wider grant than was written")
	}
	// And the classic suffix confusion.
	if v := p.Check("evilanthropic.com", 443); v.Allow {
		t.Error("evilanthropic.com matched *.anthropic.com — suffix matching without the dot")
	}
	if v := p.Check("anthropic.com.attacker.net", 443); v.Allow {
		t.Error("a domain merely CONTAINING the allowed name was permitted")
	}
}

// Case and a trailing dot are the same destination.
func TestHostIsNormalized(t *testing.T) {
	p := enforcing(Rule{Host: "API.Anthropic.Com"})
	for _, h := range []string{"api.anthropic.com", "API.ANTHROPIC.COM", "api.anthropic.com."} {
		if v := p.Check(h, 443); !v.Allow {
			t.Errorf("%q did not match its own rule", h)
		}
	}
}

// A rule allows one port, not every port the host happens to listen on.
func TestAPortIsPartOfTheGrant(t *testing.T) {
	p := enforcing(Rule{Host: "git.tinci.com", Port: 6006})
	if v := p.Check("git.tinci.com", 6006); !v.Allow {
		t.Error("the named port was refused")
	}
	if v := p.Check("git.tinci.com", 443); v.Allow {
		t.Error("a port nobody granted was allowed")
	}
	// Default is 443 and only 443.
	q := enforcing(Rule{Host: "github.com"})
	if v := q.Check("github.com", 443); !v.Allow {
		t.Error("443 is not allowed by default")
	}
	if v := q.Check("github.com", 22); v.Allow {
		t.Error("22 was allowed by a rule that named no port")
	}
}

// Observe mode lets an unlisted destination through — and says so.
//
// The flag on the verdict is the whole point: without it a log full of
// "allowed" reads as a complete allowlist, when in fact nothing has been
// decided yet. The list is meant to be DISCOVERED from these lines.
func TestObserveModeAllowsButMarksIt(t *testing.T) {
	p := Policy{Rules: []Rule{{Host: "api.anthropic.com"}}, Observe: true}
	v := p.Check("unknown.example.com", 443)
	if !v.Allow {
		t.Fatal("observe mode refused something — it is supposed to only watch")
	}
	if !v.Observed {
		t.Error("an unlisted destination was allowed without being marked as unlisted")
	}
	// A listed one is a real allow, not an observation.
	if v := p.Check("api.anthropic.com", 443); !v.Allow || v.Observed {
		t.Error("a listed destination was recorded as merely observed")
	}
}

// Enforcing refuses what is not listed.
func TestEnforcingRefusesTheUnlisted(t *testing.T) {
	p := enforcing(Rule{Host: "api.anthropic.com"})
	v := p.Check("attacker.example", 443)
	if v.Allow {
		t.Fatal("an unlisted destination was allowed while enforcing")
	}
	if v.Reason == "" {
		t.Error("a refusal with no reason — the audit line would say nothing")
	}
}

// Every verdict explains itself, allow or deny.
func TestEveryVerdictHasAReason(t *testing.T) {
	for _, p := range []Policy{
		enforcing(Rule{Host: "a.example.com", Note: "模型 API"}),
		{Rules: []Rule{{Host: "a.example.com"}}, Observe: true},
	} {
		for _, h := range []string{"a.example.com", "b.example.com", "1.2.3.4", ""} {
			if v := p.Check(h, 443); v.Reason == "" {
				t.Errorf("observe=%v host=%q: decided allow=%v with no reason", p.Observe, h, v.Allow)
			}
		}
	}
}

// An empty policy that is enforcing allows nothing. Fail closed.
func TestAnEmptyEnforcingPolicyAllowsNothing(t *testing.T) {
	if v := (Policy{}).Check("api.anthropic.com", 443); v.Allow {
		t.Fatal("an empty allowlist allowed a destination")
	}
}

// Attribution reports honestly when it could not identify anybody.
func TestUnattributedIsNotSilentlyBlamedOnNobody(t *testing.T) {
	a := Attribution{Pod: "runtime-x", IP: "10.42.0.9"}
	if a.Known() {
		t.Error("an attribution with no principal claims to know who it was")
	}
	if a.Pod == "" || a.IP == "" {
		t.Error("an unattributed request kept no evidence at all")
	}
}
