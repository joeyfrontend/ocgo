// Package scopeguard enforces a bug-bounty programme's asset scope at the
// network layer.
//
// It is an HTTP forward proxy that will only dial hosts named in a scope file.
// A run configured to reach the network through it cannot touch anything else,
// and every decision, allowed or refused, is logged with the host and the
// reason. This is deliberately a hard fail rather than an advisory check: an
// agent that is merely *asked* not to leave scope will eventually leave it.
//
// Matching is exact. A wildcard entry must be written explicitly as
// "*.example.com" in the scope file; nothing is ever inferred from a parent
// hostname, because that is how scope silently grows.
package scopeguard

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Scope is the set of destinations a run may reach.
type Scope struct {
	// Program is the programme slug, recorded in the log for provenance.
	Program string `json:"program"`
	// Hosts are the in-scope hostnames. "*.example.com" matches one or more
	// subdomain levels and must be written out; nothing is inferred.
	Hosts []string `json:"hosts"`
	// LocalExempt lists "host:port" endpoints that are always reachable, used
	// for the local model bridge. It is deliberately separate from the
	// programme's hosts: pointing an agent at a bridge is not scope, and
	// conflating the two would quietly widen the programme's surface.
	LocalExempt []string `json:"local_exempt"`
	// Ports restricts which ports may be dialled. Empty means 443 and 80.
	Ports []int `json:"ports"`
	// Deny removes hosts that a wildcard would otherwise cover.
	Deny []string `json:"deny"`
}

// Load reads a scope file.
func Load(path string) (*Scope, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("scope file: %w", err)
	}
	var s Scope
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("scope file %s: %w", path, err)
	}
	if len(s.Hosts) == 0 {
		return nil, fmt.Errorf("scope file %s lists no hosts", path)
	}
	return &s, nil
}

// DefaultPorts are allowed when the scope does not name any.
var DefaultPorts = []int{443, 80}

// Allows reports whether a host and port may be dialled, and why not.
func (s *Scope) Allows(host string, port int) (bool, string) {
	if s == nil {
		return false, "no scope loaded"
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return false, "empty host"
	}
	if s.exempt(host, port) {
		return true, ""
	}
	for _, denied := range s.Deny {
		if matchHost(denied, host) {
			return false, "explicitly denied by scope"
		}
	}
	allowed := false
	for _, entry := range s.Hosts {
		if matchHost(entry, host) {
			allowed = true
			break
		}
	}
	if !allowed {
		return false, "not listed in scope"
	}
	ports := s.Ports
	if len(ports) == 0 {
		ports = DefaultPorts
	}
	for _, p := range ports {
		if p == port {
			return true, ""
		}
	}
	return false, fmt.Sprintf("port %d not listed in scope", port)
}

// matchHost compares one scope entry against a host. Exact entries match
// exactly; a leading "*." matches one or more label(s) of subdomain.
func matchHost(entry, host string) bool {
	entry = strings.ToLower(strings.TrimSpace(entry))
	if entry == "" {
		return false
	}
	if strings.HasPrefix(entry, "*.") {
		suffix := entry[1:] // ".example.com"
		return strings.HasSuffix(host, suffix) && len(host) > len(suffix)
	}
	return entry == host
}

// exempt reports whether host:port is a declared local exemption.
func (s *Scope) exempt(host string, port int) bool {
	want := net.JoinHostPort(host, fmt.Sprint(port))
	for _, entry := range s.LocalExempt {
		if strings.EqualFold(strings.TrimSpace(entry), want) {
			return true
		}
	}
	return false
}

// LocalExemptions returns the parsed local exemptions, for logging.
func (s *Scope) LocalExemptions() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.LocalExempt...)
}

// Decision is one recorded verdict.
type Decision struct {
	TS      time.Time `json:"ts"`
	Host    string    `json:"host"`
	Port    int       `json:"port"`
	Method  string    `json:"method"`
	Allowed bool      `json:"allowed"`
	Reason  string    `json:"reason,omitempty"`
}

// Guard is the proxy.
type Guard struct {
	Scope *Scope
	// OnDecision is called for every request, allowed or refused.
	OnDecision func(Decision)
	// DialTimeout bounds the connection to an allowed host.
	DialTimeout time.Duration

	mu       sync.Mutex
	refusals int
	allowed  int
}

// ServeHTTP implements an HTTP forward proxy, including CONNECT tunnelling.
func (g *Guard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, port := destination(r)
	ok, reason := g.Scope.Allows(host, port)
	g.record(Decision{TS: time.Now().UTC(), Host: host, Port: port, Method: r.Method, Allowed: ok, Reason: reason})
	if !ok {
		http.Error(w, fmt.Sprintf("ocgo scope guard: %s is out of scope (%s)", host, reason), http.StatusForbidden)
		return
	}

	if r.Method == http.MethodConnect {
		g.tunnel(w, r, host, port)
		return
	}
	g.forward(w, r)
}

func (g *Guard) tunnel(w http.ResponseWriter, r *http.Request, host string, port int) {
	target := net.JoinHostPort(host, fmt.Sprint(port))
	upstream, err := net.DialTimeout("tcp", target, g.dialTimeout())
	if err != nil {
		http.Error(w, "ocgo scope guard: dial failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "ocgo scope guard: connection cannot be hijacked", http.StatusInternalServerError)
		return
	}
	client, _, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	go func() {
		_, _ = io.Copy(upstream, client)
		_ = upstream.Close()
	}()
	_, _ = io.Copy(client, upstream)
	_ = client.Close()
}

func (g *Guard) forward(w http.ResponseWriter, r *http.Request) {
	out := r.Clone(r.Context())
	out.RequestURI = ""
	// A forward proxy must not pass its own hop-by-hop headers upstream.
	out.Header.Del("Proxy-Connection")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(out)
	if err != nil {
		http.Error(w, "ocgo scope guard: upstream request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// destination extracts the host and port a request is aimed at.
func destination(r *http.Request) (string, int) {
	if r.Method == http.MethodConnect {
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil {
			return r.Host, 443
		}
		return host, atoiOr(port, 443)
	}
	if r.URL != nil && r.URL.Host != "" {
		host := r.URL.Hostname()
		port := r.URL.Port()
		if port == "" {
			if r.URL.Scheme == "https" {
				return host, 443
			}
			return host, 80
		}
		return host, atoiOr(port, 80)
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		return r.Host, 80
	}
	return host, atoiOr(port, 80)
}

func atoiOr(s string, fallback int) int {
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return fallback
		}
		n = n*10 + int(ch-'0')
	}
	if s == "" {
		return fallback
	}
	return n
}

func (g *Guard) dialTimeout() time.Duration {
	if g.DialTimeout <= 0 {
		return 15 * time.Second
	}
	return g.DialTimeout
}

func (g *Guard) record(d Decision) {
	g.mu.Lock()
	if d.Allowed {
		g.allowed++
	} else {
		g.refusals++
	}
	g.mu.Unlock()
	if g.OnDecision != nil {
		g.OnDecision(d)
	}
}

// Counts returns the number of allowed and refused requests so far.
func (g *Guard) Counts() (allowed, refused int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.allowed, g.refusals
}

// JSONLLogger returns an OnDecision callback appending decisions to a JSONL
// file at 0600, plus a Close function.
func JSONLLogger(path string) (func(Decision), func() error, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	var mu sync.Mutex
	log := func(d Decision) {
		b, err := json.Marshal(d)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		_, _ = f.Write(append(b, '\n'))
	}
	return log, f.Close, nil
}
