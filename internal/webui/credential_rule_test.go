package webui

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/zippo1908/agentcell/internal/identity"
)

func secretOf(name string, t corev1.SecretType, owner string) *corev1.Secret {
	labels := map[string]string{}
	if owner != "" {
		labels[OwnerLabel] = owner
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Type:       t,
	}
}

// Listing and using must be the SAME rule.
//
// They were two, and they disagreed: the create-a-project form offers every
// unowned forge credential to everybody, while the check on the way in
// treated unowned as operator-only. With exactly one credential visible the
// form preselects it — so a colleague following the form exactly got 404
// "not found" on a field they never touched.
func TestAnUnownedForgeCredentialIsThePlatformsAndUsableByAnybody(t *testing.T) {
	somebody := identity.Principal{Subject: identity.UserSubject("li@tinci.com"), Kind: identity.KindUser}

	if !mayUseCredentialSecret(somebody, secretOf("git-cred-web", corev1.SecretTypeBasicAuth, "")) {
		t.Fatal("an unowned forge credential is the platform's; refusing it makes the form a trap")
	}
}

// A model key is somebody's budget. "Unlabelled" is not consent to spend it.
func TestAnUnownedModelKeyStaysOperatorOnly(t *testing.T) {
	somebody := identity.Principal{Subject: identity.UserSubject("li@tinci.com"), Kind: identity.KindUser}
	key := secretOf("old-key", corev1.SecretTypeOpaque, "")

	if mayUseCredentialSecret(somebody, key) {
		t.Fatal("an unowned model key must not be spendable by anybody who can log in")
	}
	// The operator's own principal still can — that is what it is for.
	if !mayUseCredentialSecret(identity.StaticToken, key) {
		t.Error("the static token is the operator; it must still reach its own credentials")
	}
}

// An owned credential is that person's, whatever kind it is.
func TestAnOwnedCredentialReachesNobodyElse(t *testing.T) {
	mine := identity.Principal{Subject: identity.UserSubject("li@tinci.com"), Kind: identity.KindUser}
	yours := identity.Principal{Subject: identity.UserSubject("wang@tinci.com"), Kind: identity.KindUser}

	for _, kind := range []corev1.SecretType{corev1.SecretTypeBasicAuth, corev1.SecretTypeOpaque} {
		sec := secretOf("li-thing", kind, mine.ID())
		if !mayUseCredentialSecret(mine, sec) {
			t.Errorf("%s: the owner cannot use their own credential", kind)
		}
		if mayUseCredentialSecret(yours, sec) {
			t.Errorf("%s: somebody else could use it", kind)
		}
	}
}
