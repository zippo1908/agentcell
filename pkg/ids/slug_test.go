package ids

import "testing"

// Somebody types a name; the platform derives one it can use. The person is
// never asked to satisfy a DNS rule they did not ask to know about.
func TestSlugCellName(t *testing.T) {
	cases := []struct{ typed, want string }{
		{"shop", "shop"},
		{"My Shop", "my-shop"},
		{"平台运维组", ""}, // derived from a hash; checked separately
		{"  Platform Ops  ", "platform-ops"},
		{"my__project", "my-project"},
		{"my  project", "my-project"},
		{"web/front", "web-front"},
		{"---weird---", "weird"},
		{"Shop 2.0", "shop-2-0"},
	}
	for _, c := range cases {
		got := SlugCellName(c.typed)
		if c.want == "" {
			// A name with nothing usable in it still has to produce a legal
			// name, not an error — that is the whole point.
			if err := ValidateCellName(got); err != nil {
				t.Errorf("%q -> %q, which is not usable: %v", c.typed, got, err)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%q -> %q, want %q", c.typed, got, c.want)
		}
		if err := ValidateCellName(got); err != nil {
			t.Errorf("%q -> %q, which is not usable: %v", c.typed, got, err)
		}
	}
}

// The same name twice is the same slug, so a genuine collision is reported
// honestly instead of being hidden behind a random suffix.
func TestSlugIsStable(t *testing.T) {
	a := SlugCellName("平台运维组")
	b := SlugCellName("平台运维组")
	if a != b {
		t.Fatalf("the same name produced %q and %q", a, b)
	}
	if SlugCellName("平台运维组") == SlugCellName("另一个组") {
		t.Fatal("two different names collided")
	}
}

// A very long name must still fit in a DNS label.
func TestSlugFitsTheLabelLimit(t *testing.T) {
	long := ""
	for range 200 {
		long += "a"
	}
	got := SlugCellName(long)
	if err := ValidateCellName(got); err != nil {
		t.Fatalf("a long name produced %q: %v", got, err)
	}
}
