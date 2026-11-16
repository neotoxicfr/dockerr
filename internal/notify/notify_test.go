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
	if err := n.Send(context.Background(), Event{Kind: Died, Title: "web s'est arrêté", Host: "pi"}); err != nil {
		t.Fatal(err)
	}
	if h := hits(); len(h) != 4 || h[2].body["title"] != "pi · web s'est arrêté" || h[3].body["host"] != "pi" {
		t.Errorf("host not named: %+v", h[2:])
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
		{"", "ftp://example.com/x", "URL http(s) attendue"},
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

func TestNoSecretInErrorsOrTargets(t *testing.T) {
	// Each channel, with the secret in the user info, the path or the query.
	cases := []struct{ name, ntfy, hook string }{
		{"ntfy topic", "http://127.0.0.1:1/s3cret", ""},
		{"ntfy user info", "http://u:s3cret@127.0.0.1:1/topic", ""},
		{"webhook path", "", "http://127.0.0.1:1/hooks/s3cret"},
		{"webhook query", "", "http://127.0.0.1:1/hook?token=s3cret"},
		{"webhook user info", "", "http://u:s3cret@127.0.0.1:1/hook"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, err := New(c.ntfy, "", c.hook)
			if err != nil {
				t.Fatal(err)
			}
			err = n.Send(context.Background(), Event{Kind: Test})
			if err == nil || strings.Contains(err.Error(), "s3cret") {
				t.Errorf("send error = %v", err)
			}
			for _, s := range n.Targets() {
				if strings.Contains(s, "s3cret") {
					t.Errorf("target = %s", s)
				}
			}
		})
	}
	if _, err := New("ftp://s3cret@example.com/x", "", ""); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("config error = %v", err)
	}
}

func TestFailedSendIsNotSilenced(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer s.Close()
	n, _ := New("", "", s.URL)
	e := Event{Kind: Died, Stack: "a", Container: "a-1"}
	count := func() int { mu.Lock(); defer mu.Unlock(); return hits }
	n.Notify(e)
	for i := 0; count() < 1 && i < 100; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // the failure clears the key
	n.Notify(e)
	for i := 0; count() < 2 && i < 100; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if got := count(); got != 2 {
		t.Fatalf("attempts = %d, want 2: a failed send must not silence the next one", got)
	}
}
