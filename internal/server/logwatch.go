package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/client"
	"github.com/neotoxicfr/dockerr/internal/auth"
	"github.com/neotoxicfr/dockerr/internal/docker"
	"github.com/neotoxicfr/dockerr/internal/notify"
	"github.com/neotoxicfr/dockerr/internal/store"
)

// maxWatched bounds the containers followed for log alerts.
const maxWatched = 100

// alertLineLimit: alerts look at long lines further than the display,
// within bounds: with the 8-line buffer, about 1 MiB per watched container.
const alertLineLimit = 128 << 10

// restartDelay: a stream that ended is not started again sooner, so a
// failing container or API is not retried in a loop.
const restartDelay = 30 * time.Second

// matcher is a compiled log alert.
type matcher struct {
	id    int64
	stack string
	text  string         // lower-cased, when not a regexp
	re    *regexp.Regexp // /…/ patterns
}

// compileAlert accepts text (matched in any case) or /regexp/.
func compileAlert(a store.LogAlert) (matcher, error) {
	m := matcher{id: a.ID, stack: a.Stack}
	p := strings.TrimSpace(a.Pattern)
	if p == "" {
		return m, errors.New("motif vide")
	}
	if len(p) > 2 && strings.HasPrefix(p, "/") && strings.HasSuffix(p, "/") {
		re, err := regexp.Compile(p[1 : len(p)-1])
		if err != nil {
			return m, fmt.Errorf("expression régulière invalide : %w", err)
		}
		m.re = re
		return m, nil
	}
	m.text = strings.ToLower(p)
	return m, nil
}

func (m matcher) match(line string) bool {
	if m.re != nil {
		return m.re.MatchString(line)
	}
	return strings.Contains(strings.ToLower(line), m.text)
}

// logWatch follows the running containers the alerts cover, from now on,
// and notifies on a matching line. It reconciles every 30 seconds: new
// rules, containers started or gone.
func (s *Server) logWatch(ctx context.Context) {
	// watched: container ID → its stream. A stream that ends (the
	// container stopped) reports on ended, so a restart is followed again;
	// the pointer tells an old stream from its replacement.
	type stream struct{ cancel context.CancelFunc }
	watched := map[string]*stream{}
	endedAt := map[string]time.Time{} // when a stream last ended
	ended := make(chan [2]any, 16)
	defer func() {
		for _, w := range watched {
			w.cancel()
		}
	}()
	var rules []matcher
	var sig string // the rules streams were started with
	reconcile := func() {
		list, err := s.store.LogAlerts(ctx)
		if err != nil {
			return
		}
		rules = rules[:0]
		var b strings.Builder
		for _, a := range list {
			if m, err := compileAlert(a); err == nil {
				rules = append(rules, m)
				fmt.Fprintf(&b, "%d|%s|%s\n", a.ID, a.Stack, a.Pattern)
			}
		}
		if b.String() != sig {
			// Rules changed: restart every stream with the new ones.
			sig = b.String()
			for id, w := range watched {
				w.cancel()
				delete(watched, id)
			}
		}
		all, err := s.stacks.Containers(ctx)
		if err != nil {
			return
		}
		want := map[string]bool{}
		for _, c := range all {
			if c.State != "running" || len(want) >= maxWatched {
				continue
			}
			for _, r := range rules {
				if r.stack == "" || r.stack == c.Project {
					want[c.ID] = true
					break
				}
			}
		}
		for id, w := range watched {
			if !want[id] {
				w.cancel()
				delete(watched, id)
			}
		}
		for _, c := range all {
			if !want[c.ID] || watched[c.ID] != nil || time.Since(endedAt[c.ID]) < restartDelay {
				continue
			}
			cctx, cancel := context.WithCancel(ctx)
			w := &stream{cancel}
			watched[c.ID] = w
			rs := append([]matcher(nil), rules...)
			go func(id, name, project string) {
				s.watchOne(cctx, id, name, project, rs)
				select {
				case ended <- [2]any{id, w}:
				case <-ctx.Done():
				}
			}(c.ID, c.Name, c.Project)
		}
	}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		reconcile()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.alertsNudge:
		case e := <-ended:
			if id := e[0].(string); watched[id] == e[1].(*stream) {
				watched[id].cancel()
				delete(watched, id)
				endedAt[id] = time.Now()
			}
			for id, t := range endedAt {
				if time.Since(t) > restartDelay {
					delete(endedAt, id)
				}
			}
		}
	}
}

// watchOne follows one container's new lines with the rules it was
// started with; logWatch restarts it when they change.
func (s *Server) watchOne(ctx context.Context, id, name, stack string, rules []matcher) {
	lines := make(chan docker.LogLine, 8)
	go func() {
		defer close(lines)
		opts := client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: true, Tail: "0",
			Since: fmt.Sprint(time.Now().Unix())}
		if in, err := s.docker.API.ContainerInspect(ctx, id, client.ContainerInspectOptions{}); err == nil {
			tty := in.Container.Config != nil && in.Container.Config.Tty
			if err := docker.StreamLogs(ctx, s.docker.API, id, 0, tty, alertLineLimit, opts, lines); err != nil && ctx.Err() == nil {
				slog.Warn("log alerts: stream", "container", name, "err", err)
			}
		}
	}()
	for l := range lines {
		for _, r := range rules {
			if (r.stack == "" || r.stack == stack) && r.match(l.M) {
				msg := l.M
				if len(msg) > 300 {
					msg = msg[:300] + "…"
				}
				s.notifier.Notify(notify.Event{Kind: notify.LogAlert, Stack: stack, Container: name, Priority: 4,
					Title: "Alerte logs : " + name, Message: msg})
				break
			}
		}
	}
}

type alertBody struct {
	Pattern string `json:"pattern"`
	Stack   string `json:"stack"`
}

func (s *Server) alertAdd(w http.ResponseWriter, r *http.Request) {
	var b alertBody
	if !decode(w, r, &b) {
		return
	}
	a := store.LogAlert{Pattern: strings.TrimSpace(b.Pattern), Stack: b.Stack, By: auth.User(r.Context())}
	if len(a.Pattern) > 500 {
		jsonError(w, http.StatusBadRequest, "motif trop long")
		return
	}
	if _, err := compileAlert(a); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.AddLogAlert(r.Context(), a); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.nudgeAlerts()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) alertDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "alerte inconnue")
		return
	}
	if err := s.store.DeleteLogAlert(r.Context(), id); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.nudgeAlerts()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) nudgeAlerts() {
	select {
	case s.alertsNudge <- struct{}{}:
	default:
	}
}
