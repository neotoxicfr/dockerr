package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/neotoxicfr/dockerr/internal/auth"
	"github.com/neotoxicfr/dockerr/internal/notify"
	"github.com/neotoxicfr/dockerr/internal/ops"
	"github.com/neotoxicfr/dockerr/internal/stacks"
)

// autoLabel opts a service in to automatic updates when set to "true".
const autoLabel = "dockerr.autoupdate"

// autoUser is the operations' user for updates nobody asked for.
const autoUser = "auto"

// autoTarget is what one automatic update runs on: a whole stack, or one
// service (Service != "").
type autoTarget struct{ Stack, Service string }

// nextWake returns the next occurrence of hour:minute strictly after now.
func nextWake(now time.Time, hour, minute int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !t.After(now) {
		t = time.Date(now.Year(), now.Month(), now.Day()+1, hour, minute, 0, 0, now.Location())
	}
	return t
}

// labeled reports a service with the opt-in label on one of its containers.
func labeled(svc *stacks.Service) bool {
	return slices.ContainsFunc(svc.Containers, func(c *stacks.Container) bool { return c.Labels[autoLabel] == "true" })
}

// pickTargets chooses what to update: an opted-in stack as a whole when one
// of its services has a newer image, otherwise the labelled services that
// have one. dockerr's own stack is never touched.
func pickTargets(list []*stacks.Stack, optIn map[string]bool) []autoTarget {
	var out []autoTarget
	for _, st := range list {
		if st.Self || st.File == "" {
			continue
		}
		if optIn[st.Name] {
			if slices.ContainsFunc(st.Services, func(s *stacks.Service) bool { return s.Update }) {
				out = append(out, autoTarget{Stack: st.Name})
			}
			continue
		}
		for _, svc := range st.Services {
			if svc.Update && labeled(svc) {
				out = append(out, autoTarget{st.Name, svc.Name})
			}
		}
	}
	return out
}

// autoUpdateLoop runs autoUpdate every day at the configured time.
func (s *Server) autoUpdateLoop(ctx context.Context) {
	for {
		wake := nextWake(time.Now(), s.cfg.AutoUpdateHour, s.cfg.AutoUpdateMinute)
		t := time.NewTimer(time.Until(wake))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		s.autoUpdate(ctx)
	}
}

// autoUpdate checks the images, updates what is opted in and has a newer
// digest, and sends one summary. Failures already notify through the
// operation itself.
func (s *Server) autoUpdate(ctx context.Context) {
	if !s.docker.Caps.Compose() {
		slog.Warn("auto update skipped: the Docker proxy does not allow compose")
		return
	}
	// A check already running (started by hand) would make this one return
	// at once with stale results: wait for it first.
	if !s.updates.CheckWhenIdle(ctx, s.imageRefs(ctx)) {
		return
	}
	_, list, err := s.snapshot(ctx)
	if err != nil {
		slog.Warn("auto update: stacks unreadable", "err", err)
		return
	}
	names, err := s.store.AutoUpdates(ctx, s.storeHost())
	if err != nil {
		slog.Warn("auto update: opt-ins unreadable", "err", err)
	}
	optIn := map[string]bool{}
	for _, n := range names {
		optIn[n] = true
	}
	var done []string
	failed := 0
	for _, t := range pickTargets(list, optIn) {
		var st *stacks.Stack
		for _, x := range list {
			if x.Name == t.Stack {
				st = x
			}
		}
		if ctx.Err() != nil {
			return // shutting down: no new operation, no summary
		}
		op, err := s.runner.Run(st, ops.Update, t.Service, autoUser)
		if err != nil {
			slog.Info("auto update skipped", "stack", t.Stack, "service", t.Service, "err", err)
			continue
		}
		snap := s.waitOp(ctx, op)
		if !snap.Done {
			return // the wait was cut short: its outcome is unknown
		}
		name := t.Stack
		if t.Service != "" {
			name += " · " + t.Service
		}
		if snap.Err != "" {
			failed++
			continue
		}
		done = append(done, name)
	}
	if len(done) == 0 {
		return
	}
	title := "Mise à jour automatique : " + done[0]
	if len(done) > 1 {
		title = fmt.Sprintf("Mise à jour automatique : %d piles", len(done))
	}
	msg := strings.Join(done, "\n")
	if failed > 0 {
		msg += fmt.Sprintf("\n%d échec(s), notifiés à part.", failed)
	}
	s.notify(notify.Event{Kind: notify.AutoUpdate, Title: title, Message: msg, Priority: 3, Container: strings.Join(done, " ")})
}

// waitOp blocks until the operation ends (or ctx does).
func (s *Server) waitOp(ctx context.Context, op *ops.Op) ops.Snapshot {
	ch, cancel := op.Watch()
	defer cancel()
	for {
		if snap := op.Snapshot(); snap.Done {
			return snap
		}
		select {
		case <-ctx.Done():
			return op.Snapshot()
		case <-ch:
		case <-time.After(5 * time.Second):
		}
	}
}

// stackAutoUpdate turns a stack's opt-in on or off.
func (s *Server) stackAutoUpdate(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.loadStack(w, r)
	if !ok {
		return
	}
	if st.Self {
		jsonError(w, http.StatusConflict, "cette pile fait tourner dockerr : elle ne peut pas se mettre à jour seule")
		return
	}
	if err := s.store.SetAutoUpdate(r.Context(), s.storeHost(), st.Name, r.FormValue("on") == "1", auth.User(r.Context())); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
