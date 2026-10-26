package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	var seen string
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = nonce(r) }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	got := w.Header()
	if len(seen) < 20 {
		t.Fatalf("nonce %q", seen)
	}
	strict := csp(seen)
	for name, want := range map[string]string{
		"Content-Security-Policy": strict,
		"Permissions-Policy":      "camera=(), microphone=(), geolocation=()",
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
	} {
		if got.Get(name) != want {
			t.Errorf("%s = %q, want %q", name, got.Get(name), want)
		}
	}
	if strings.Contains(strict, "unsafe-inline") || !strings.Contains(strict, "object-src 'none'") || !strings.HasPrefix(strict, "default-src 'self';") {
		t.Errorf("csp not strict: %q", strict)
	}
	first := seen
	// A fresh nonce per response.
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/", nil))
	if w2.Header().Get("Content-Security-Policy") == strict {
		t.Error("nonce reused across responses")
	}
	// The terminal variant only relaxes style-src.
	if strings.Replace(cspTerminal, "'unsafe-inline'", "'nonce-"+first+"'", 1) != strict {
		t.Errorf("cspTerminal differs beyond style-src: %q", cspTerminal)
	}
}
