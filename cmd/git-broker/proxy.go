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
)

// handleGit serves one git smart-HTTP request at /<cell>/<rest>.
func (s *server) handleGit(w http.ResponseWriter, r *http.Request) {
	cell, rest := splitCellPath(r.URL.Path)
	if cell == "" {
		http.Error(w, "usage: /<cell>/<git-path>", http.StatusBadRequest)
		return
	}

	// 1. Authenticate the workload by its ServiceAccount token and bind it
	//    to the Cell: a pod in cell-foo may only act as cell "foo".
	ns, err := s.authenticate(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Basic realm=agentcell")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if ns != ids.WorkloadNamespace(cell) {
		http.Error(w, fmt.Sprintf("namespace %q may not act as cell %q", ns, cell), http.StatusForbidden)
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
	cred, err := s.creds.credentials(s.ctx(), c.Spec.Repo.URL, secret.Data)
	if err != nil {
		http.Error(w, "credential error", http.StatusInternalServerError)
		return
	}

	// 3. v2 action-level boundary: gate pushes to session/* only.
	if s.enforceRef && strings.HasSuffix(rest, "git-receive-pack") && r.Method == http.MethodPost {
		gz := strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip")
		newBody, perr := enforcePushPolicy(r.Body, gz)
		if perr != nil {
			http.Error(w, "push rejected: "+perr.Error(), http.StatusForbidden)
			return
		}
		r.Body = io.NopCloser(newBody) // byte-identical to the original
	}

	// 4. Proxy to the real remote with the real credential injected. The
	//    workload's SA token (its basic-auth password) is replaced, never
	//    forwarded onward.
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

// authenticate validates the request's basic-auth password as a Kubernetes
// ServiceAccount token and returns the pod's namespace.
func (s *server) authenticate(r *http.Request) (string, error) {
	_, token, ok := r.BasicAuth()
	if !ok || token == "" {
		return "", fmt.Errorf("no bearer token")
	}
	tr := &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: token}}
	res, err := s.auth.AuthenticationV1().TokenReviews().Create(s.ctx(), tr, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	if !res.Status.Authenticated {
		return "", fmt.Errorf("token not authenticated")
	}
	// Username is "system:serviceaccount:<namespace>:<sa>".
	parts := strings.Split(res.Status.User.Username, ":")
	if len(parts) != 4 || parts[0] != "system" || parts[1] != "serviceaccount" {
		return "", fmt.Errorf("not a service account token: %q", res.Status.User.Username)
	}
	return parts[2], nil
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
