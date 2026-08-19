// Command egress-proxy is the one way out of a cell namespace.
//
// It answers two questions that the platform previously could not answer at
// all: may this agent reach that destination, and who was it. See
// internal/egress for why the second one is the valuable half.
//
// It is a CONNECT proxy and does NOT terminate TLS. The destination name
// comes from the CONNECT request line, which is enough to decide policy and
// to write an audit line; the bytes stay encrypted end to end. Intercepting
// would mean distributing a CA into every sandbox and would let the platform
// read everything an agent sends — a far larger decision, and not one this
// needs.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/internal/egress"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
)

func main() {
	var (
		addr       = flag.String("addr", ":3128", "listen address")
		policyPath = flag.String("policy", "/etc/agentcell/egress.json", "allowlist file (re-read while running)")
		controlNS  = flag.String("control-namespace", "agentcell-system", "namespace holding Session objects")
		observe    = flag.Bool("observe", true, "allow unlisted destinations, but record them")
	)
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	p := &proxy{log: log, defaultObserve: *observe, policyPath: *policyPath}
	p.load() // first read: a bad file must fail loudly at start, not at 3am
	go p.watch()

	if r, err := newResolver(*controlNS); err != nil {
		// Attribution is not worth refusing to start over: without it the
		// proxy still enforces policy, and every line says the principal is
		// unknown rather than pretending.
		log.Warn("running without attribution — every line will be unattributed", "err", err)
	} else {
		p.resolver = r
	}

	log.Info("egress proxy listening", "addr", *addr, "observe", *observe, "policy", *policyPath)
	srv := &http.Server{Addr: *addr, Handler: p}
	if err := srv.ListenAndServe(); err != nil {
		log.Error("egress proxy stopped", "err", err)
		os.Exit(1)
	}
}

func newResolver(controlNS string) (*egress.Resolver, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := acv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}
	return &egress.Resolver{Client: c, ControlNS: controlNS, TTL: 30 * time.Second}, nil
}

type proxy struct {
	log            *slog.Logger
	policyPath     string
	defaultObserve bool
	policy         atomic.Pointer[egress.Policy]
	resolver       *egress.Resolver
}

// policyFile is the on-disk shape, kept separate from egress.Policy so the
// file can gain fields without the decision logic knowing about them.
type policyFile struct {
	// Observe overrides the flag when present, so the mode lives next to
	// the list it applies to and flipping it is one edit in one place.
	Observe *bool         `json:"observe,omitempty"`
	Allow   []egress.Rule `json:"allow"`
}

func (p *proxy) load() {
	pol := egress.Policy{Observe: p.defaultObserve}
	b, err := os.ReadFile(p.policyPath)
	if err != nil {
		// No file means nothing is allowed by name. In observe mode that is
		// harmless and everything gets recorded; enforcing, it is a closed
		// door, which is the correct direction for a missing policy.
		p.log.Warn("no egress policy file; nothing is allowed by name", "path", p.policyPath, "err", err)
		p.policy.Store(&pol)
		return
	}
	var f policyFile
	if err := json.Unmarshal(b, &f); err != nil {
		// Keep whatever is already loaded. Replacing a working policy with
		// an empty one because somebody mistyped a comma is how an edit
		// takes a platform down.
		p.log.Error("egress policy file is not valid JSON; keeping the previous one", "err", err)
		if p.policy.Load() == nil {
			p.policy.Store(&pol)
		}
		return
	}
	pol.Rules = f.Allow
	if f.Observe != nil {
		pol.Observe = *f.Observe
	}
	p.policy.Store(&pol)
	p.log.Info("egress policy loaded", "rules", len(pol.Rules), "observe", pol.Observe)
}

// watch re-reads the file so the list can be edited while agents are
// running — which is the whole point of discovering it in observe mode.
func (p *proxy) watch() {
	var last string
	for range time.Tick(10 * time.Second) {
		b, err := os.ReadFile(p.policyPath)
		if err != nil {
			continue
		}
		if s := string(b); s != last {
			last = s
			p.load()
		}
	}
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	// Plain HTTP. Same policy, same audit line — package mirrors that still
	// serve over HTTP are a real thing, and refusing them outright during
	// observation would break the very work we are trying to watch.
	p.plain(w, r)
}

