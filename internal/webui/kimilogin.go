package webui

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/identity"
)

// Connecting a Kimi Code account, instead of pasting an API key.
//
// Kimi authenticates with a device-code flow, which is the one OAuth shape
// built for a machine with no browser — exactly a pod's situation. The CLI
// prints a URL and a short code; somebody approves it in their own browser;
// the CLI receives a long-lived credential and writes it under
// $KIMI_CODE_HOME/credentials/. That credential moves with the directory, so
// once captured it can be handed to every session the person starts.
//
// Two boundaries decide the whole shape of this file:
//
//   - the credential must not travel through POD LOGS, which anyone with log
//     access in this namespace can read. Exec output goes only to the caller,
//     so the login runs over the exec channel and its output is never logged;
//   - it must not appear in a POD SPEC either. It is stored in a Secret owned
//     by the person, and reaches a session the same way their model key does:
//     by reference, resolved by kubelet.
//
// What this deliberately does NOT do is run the login inside a session pod.
// A session runs repository code and a model's output; handing it the
// interactive login of a human account would be handing that code the
// account.

const (
	// kimiCredKey is where the captured credential lives inside the user's
	// Secret: a base64 tar of the credentials directory, because the CLI
	// writes more than one file and their names are its business, not ours.
	kimiCredKey = "kimi-credentials"
	// KimiCredentialSuffix names a user's Kimi credential Secret.
	KimiCredentialSuffix = "-kimi"
	// credKindKimi is the label VALUE marking a connected account, as opposed
	// to a model key. See credKindModel in credentials.go for why the value,
	// not merely the label, is what queries have to match on.
	credKindKimi = "kimi-oauth"
	loginPodTTL  = 30 * time.Minute
)

// deviceLine matches what the CLI prints first: a URL carrying the code.
var deviceLine = regexp.MustCompile(`(https://\S*authorize_device\S*user_code=([A-Za-z0-9-]+))`)

type kimiLoginState struct {
	// URL and Code are what a person needs; the console shows both because
	// some browsers strip the query when a link is copied by hand.
	URL  string `json:"url,omitempty"`
	Code string `json:"code,omitempty"`
	// Status: "pending" while waiting for approval, "connected" once the
	// credential is stored, "expired" when the code timed out.
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func (h *Handler) kimiLoginPod(user string) string {
	return "kimi-login-" + strings.TrimPrefix(user, "u-")
}

// startKimiLogin brings up a short-lived pod and starts the device flow in
// it, returning the URL and code as soon as the CLI prints them.
func (h *Handler) startKimiLogin(w http.ResponseWriter, r *http.Request) {
	p := identity.FromContext(r.Context())
	if p.ID() == "" {
		writeErr(w, 403, fmt.Errorf("connecting an account needs a signed-in user"))
		return
	}
	if h.RESTConfig == nil || h.Kube == nil {
		writeErr(w, 501, fmt.Errorf("this celld has no cluster exec access"))
		return
	}
	image, err := h.anyDevboxImage(r.Context())
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	name := h.kimiLoginPod(p.ID())
	if err := h.ensureLoginPod(r.Context(), name, image); err != nil {
		writeErr(w, 500, err)
		return
	}
	// Start the login detached and let it write to a file: the exec that
	// starts it must return now, because the person cannot approve a code
	// they have not been shown yet.
	_, _ = h.execInPod(r.Context(), h.Namespace, name, []string{"sh", "-c",
		`export KIMI_CODE_HOME=/tmp/kh; mkdir -p $KIMI_CODE_HOME; ` +
			`[ -f /tmp/kh/started ] || { touch /tmp/kh/started; ` +
			`setsid kimi login >/tmp/kh/login.out 2>&1 < /dev/null & }; echo ok`})

	// Poll the file rather than the stream: the CLI prints the URL within a
	// second or two, and a request that hangs until it does is a request an
	// intermediary may time out.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := h.execInPod(r.Context(), h.Namespace, name,
			[]string{"sh", "-c", "cat /tmp/kh/login.out 2>/dev/null"})
		if m := deviceLine.FindStringSubmatch(out); m != nil {
			writeJSON(w, 200, kimiLoginState{URL: m[1], Code: m[2], Status: "pending"})
			return
		}
		time.Sleep(time.Second)
	}
	writeErr(w, 504, fmt.Errorf("Kimi CLI 没有给出登录链接;镜像里可能没有 kimi"))
}

