// Package notify sends dockerr's events to ntfy and to a generic JSON
// webhook.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Event kinds.
const (
	Died      = "container_died"
	OOM       = "container_oom"
	Unhealthy = "container_unhealthy"
	Update    = "update_available"
	OpFailed  = "operation_failed"
	Repaired  = "network_repaired"
	Test      = "test"
)

type Event struct {
	Kind      string    `json:"event"`
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	Stack     string    `json:"stack,omitempty"`
	Container string    `json:"container,omitempty"`
	Priority  int       `json:"priority"` // 1 (min) to 5 (urgent), ntfy's scale
	Time      time.Time `json:"time"`
}

// Quiet is how long the same event (kind, stack, container) stays silent
// after it was sent.
const Quiet = 10 * time.Minute

type Notifier struct {
	ntfyURL, ntfyToken, webhook string
	client                      *http.Client

	mu   sync.Mutex
	last map[string]time.Time
}

// New checks the URLs; an empty one turns that channel off.
func New(ntfyURL, ntfyToken, webhook string) (*Notifier, error) {
	for _, v := range [][2]string{{"DOCKERR_NTFY_URL", ntfyURL}, {"DOCKERR_WEBHOOK_URL", webhook}} {
		if v[1] == "" {
			continue
		}
		// The URL itself stays out of the message: it may hold a secret.
		p, err := url.Parse(v[1])
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			return nil, fmt.Errorf("%s : URL http(s) attendue", v[0])
		}
	}
	if ntfyURL != "" {
		if p, _ := url.Parse(ntfyURL); strings.Trim(p.Path, "/") == "" || strings.Contains(strings.Trim(p.Path, "/"), "/") {
			return nil, errors.New("DOCKERR_NTFY_URL doit viser un sujet : https://serveur/sujet")
		}
	}
	return &Notifier{ntfyURL: ntfyURL, ntfyToken: ntfyToken, webhook: webhook,
		client: &http.Client{Timeout: 10 * time.Second}, last: map[string]time.Time{}}, nil
}

func (n *Notifier) Enabled() bool { return n != nil && (n.ntfyURL != "" || n.webhook != "") }

// Targets names the channels in use, for the settings page: the server
// only. A public ntfy topic and a webhook path work as passwords.
func (n *Notifier) Targets() []string {
	var out []string
	if n == nil {
		return out
	}
	for _, t := range [][2]string{{"ntfy", n.ntfyURL}, {"webhook", n.webhook}} {
		if t[1] != "" {
			p, _ := url.Parse(t[1])
			out = append(out, t[0]+" · "+p.Scheme+"://"+p.Host)
		}
	}
	return out
}

// Notify sends e in the background, unless the same event went out less
// than Quiet ago.
func (n *Notifier) Notify(e Event) {
	if !n.Enabled() {
		return
	}
	key := e.Kind + "|" + e.Stack + "|" + e.Container
	n.mu.Lock()
	if t, ok := n.last[key]; ok && time.Since(t) < Quiet {
		n.mu.Unlock()
		return
	}
	n.last[key] = time.Now()
	for k, t := range n.last {
		if time.Since(t) > Quiet {
			delete(n.last, k)
		}
	}
	n.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := n.Send(ctx, e); err != nil {
			slog.Warn("notification not sent", "event", e.Kind, "err", err)
			// Not delivered: the next occurrence must not be silenced.
			n.mu.Lock()
			delete(n.last, key)
			n.mu.Unlock()
		}
	}()
}

// Send delivers e to every channel now, and reports the first failure.
func (n *Notifier) Send(ctx context.Context, e Event) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if e.Priority == 0 {
		e.Priority = 3
	}
	var errs []error
	if n.ntfyURL != "" {
		errs = append(errs, n.ntfy(ctx, e))
	}
	if n.webhook != "" {
		errs = append(errs, n.post(ctx, n.webhook, e, ""))
	}
	return errors.Join(errs...)
}

// ntfy publishes as JSON to the server root, so titles keep their accents.
func (n *Notifier) ntfy(ctx context.Context, e Event) error {
	p, _ := url.Parse(n.ntfyURL)
	topic := strings.Trim(p.Path, "/")
	p.Path = "/"
	msg := map[string]any{"topic": topic, "title": e.Title, "message": e.Message, "priority": e.Priority, "tags": []string{tag(e.Kind)}}
	return n.post(ctx, p.String(), msg, n.ntfyToken)
}

func (n *Notifier) post(ctx context.Context, u string, body any, token string) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "dockerr")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := n.client.Do(req)
	if err != nil {
		// The URL may carry a secret (a webhook token, an ntfy topic): name
		// the host only, in logs and on the page.
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("%s : %w", req.URL.Host, ue.Err)
		}
		return fmt.Errorf("%s : échec de l'envoi", req.URL.Host)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s : HTTP %d", req.URL.Host, res.StatusCode)
	}
	return nil
}

// tag is ntfy's emoji shortcode for an event.
func tag(kind string) string {
	switch kind {
	case Died, OpFailed:
		return "x"
	case OOM, Unhealthy:
		return "warning"
	case Update:
		return "arrow_up"
	case Repaired:
		return "wrench"
	}
	return "bell"
}