func (p *proxy) decide(r *http.Request, host string, port int) (egress.Verdict, egress.Attribution) {
	pol := p.policy.Load()
	v := pol.Check(host, port)
	var attr egress.Attribution
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if p.resolver != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		attr = p.resolver.Attribute(ctx, ip)
	} else {
		attr = egress.Attribution{IP: ip}
	}
	return v, attr
}

// record writes the audit line. One line per attempt, allowed or not.
func (p *proxy) record(v egress.Verdict, a egress.Attribution, host string, port int, bytesUp, bytesDown int64) {
	p.log.Info("egress",
		"allow", v.Allow,
		"observed", v.Observed,
		"rule", v.Rule,
		"reason", v.Reason,
		"host", host,
		"port", port,
		"principal", a.PrincipalID,
		"cell", a.Cell,
		"session", a.Session,
		"pod", a.Pod,
		"ip", a.IP,
		"bytes_up", bytesUp,
		"bytes_down", bytesDown,
	)
}

func (p *proxy) connect(w http.ResponseWriter, r *http.Request) {
	host, portS, err := net.SplitHostPort(r.Host)
	if err != nil {
		host, portS = r.Host, "443"
	}
	port, _ := strconv.Atoi(portS)
	if port == 0 {
		port = 443
	}

	v, attr := p.decide(r, host, port)
	if !v.Allow {
		p.record(v, attr, host, port, 0, 0)
		http.Error(w, "egress refused: "+v.Reason, http.StatusForbidden)
		return
	}

	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(host, portS), 10*time.Second)
	if err != nil {
		p.record(egress.Verdict{Allow: false, Rule: v.Rule, Reason: "拨号失败: " + err.Error()}, attr, host, port, 0, 0)
		http.Error(w, "egress: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "egress: connection cannot be hijacked", http.StatusInternalServerError)
		return
	}
	down, _, err := hj.Hijack()
	if err != nil {
		return
	}
	defer down.Close()
	if _, err := down.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		return
	}

	up, downBytes := splice(down, upstream)
	p.record(v, attr, host, port, up, downBytes)
}

// splice copies in both directions and reports how much moved.
//
// The byte counts are the only quantitative signal an audit line carries
// without decrypting anything: "this session sent 300MB to an allowed host"
// is a question worth asking even when the host is on the list.
func splice(down net.Conn, up net.Conn) (int64, int64) {
	var sent, received int64
	done := make(chan struct{})
	go func() {
		received, _ = io.Copy(down, up)
		if c, ok := down.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
		close(done)
	}()
	sent, _ = io.Copy(up, down)
	if c, ok := up.(interface{ CloseWrite() error }); ok {
		_ = c.CloseWrite()
	}
	<-done
	return sent, received
}

func (p *proxy) plain(w http.ResponseWriter, r *http.Request) {
	if !r.URL.IsAbs() {
		http.Error(w, "egress: this is a proxy; use an absolute URL or CONNECT", http.StatusBadRequest)
		return
	}
	host := r.URL.Hostname()
	port := 80
	if s := r.URL.Port(); s != "" {
		port, _ = strconv.Atoi(s)
	}
	v, attr := p.decide(r, host, port)
	if !v.Allow {
		p.record(v, attr, host, port, 0, 0)
		http.Error(w, "egress refused: "+v.Reason, http.StatusForbidden)
		return
	}

	out := r.Clone(r.Context())
	out.RequestURI = ""
	resp, err := http.DefaultTransport.RoundTrip(out)
	if err != nil {
		p.record(egress.Verdict{Rule: v.Rule, Reason: "上游失败: " + err.Error()}, attr, host, port, 0, 0)
		http.Error(w, "egress: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, s := range vs {
			w.Header().Add(k, s)
		}
	}
	w.WriteHeader(resp.StatusCode)
	n, _ := io.Copy(w, resp.Body)
	p.record(v, attr, host, port, 0, n)
}
