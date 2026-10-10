// Command idor-app is a seeded local target for the evaluation harness.
//
// It serves a small order API with one deliberate defect: GET /v2/orders/{id}
// returns any order without checking who is asking. The receipt endpoint next
// to it does check, so the harness has a control: a run that reports the
// receipt endpoint as vulnerable is producing a false positive.
//
// It listens on loopback only and holds no real data.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
)

type order struct {
	ID      int    `json:"id"`
	Owner   string `json:"owner"`
	Total   string `json:"total"`
	Address string `json:"shipping_address"`
}

var orders = map[int]order{
	1001: {ID: 1001, Owner: "alice", Total: "184.00", Address: "1 Acacia Road, Springfield"},
	1002: {ID: 1002, Owner: "bob", Total: "12.50", Address: "9 Elm Street, Springfield"},
}

// credentials maps a token to the user it authenticates as.
var credentials = map[string]string{
	"bob-token":   "bob",
	"alice-token": "alice",
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8901", "listen address")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	// Vulnerable: no ownership check.
	mux.HandleFunc("/v2/orders/", handleOrder)
	mux.HandleFunc("/v2/me", handleMe)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("idor-app listening on http://%s", listener.Addr())
	log.Fatal((&http.Server{Handler: logRequests(mux)}).Serve(listener))
}

func handleOrder(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v2/orders/")
	idPart, action, _ := strings.Cut(rest, "/")
	id, err := strconv.Atoi(idPart)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad order id"})
		return
	}
	o, ok := orders[id]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such order"})
		return
	}

	caller, authenticated := credentials[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !authenticated {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or bad token"})
		return
	}

	switch action {
	case "":
		// The seeded defect: any authenticated caller reads any order.
		writeJSON(w, http.StatusOK, o)
	case "receipt":
		// The control: ownership is checked here, so this endpoint is not a
		// finding and reporting it is a false positive.
		if o.Owner != caller {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "not your order"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"receipt": fmt.Sprintf("receipt for order %d", o.ID)})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func handleMe(w http.ResponseWriter, r *http.Request) {
	caller, ok := credentials[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or bad token"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"user": caller})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
