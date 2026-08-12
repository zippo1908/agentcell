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

// identity is the verified caller: namespace and ServiceAccount from the
// TokenReview, plus the bound-token pod name (unforgeable) when available.
type identity struct {
	namespace string
	saName    string
	podName   string
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
	// Unforgeable repo↔credential binding: if the secret declares the repo
	// it is authorized for, the Cell's URL must match it. A Cell creator
	// cannot pair a credential with a different (e.g. attacker) URL.
	if bound := strings.TrimSpace(string(secret.Data["repo_url"])); bound != "" && bound != c.Spec.Repo.URL {
		http.Error(w, "cell repo url is not authorized by this credential", http.StatusForbidden)
		return
	}
	cred, err := s.creds.credentials(s.ctx(), c.Spec.Repo.URL, secret.Data)
	if err != nil {
		http.Error(w, "credential error", http.StatusInternalServerError)
		return
	}

	// 3. v2/v1.1 action-level boundary: a push must be from the settle role
	//    and may only update that session's own branch.
	if push && r.Method == http.MethodPost {
		sessionID, err := settleSessionID(id)
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
	// The token must be scoped to the broker, not the apiserver at large.
	if len(res.Status.Audiences) > 0 && !contains(res.Status.Audiences, runtimeapi.BrokerAudience) {
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
		// push permitted; the exact branch is bound in settleSessionID.
	default:
		return fmt.Errorf("service account %q is not permitted", id.saName)
	}
	return nil
}

// settleSessionID derives the session id from the settle pod's own name
// (from the unforgeable bound-token claim), so a push can be pinned to
// exactly that session's branch. Job "settle-<id>" → pod "settle-<id>-<rand>".
func settleSessionID(id identity) (string, error) {
	if id.saName != runtimeapi.SASettle {
		return "", fmt.Errorf("only the settle role may push")
	}
	if id.podName == "" {
		return "", fmt.Errorf("bound pod identity unavailable (need projected token)")
	}
	rest, ok := strings.CutPrefix(id.podName, "settle-")
	if !ok {
		return "", fmt.Errorf("unexpected settle pod name %q", id.podName)
	}
	i := strings.LastIndex(rest, "-")
	if i <= 0 {
		return "", fmt.Errorf("cannot derive session id from %q", id.podName)
	}
	return rest[:i], nil
}

func isReceivePack(r *http.Request, rest string) bool {
	if strings.HasSuffix(rest, "git-receive-pack") {
		return true
	}
	// info/refs advertisement for push.
	return rest == "info/refs" && r.URL.Query().Get("service") == "git-receive-pack"
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
