// Command ocgo-guard runs the scope-enforcing egress proxy.
//
// Point a sandboxed agent at it with HTTP_PROXY / HTTPS_PROXY. Anything not
// listed in the scope file is refused with 403 and logged. Codex routes all of
// its traffic through the proxy when those variables are set, including its own
// update checks, so a run reaches nothing that is not either in scope or an
// explicitly declared local exemption.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"

	"ocgo/internal/scopeguard"
)

func main() {
	scopePath := flag.String("scope", "", "JSON scope file listing in-scope hosts")
	addr := flag.String("addr", "127.0.0.1:8888", "listen address")
	logPath := flag.String("log", "", "JSONL file for every decision")
	exempt := flag.String("exempt", "", "comma-separated host:port endpoints always allowed, for the local model bridge")
	quiet := flag.Bool("quiet", false, "log refusals only")
	flag.Parse()

	if strings.TrimSpace(*scopePath) == "" {
		fmt.Fprintln(os.Stderr, "ocgo-guard: -scope is required")
		flag.Usage()
		os.Exit(2)
	}
	scope, err := scopeguard.Load(*scopePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ocgo-guard:", err)
		os.Exit(1)
	}
	for _, entry := range strings.Split(*exempt, ",") {
		if e := strings.TrimSpace(entry); e != "" {
			scope.LocalExempt = append(scope.LocalExempt, e)
		}
	}

	guard := &scopeguard.Guard{Scope: scope}
	guard.OnDecision = func(d scopeguard.Decision) {
		if d.Allowed && *quiet {
			return
		}
		verdict := "ALLOW"
		if !d.Allowed {
			verdict = "DENY"
		}
		fmt.Printf("%s %s:%d %s %s\n", verdict, d.Host, d.Port, d.Method, d.Reason)
	}
	if *logPath != "" {
		logFn, closeFn, err := scopeguard.JSONLLogger(*logPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ocgo-guard:", err)
			os.Exit(1)
		}
		guard.OnDecision = chain(guard.OnDecision, logFn)
		defer func() { _ = closeFn() }()
	}

	hosts := append([]string(nil), scope.Hosts...)
	sort.Strings(hosts)
	log.Printf("scope: programme %q, %d host entries: %s", scope.Program, len(hosts), strings.Join(hosts, ", "))
	if len(scope.Deny) > 0 {
		log.Printf("denied: %s", strings.Join(scope.Deny, ", "))
	}
	if len(scope.LocalExempt) > 0 {
		log.Printf("local exemptions (not programme scope): %s", strings.Join(scope.LocalExempt, ", "))
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ocgo-guard:", err)
		os.Exit(1)
	}
	log.Printf("ocgo-guard listening on http://%s (set HTTP_PROXY and HTTPS_PROXY)", listener.Addr())
	if err := (&http.Server{Handler: guard}).Serve(listener); err != nil {
		fmt.Fprintln(os.Stderr, "ocgo-guard:", err)
		os.Exit(1)
	}
}

func chain(first, second func(scopeguard.Decision)) func(scopeguard.Decision) {
	return func(d scopeguard.Decision) {
		first(d)
		second(d)
	}
}