// pollKimiLogin reports whether the person has approved yet, and captures the
// credential the moment they have.
func (h *Handler) pollKimiLogin(w http.ResponseWriter, r *http.Request) {
	p := identity.FromContext(r.Context())
	// An already-connected account is the answer to this question, and it is
	// answerable without a login pod. Asking the pod first meant that once the
	// pod was cleaned up — which happens the instant a login succeeds — the
	// console told a connected person their login had "expired", and offered
	// to start one they did not need.
	if h.hasKimiCredential(r.Context(), p.ID()) {
		writeJSON(w, 200, kimiLoginState{Status: "connected"})
		return
	}
	name := h.kimiLoginPod(p.ID())
	out, err := h.execInPod(r.Context(), h.Namespace, name,
		[]string{"sh", "-c", "cat /tmp/kh/login.out 2>/dev/null"})
	if err != nil {
		writeJSON(w, 200, kimiLoginState{Status: "expired",
			Message: "登录已经结束或超时,重新开始一次"})
		return
	}
	// The credential directory appearing IS the completion signal — more
	// reliable than matching a success sentence the CLI is free to reword.
	tar, terr := h.execInPod(r.Context(), h.Namespace, name, []string{"sh", "-c",
		`[ -d /tmp/kh/credentials ] && [ -n "$(ls -A /tmp/kh/credentials 2>/dev/null)" ] && ` +
			`tar czf - -C /tmp/kh credentials | base64 -w0`})
	if terr == nil && strings.TrimSpace(tar) != "" {
		if err := h.storeKimiCredential(r.Context(), p.ID(), strings.TrimSpace(tar)); err != nil {
			writeErr(w, 500, err)
			return
		}
		// The pod held a live login; it has no further purpose and every
		// extra minute it exists is a minute somebody could exec into it.
		_ = h.Kube.CoreV1().Pods(h.Namespace).Delete(r.Context(), name, metav1.DeleteOptions{})
		writeJSON(w, 200, kimiLoginState{Status: "connected",
			Message: "Kimi 账号已连接,之后所有会话都用它"})
		return
	}
	st := kimiLoginState{Status: "pending"}
	if m := deviceLine.FindStringSubmatch(out); m != nil {
		st.URL, st.Code = m[1], m[2]
	}
	if strings.Contains(out, "expired") {
		st.Status, st.Message = "expired", "设备码过期了,重新开始一次"
	}
	writeJSON(w, 200, st)
}

// hasKimiCredential reports whether this person has a connected account.
func (h *Handler) hasKimiCredential(ctx context.Context, user string) bool {
	var sec corev1.Secret
	name := strings.TrimPrefix(user, "u-") + KimiCredentialSuffix
	if err := h.Client.Get(ctx, types.NamespacedName{Namespace: h.Namespace, Name: name}, &sec); err != nil {
		return false
	}
	return sec.Labels[OwnerLabel] == user && len(sec.Data[kimiCredKey]) > 0
}

// disconnectKimi drops the stored credential.
//
// A connected account is the one credential a person cannot rotate by pasting
// a new value, so without this the only exit from a revoked or wrong account
// is a cluster administrator — the same gap that made bring-your-own-key
// need an API in the first place.
func (h *Handler) disconnectKimi(w http.ResponseWriter, r *http.Request) {
	p := identity.FromContext(r.Context())
	name := strings.TrimPrefix(p.ID(), "u-") + KimiCredentialSuffix
	var sec corev1.Secret
	if err := h.Client.Get(r.Context(),
		types.NamespacedName{Namespace: h.Namespace, Name: name}, &sec); err != nil {
		writeJSON(w, 200, kimiLoginState{Status: "disconnected"})
		return
	}
	if sec.Labels[OwnerLabel] != p.ID() || sec.Labels[credLabel] != credKindKimi {
		writeErr(w, 404, errNotFound)
		return
	}
	if err := h.Client.Delete(r.Context(), &sec); err != nil {
		writeErr(w, 500, err)
		return
	}
	// Sessions already running keep the copy kubelet handed them; this stops
	// the NEXT one. Said plainly, because "disconnected" that silently left a
	// live session authenticated would be a lie by omission.
	writeJSON(w, 200, kimiLoginState{Status: "disconnected",
		Message: "已断开;正在跑的会话仍用着已发下去的凭据,新会话不再带它"})
}

