package scopeguard

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func scope(hosts ...string) *Scope {
	return &Scope{Program: "test", Hosts: hosts}
}

func TestAllowsExactHost(t *testing.T) {
	s := scope("api.example.com")
	if ok, reason := s.Allows("api.example.com", 443); !ok {
		t.Fatalf("exact host refused: %s", reason)
	}
	if ok, _ := s.Allows("api.example.com", 80); !ok {
		t.Fatal("port 80 should be allowed by default")
	}
}

func TestNeverInfersScopeFromAParentHost(t *testing.T) {
	s := scope("example.com")
	if ok, _ := s.Allows("sub.example.com", 443); ok {
		t.Fatal("a parent host must not imply its subdomains")
	}
	if ok, _ := s.Allows("example.com.evil.test", 443); ok {
		t.Fatal("a suffix match must not be confused for the host itself")
	}
}

func TestWildcardMustBeExplicit(t *testing.T) {
	s := scope("*.example.com")
	if ok, reason := s.Allows("api.example.com", 443); !ok {
		t.Fatalf("wildcard should cover a subdomain: %s", reason)
	}
	if ok, _ := s.Allows("deep.api.example.com", 443); !ok {
		t.Fatal("wildcard should cover nested subdomains")
	}
	if ok, _ := s.Allows("example.com", 443); ok {
		t.Fatal("wildcard must not cover the apex")
	}
	if ok, _ := s.Allows("notexample.com", 443); ok {
		t.Fatal("wildcard must not match a different registrable domain")
	}
}

func TestDenyBeatsAllow(t *testing.T) {
	s := scope("*.example.com")
	s.Deny = []string{"admin.example.com"}
	if ok, reason := s.Allows("admin.example.com", 443); ok {
		t.Fatal("denied host was allowed")
	} else if !strings.Contains(reason, "denied") {
		t.Fatalf("reason = %q", reason)
	}
	if ok, _ := s.Allows("api.example.com", 443); !ok {
		t.Fatal("deny list should not affect other hosts")
	}
}

func TestPortMustBeListed(t *testing.T) {
	s := scope("api.example.com")
	s.Ports = []int{443}
	if ok, _ := s.Allows("api.example.com", 443); !ok {
		t.Fatal("listed port refused")
	}
	if ok, reason := s.Allows("api.example.com", 8080); ok {
		t.Fatal("unlisted port allowed")
	} else if !strings.Contains(reason, "8080") {
		t.Fatalf("reason should name the port, got %q", reason)
	}
}

func TestHostNormalisation(t *testing.T) {
	s := scope("API.Example.com")
	for _, host := range []string{"api.example.com", "API.EXAMPLE.COM", "api.example.com."} {
		if ok, reason := s.Allows(host, 443); !ok {
			t.Fatalf("%q refused: %s", host, reason)
		}
	}
}

func TestNilAndEmptyScopeRefuse(t *testing.T) {
	var s *Scope
	if ok, reason := s.Allows("example.com", 443); ok || reason == "" {
		t.Fatalf("nil scope must refuse with a reason, got %v %q", ok, reason)
	}
	if ok, _ := scope().Allows("example.com", 443); ok {
		t.Fatal("an empty host list must refuse")
	}
}

func TestLoadRejectsEmptyScopeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scope.json")
	if err := os.WriteFile(path, []byte(`{"program":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a scope file with no hosts must be rejected")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("a missing scope file must be rejected")
	}
}

func TestGuardRefusesOutOfScopeHost(t *testing.T) {
	var decisions []Decision
	guard := &Guard{Scope: scope("in-scope.test"), OnDecision: func(d Decision) { decisions = append(decisions, d) }}

	req := httptest.NewRequest(http.MethodGet, "http://out-of-scope.test/secret", nil)
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "out of scope") {
		t.Fatalf("body should explain the refusal: %s", rec.Body.String())
	}
	if len(decisions) != 1 || decisions[0].Allowed || decisions[0].Host != "out-of-scope.test" {
		t.Fatalf("refusal was not logged: %+v", decisions)
	}
}

func TestLocalExemptionAllowsAnUnlistedEndpoint(t *testing.T) {
	s := scope("api.example.com")
	s.Ports = []int{443}
	s.LocalExempt = []string{"127.0.0.1:3456"}
	if ok, reason := s.Allows("127.0.0.1", 3456); !ok {
		t.Fatalf("local exemption refused: %s", reason)
	}
	// The exemption must not widen programme scope.
	if ok, _ := s.Allows("127.0.0.1", 3457); ok {
		t.Fatal("only the exact exempt endpoint should be reachable")
	}
	if ok, _ := s.Allows("api.example.com", 8080); ok {
		t.Fatal("the exemption must not relax the port list")
	}
}

func TestLocalExemptionIsCaseInsensitive(t *testing.T) {
	s := scope("api.example.com")
	s.LocalExempt = []string{"127.0.0.1:3456"}
	if ok, _ := s.Allows("127.0.0.1", 3456); !ok {
		t.Fatal("exemption should match")
	}
}

func TestGuardProxiesInScopeHost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "proxied:"+r.URL.Path)
	}))
	defer upstream.Close()
	host, port := hostPort(t, upstream.URL)

	guard := &Guard{Scope: &Scope{Program: "t", Hosts: []string{host}, Ports: []int{port}}}
	proxy := httptest.NewServer(guard)
	defer proxy.Close()

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustURL(t, proxy.URL))}}
	resp, err := client.Get("http://" + net.JoinHostPort(host, fmt.Sprint(port)) + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "proxied:/ok" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
}

func TestGuardRefusesOutOfScopeConnect(t *testing.T) {
	var decisions []Decision
	guard := &Guard{Scope: scope("in-scope.test"), OnDecision: func(d Decision) { decisions = append(decisions, d) }}
	proxy := httptest.NewServer(guard)
	defer proxy.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(proxy.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT out-of-scope.test:443 HTTP/1.1\r\nHost: out-of-scope.test:443\r\n\r\n")
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "403") {
		t.Fatalf("CONNECT response = %q, want 403", line)
	}
	if len(decisions) != 1 || decisions[0].Allowed || decisions[0].Method != http.MethodConnect {
		t.Fatalf("CONNECT refusal not logged: %+v", decisions)
	}
}

func TestGuardTunnelsInScopeConnect(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	host, port := hostPort(t, "http://"+echo.Addr().String())

	guard := &Guard{Scope: &Scope{Program: "t", Hosts: []string{host}, Ports: []int{port}}}
	proxy := httptest.NewServer(guard)
	defer proxy.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(proxy.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "CONNECT %s:%d HTTP/1.1\r\nHost: %s:%d\r\n\r\n", host, port, host, port)
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "200") {
		t.Fatalf("CONNECT response = %q, want 200", line)
	}
	// Consume the blank line that ends the CONNECT response headers.
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if got != "ping\n" {
		t.Fatalf("tunnel echoed %q", got)
	}
}

func TestJSONLLoggerWritesDecisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	logFn, closeFn, err := JSONLLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	logFn(Decision{Host: "a.test", Port: 443, Allowed: true})
	logFn(Decision{Host: "b.test", Port: 443, Allowed: false, Reason: "not listed in scope"})
	if err := closeFn(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d log lines", len(lines))
	}
	var d Decision
	if err := json.Unmarshal([]byte(lines[1]), &d); err != nil {
		t.Fatal(err)
	}
	if d.Allowed || d.Host != "b.test" || d.Reason == "" {
		t.Fatalf("second decision = %+v", d)
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("log mode = %o, want 600", perm)
	}
}

func TestCounts(t *testing.T) {
	guard := &Guard{Scope: scope("a.test")}
	guard.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://b.test/", nil))
	guard.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://c.test/", nil))
	allowed, refused := guard.Counts()
	if allowed != 0 || refused != 2 {
		t.Fatalf("counts = %d allowed, %d refused", allowed, refused)
	}
}

func hostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port := 0
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatal(err)
	}
	return host, port
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
