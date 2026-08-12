package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/pkg/ids"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// identity is the verified caller from the TokenReview: namespace and
// ServiceAccount, plus the bound-token pod name and uid (unforgeable).
type identity struct {
	namespace string
	saName    string
	podName   string
	podUID    string
}

// handleGit serves one git smart-HTTP request at /<cell>/<rest>.
func (s *server) handleGit(w http.ResponseWriter, r *http.Request) {
	cell, rest := splitCellPath(r.URL.Path)
	if cell == "" {
		http.Error(w, "usage: /<cell>/<git-path>", http.StatusBadRequest)
		return
	}

	// 1. Authenticate (audience-bound SA token) and authorize (namespace ↔
	//    cell, role, push-vs-fetch).
	id, err := s.authenticate(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Basic realm=agentcell")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	push := isReceivePack(r, rest)
	if err := authorize(id, cell, push); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	// 2. Resolve the real remote and its credentials from the Cell CR.
	var c acv1.Cell
	if err := s.k8s.Get(s.ctx(), types.NamespacedName{Namespace: s.controlNS, Name: cell}, &c); err != nil {
		http.Error(w, "cell not found", http.StatusNotFound)
		return
	}
	if c.Spec.Repo.SecretName == "" {
		http.Error(w, "cell has no git secret configured", http.StatusForbidden)
		return
	}
	var secret corev1.Secret
	if err := s.k8s.Get(s.ctx(), types.NamespacedName{Namespace: s.controlNS, Name: c.Spec.Repo.SecretName}, &secret); err != nil {
		http.Error(w, "git secret not found", http.StatusInternalServerError)
		return
	}
	// Unforgeable repo↔credential binding (REQUIRED, not optional): the
	// secret must declare the repo it is authorized for, and the Cell's URL
	// must match it after normalization. This closes credential-forwarding /
	// SSRF: a Cell creator cannot point a credential at another URL.
	bound := strings.TrimSpace(string(secret.Data["repo_url"]))
	if bound == "" {
		http.Error(w, "git secret is missing the required repo_url binding", http.StatusForbidden)
		return
	}
	if !sameRepo(bound, c.Spec.Repo.URL) {
		http.Error(w, "cell repo url is not authorized by this credential", http.StatusForbidden)
		return
	}
	cred, err := s.creds.credentials(s.ctx(), c.Spec.Repo.URL, secret.Data)
	if err != nil {
		http.Error(w, "credential error", http.StatusInternalServerError)
		return
	}

	// 3. A push must come from a real settle Job pod (verified by uid +
	//    owner reference) and may only update that session's own branch.
	if push && r.Method == http.MethodPost {
		sessionID, err := s.settleSession(id)
		if err != nil {
			http.Error(w, "push identity: "+err.Error(), http.StatusForbidden)
			return
		}
		if s.enforceRef {
			gz := strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip")
			newBody, perr := enforcePushPolicy(r.Body, gz, sessionID)
			if perr != nil {
				http.Error(w, "push rejected: "+perr.Error(), http.StatusForbidden)
				return
			}
			r.Body = io.NopCloser(newBody)
		}
	}

	// 4. Proxy to the real remote with the real credential injected. The
	//    workload's SA token is replaced, never forwarded onward.
	target, err := url.Parse(c.Spec.Repo.URL)
	if err != nil {
		http.Error(w, "bad remote url", http.StatusInternalServerError)
		return
	}
	basePath := strings.TrimRight(target.Path, "/")
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			req.URL.Path = basePath + "/" + rest
			req.SetBasicAuth(cred.username, cred.password)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// authenticate validates the request's basic-auth password as an
// audience-bound ServiceAccount token and returns the caller identity.
func (s *server) authenticate(r *http.Request) (identity, error) {
	_, token, ok := r.BasicAuth()
	if !ok || token == "" {
		return identity{}, fmt.Errorf("no bearer token")
	}
	tr := &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{
		Token:     token,
		Audiences: []string{runtimeapi.BrokerAudience},
	}}
	res, err := s.auth.AuthenticationV1().TokenReviews().Create(s.ctx(), tr, metav1.CreateOptions{})
	if err != nil {
		return identity{}, err
	}
	if !res.Status.Authenticated {
		return identity{}, fmt.Errorf("token not authenticated")
	}
	// Fail closed: the token must be explicitly scoped to the broker. An
	// empty audiences response means the token is only valid for the
	// apiserver, not us — reject it.
	if !contains(res.Status.Audiences, runtimeapi.BrokerAudience) {
		return identity{}, fmt.Errorf("token audience does not include the broker")
	}
	return identityFromReview(res.Status.User)
}

func identityFromReview(u authnv1.UserInfo) (identity, error) {
	// Username is "system:serviceaccount:<namespace>:<sa>".
	parts := strings.Split(u.Username, ":")
	if len(parts) != 4 || parts[0] != "system" || parts[1] != "serviceaccount" {
		return identity{}, fmt.Errorf("not a service account token: %q", u.Username)
	}
	id := identity{namespace: parts[2], saName: parts[3]}
	if v, ok := u.Extra["authentication.kubernetes.io/pod-name"]; ok && len(v) > 0 {
		id.podName = v[0]
	}
	if v, ok := u.Extra["authentication.kubernetes.io/pod-uid"]; ok && len(v) > 0 {
		id.podUID = v[0]
	}
	return id, nil
}

// authorize enforces namespace↔cell binding and the per-role capability:
// only the settle role may push; anchor and prod may only fetch.
func authorize(id identity, cell string, push bool) error {
	if id.namespace != ids.WorkloadNamespace(cell) {
		return fmt.Errorf("namespace %q may not act as cell %q", id.namespace, cell)
	}
	switch id.saName {
	case runtimeapi.SAAnchor, runtimeapi.SAProd:
		if push {
			return fmt.Errorf("service account %q may not push", id.saName)
		}
	case runtimeapi.SASettle:
		// push permitted; the exact branch is bound in settleSession.
	default:
		return fmt.Errorf("service account %q is not permitted", id.saName)
	}
	return nil
}

// settleSession verifies the caller is a genuine settle Job pod and returns
// the session id from the OWNING Job (unforgeable), not a caller-supplied
// value: it reads the pod, checks its uid matches the token claim, and
// requires a controller ownerReference to a Job named settle-<id>.
func (s *server) settleSession(id identity) (string, error) {
	if id.saName != runtimeapi.SASettle {
		return "", fmt.Errorf("only the settle role may push")
	}
	if id.podName == "" || id.podUID == "" {
		return "", fmt.Errorf("bound pod identity unavailable (need projected token)")
	}
	var pod corev1.Pod
	if err := s.k8s.Get(s.ctx(), types.NamespacedName{Namespace: id.namespace, Name: id.podName}, &pod); err != nil {
		return "", fmt.Errorf("pod not found")
	}
	if string(pod.UID) != id.podUID {
		return "", fmt.Errorf("pod uid mismatch")
	}
	for _, o := range pod.OwnerReferences {
		if o.Controller != nil && *o.Controller && o.Kind == "Job" {
			if sess, ok := strings.CutPrefix(o.Name, "settle-"); ok && sess != "" {
				return sess, nil
			}
		}
	}
	return "", fmt.Errorf("pod is not controlled by a settle Job")
}

func isReceivePack(r *http.Request, rest string) bool {
	if strings.HasSuffix(rest, "git-receive-pack") {
		return true
	}
	// info/refs advertisement for push.
	return rest == "info/refs" && r.URL.Query().Get("service") == "git-receive-pack"
}

// sameRepo compares two git remote URLs after normalizing scheme/host case,
// a trailing slash, and a ".git" suffix, so cosmetic differences don't
// weaken (or falsely fail) the repo↔credential binding.
func sameRepo(a, b string) bool {
	na, oka := normalizeRepo(a)
	nb, okb := normalizeRepo(b)
	return oka && okb && na == nb
}

func normalizeRepo(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", false
	}
	path := strings.TrimSuffix(strings.TrimRight(u.Path, "/"), ".git")
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + path, true
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

// splitCellPath splits "/shop/info/refs" into ("shop", "info/refs").
func splitCellPath(p string) (cell, rest string) {
	p = strings.TrimPrefix(p, "/")
	i := strings.IndexByte(p, '/')
	if i < 0 {
		return p, ""
	}
	return p[:i], p[i+1:]
}
