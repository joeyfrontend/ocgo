// Package session resolves the stable per-conversation session id that
// OpenCode Go expects in the x-opencode-session header.
//
// Go returns 400 MissingSessionID when the header is absent, and uses the id to
// pin prompt-cache routing, so the value must not change mid-conversation.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
)

// Candidate header names that carry a client's native session id, in
// resolution order. Names are written in canonical MIME form because
// http.Header.Get only canonicalises "-" separated names: an underscore name
// such as "session_id" is returned unchanged and therefore never matches the
// stored "Session-Id" key. Measured on Codex 0.162.1, which sends the same
// value in Session-Id, Thread-Id, X-Client-Request-Id and X-Codex-Window-Id.
var nativeHeaders = []string{
	"Session-Id",
	"X-Session-Id",
	"X-Session-Affinity",
	"X-Conversation-Id",
	"X-Opencode-Session",
	"OpenAI-Conversation-Id",
	"Thread-Id",
	"X-Thread-Id",
}

// Resolver derives and caches one session id per conversation.
type Resolver struct {
	mu     sync.Mutex
	byConv map[string]string
}

// NewResolver returns an empty Resolver.
func NewResolver() *Resolver {
	return &Resolver{byConv: map[string]string{}}
}

// NativeSessionID returns the first non-empty native session header, and the
// header name it came from.
func NativeSessionID(h http.Header) (string, string) {
	if h == nil {
		return "", ""
	}
	for _, name := range nativeHeaders {
		if v := strings.TrimSpace(h.Get(name)); v != "" {
			return v, name
		}
	}
	return "", ""
}

// Derive returns the session id for a conversation, in resolution order:
//
//  1. the client's native session header
//  2. prompt_cache_key from the request body
//  3. SHA-256 of the first user message, truncated to 32 hex characters
//
// The result is cached per convKey so a conversation keeps one id even if a
// later turn stops sending the header.
func (r *Resolver) Derive(convKey, nativeID, promptCacheKey, firstUserMessage string) string {
	if r == nil {
		return FirstNonEmpty(nativeID, promptCacheKey, hashText(firstUserMessage))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.byConv[convKey]; ok && id != "" {
		return id
	}
	id := FirstNonEmpty(nativeID, promptCacheKey, hashText(firstUserMessage))
	if id == "" {
		return ""
	}
	r.byConv[convKey] = id
	return id
}

// Peek returns a previously derived id without deriving a new one.
func (r *Resolver) Peek(convKey string) string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byConv[convKey]
}

// Forget drops a cached conversation, used when a conversation key is reused
// across a fresh client session.
func (r *Resolver) Forget(convKey string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byConv, convKey)
}

// FirstNonEmpty returns the first value that is non-empty after trimming.
func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func hashText(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:32]
}
