package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type hit struct {
	path, auth string
	body       map[string]any
}

func server(t *testing.T) (*httptest.Server, func() []hit) {
	var mu sync.Mutex
	var hits []hit
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		hits = append(hits, hit{r.URL.Path, r.Header.Get("Authorization"), b})
		mu.Unlock()
	}))
	t.Cleanup(s.Close)
	return s, func() []hit { mu.Lock(); defer mu.Unlock(); return append([]hit(nil), hits...) }
}

func TestSend(t *testing.T) {
	s, hits := server(t)
	n, err := New(s.URL+"/alerts", "tk_test", s.URL+"/hook")
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Send(context.Background(), Event{Kind: Died, Title: "web s'est arrêté", Message: "code 1", Stack: "web"}); err != nil {
		t.Fatal(err)
	}
	h := hits()
	if len(h) != 2 {
		t.Fatalf("hits = %+v", h)
	}
	ntfy, hook := h[0], h[1]
	if ntfy.path != "/" || ntfy.body["topic"] != "alerts" || ntfy.body["title"] != "web s'est arrêté" || ntfy.auth != "Bearer tk_test" {
		t.Errorf("ntfy = %+v", ntfy)
	}
	if hook.path != "/hook" || hook.body["event"] != Died || hook.body["stack"] != "web" || hook.auth != "" {
		t.Errorf("webhook = %+v (no token to the webhook)", hook)
	}
}

func TestNotifyQuiet(t *testing.T) {
	s, hits := server(t)
	n, _ := New("", "", s.URL)
	e := Event{Kind: Unhealthy, Stack: "a", Container: "a-web-1"}
	n.Notify(e)
	n.Notify(e)                                  // same event: silent
	n.Notify(Event{Kind: Unhealthy, Stack: "b"}) // another one goes out
	deadline := time.Now().Add(3 * time.Second)
	for len(hits()) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if got := len(hits()); got != 2 {
		t.Fatalf("sent %d, want 2", got)
	}
}

func TestNew(t *testing.T) {
	cases := []struct {
		ntfy, hook, err string
	}{
		{"", "", ""},
		{"https://ntfy.example.com/dockerr", "", ""},
		{"https://ntfy.example.com/", "", "sujet"},
		{"https://ntfy.example.com/a/b", "", "sujet"},
		{"", "ftp://example.com/x", "invalide"},
		{"", "https://example.com/hook", ""},
	}
	for _, c := range cases {
		_, err := New(c.ntfy, "", c.hook)
		if (err == nil) != (c.err == "") || (err != nil && !strings.Contains(err.Error(), c.err)) {
			t.Errorf("New(%q, %q) = %v, want %q", c.ntfy, c.hook, err, c.err)
		}
	}
	var off *Notifier
	if off.Enabled() || len(off.Targets()) != 0 {
		t.Error("nil notifier must be off")
	}
}