// storeKimiCredential puts the captured credential in a Secret the person
// owns, in the same shape as their model keys — so the same ownership rules
// apply and nothing new has to be taught about who may spend what.
func (h *Handler) storeKimiCredential(ctx context.Context, user, tarB64 string) error {
	name := strings.TrimPrefix(user, "u-") + KimiCredentialSuffix
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace: h.Namespace, Name: name,
		Labels: map[string]string{credLabel: credKindKimi, OwnerLabel: user},
	}}
	sec.Data = map[string][]byte{kimiCredKey: []byte(tarB64)}
	err := h.Client.Create(ctx, sec)
	if apierrors.IsAlreadyExists(err) {
		var cur corev1.Secret
		if err := h.Client.Get(ctx, types.NamespacedName{Namespace: h.Namespace, Name: name}, &cur); err != nil {
			return err
		}
		// Only the owner may overwrite their own credential. A second person
		// re-connecting must not silently replace somebody else's account.
		if cur.Labels[OwnerLabel] != user {
			return fmt.Errorf("that credential belongs to somebody else")
		}
		cur.Data = sec.Data
		return h.Client.Update(ctx, &cur)
	}
	return err
}

// anyDevboxImage picks an image that has the kimi CLI in it. The login has to
// run somewhere, and the devbox is the only image the platform knows carries
// agent CLIs; using a Cell's own image means it matches what sessions will
// actually run, so a login that works cannot be followed by a session that
// cannot find the binary.
func (h *Handler) anyDevboxImage(ctx context.Context) (string, error) {
	var list acv1.CellList
	if err := h.Client.List(ctx, &list); err == nil {
		for i := range list.Items {
			if list.Items[i].Spec.Image != "" {
				return list.Items[i].Spec.Image, nil
			}
		}
	}
	return "", fmt.Errorf("还没有任何工作区,平台不知道该用哪个 devbox 镜像来登录")
}

func (h *Handler) ensureLoginPod(ctx context.Context, name, image string) error {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: h.Namespace, Name: name}}
	pod.Spec = corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		// No API token: this pod runs an interactive login, and nothing about
		// that needs to talk to Kubernetes.
		AutomountServiceAccountToken: new(bool),
		Containers: []corev1.Container{{
			Name:  "login",
			Image: image,
			// Idle; everything happens over exec. A TTL so a login somebody
			// walked away from does not leave a pod running for days.
			Command: []string{"sh", "-c",
				fmt.Sprintf("sleep %d", int(loginPodTTL.Seconds()))},
		}},
	}
	err := h.Client.Create(ctx, pod)
	if apierrors.IsAlreadyExists(err) {
		err = nil
	}
	if err != nil {
		return err
	}
	// Wait for it to be able to answer an exec.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var live corev1.Pod
		if e := h.Client.Get(ctx, types.NamespacedName{Namespace: h.Namespace, Name: name}, &live); e == nil {
			for _, c := range live.Status.ContainerStatuses {
				if c.Ready {
					return nil
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("登录用的临时容器没有起来")
}

// KimiCredentialFor returns a user's stored Kimi credential, or "".
func KimiCredentialFor(ctx context.Context, c interface {
	Get(context.Context, types.NamespacedName, *corev1.Secret) error
}, ns, user string) string {
	var sec corev1.Secret
	name := strings.TrimPrefix(user, "u-") + KimiCredentialSuffix
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &sec); err != nil {
		return ""
	}
	return string(sec.Data[kimiCredKey])
}
