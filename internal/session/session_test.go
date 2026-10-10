package session

import (
	"net/http"
	"testing"
)

func TestNativeSessionIDUsesCanonicalHeaderNames(t *testing.T) {
	// Codex 0.162.1 sends Session-Id. An underscore name must not be relied on:
	// http.Header.Get leaves such a key uncanonicalised, so it never matches the
	// stored "Session-Id" key. This is the bug the first live capture exposed.
	h := http.Header{}
	h.Set("Session-Id", "abc-123")
	got, name := NativeSessionID(h)
	if got != "abc-123" {
		t.Fatalf("NativeSessionID = %q, want abc-123", got)
	}
	if name != "Session-Id" {
		t.Fatalf("header name = %q, want Session-Id", name)
	}

	underscore := http.Header{"session_id": {"abc-123"}}
	if got, _ := NativeSessionID(underscore); got != "" {
		t.Fatalf("underscore key should not resolve, got %q", got)
	}
}

func TestNativeSessionIDFallsThroughToThreadID(t *testing.T) {
	h := http.Header{}
	h.Set("Thread-Id", "thread-9")
	if got, name := NativeSessionID(h); got != "thread-9" || name != "Thread-Id" {
		t.Fatalf("got %q via %q", got, name)
	}
}

func TestNativeSessionIDEmpty(t *testing.T) {
	if got, name := NativeSessionID(http.Header{}); got != "" || name != "" {
		t.Fatalf("empty header should resolve to nothing, got %q via %q", got, name)
	}
	if got, _ := NativeSessionID(nil); got != "" {
		t.Fatalf("nil header should resolve to nothing, got %q", got)
	}
}

func TestDeriveResolutionOrder(t *testing.T) {
	t.Run("native header wins", func(t *testing.T) {
		r := NewResolver()
		if got := r.Derive("c1", "native", "cache", "message"); got != "native" {
			t.Fatalf("got %q, want native", got)
		}
	})
	t.Run("prompt_cache_key second", func(t *testing.T) {
		r := NewResolver()
		if got := r.Derive("c1", "", "cache", "message"); got != "cache" {
			t.Fatalf("got %q, want cache", got)
		}
	})
	t.Run("first user message hashed last", func(t *testing.T) {
		r := NewResolver()
		got := r.Derive("c1", "", "", "hello")
		if got == "" || len(got) != 32 {
			t.Fatalf("hash fallback = %q, want 32 hex chars", got)
		}
	})
	t.Run("nothing available", func(t *testing.T) {
		r := NewResolver()
		if got := r.Derive("c1", "", "", ""); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
}

func TestDeriveIsStableAcrossTurns(t *testing.T) {
	r := NewResolver()
	first := r.Derive("conv", "native-id", "", "first message")
	// A later turn omits the header and changes the message; the cached value
	// must not change, or prompt-cache routing is lost mid-conversation.
	second := r.Derive("conv", "", "different-cache-key", "second message")
	if first != second {
		t.Fatalf("session id changed mid-conversation: %q then %q", first, second)
	}
	if first != "native-id" {
		t.Fatalf("first resolution = %q, want native-id", first)
	}
}

func TestDeriveSeparatesConversations(t *testing.T) {
	r := NewResolver()
	a := r.Derive("a", "id-a", "", "")
	b := r.Derive("b", "id-b", "", "")
	if a == b {
		t.Fatalf("distinct conversations share session id %q", a)
	}
}

func TestDeriveFallsBackWhenConvKeyIsEmpty(t *testing.T) {
	// Without a conv key the resolver must still cache under something, or two
	// different conversations could collide on the empty key.
	r := NewResolver()
	first := r.Derive("", "", "", "message one")
	if first == "" {
		t.Fatal("expected a derived id")
	}
	if peeked := r.Peek(""); peeked != first {
		t.Fatalf("Peek(\"\") = %q, want %q", peeked, first)
	}
}

func TestPeekAndForget(t *testing.T) {
	r := NewResolver()
	if r.Peek("c") != "" {
		t.Fatal("Peek on an unknown conversation should be empty")
	}
	r.Derive("c", "id", "", "")
	if r.Peek("c") != "id" {
		t.Fatalf("Peek = %q, want id", r.Peek("c"))
	}
	r.Forget("c")
	if r.Peek("c") != "" {
		t.Fatal("Forget should drop the cached id")
	}
}

func TestNilResolverIsUsable(t *testing.T) {
	var r *Resolver
	if got := r.Derive("c", "native", "", ""); got != "native" {
		t.Fatalf("nil resolver Derive = %q, want native", got)
	}
	if got := r.Peek("c"); got != "" {
		t.Fatalf("nil resolver Peek = %q, want empty", got)
	}
	r.Forget("c") // must not panic
}

func TestFirstNonEmpty(t *testing.T) {
	if got := FirstNonEmpty("", "  ", "value", "other"); got != "value" {
		t.Fatalf("got %q, want value", got)
	}
	if got := FirstNonEmpty("", " "); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
