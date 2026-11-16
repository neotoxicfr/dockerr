package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/events"
	"github.com/neotoxicfr/dockerr/internal/auth"
	"github.com/neotoxicfr/dockerr/internal/notify"
	"github.com/neotoxicfr/dockerr/internal/updates"
)

// stopWindow: a container that dies this soon after a stop or kill was
// meant to stop (an operation, docker stop, a restart).
const stopWindow = 30 * time.Second

// deaths tells a crash from a requested stop.
type deaths struct {
	mu      sync.Mutex
	stopped map[string]time.Time
}

// event turns a container event into a notification, or nothing.
func (d *deaths) event(m events.Message, now time.Time) (notify.Event, bool) {
	a := m.Actor.Attributes
	name, stack := a["name"], a[api.ProjectLabel]
	e := notify.Event{Stack: stack, Container: name, Priority: 4}
	switch m.Action {
	case events.ActionKill, events.ActionStop:
		d.mu.Lock()
		d.stopped[m.Actor.ID] = now
		for id, t := range d.stopped {
			if now.Sub(t) > stopWindow {
				delete(d.stopped, id)
			}
		}
		d.mu.Unlock()
		return e, false
	case events.ActionDie:
		d.mu.Lock()
		t, asked := d.stopped[m.Actor.ID]
		d.mu.Unlock()
		code := a["exitCode"]
		if code == "0" || (asked && now.Sub(t) < stopWindow) {
			return e, false
		}
		e.Kind, e.Title = notify.Died, name+" s'est arrêté"
		e.Message = fmt.Sprintf("%s s'est arrêté avec le code %s.", name, code)
	case events.ActionOOM:
		e.Kind, e.Title = notify.OOM, name+" : mémoire épuisée"
		e.Message = name + " a dépassé sa mémoire et a été tué."
		e.Priority = 5
	case events.ActionHealthStatusUnhealthy:
		e.Kind, e.Title = notify.Unhealthy, name+" en mauvaise santé"
		e.Message = name + " échoue à son test de santé."
	default:
		return e, false
	}
	if stack != "" {
		e.Message += " Pile : " + stack + "."
	}
	return e, true
}

// notifyUpdates reports images found newer, with the services using them.
func (s *Server) notifyUpdates(rs []updates.Result) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	all, err := s.stacks.Containers(ctx)
	if err != nil {
		return
	}
	var lines []string
	for _, r := range rs {
		var users []string
		for _, c := range all {
			if c.Image == r.Ref {
				users = append(users, c.Name)
			}
		}
		slices.Sort(users)
		lines = append(lines, fmt.Sprintf("%s (%s)", r.Ref, strings.Join(slices.Compact(users), ", ")))
	}
	slices.Sort(lines)
	title := "Nouvelle image disponible"
	if len(lines) > 1 {
		title = fmt.Sprintf("%d nouvelles images disponibles", len(lines))
	}
	s.notifier.Notify(notify.Event{Kind: notify.Update, Title: title, Message: strings.Join(lines, "\n"), Priority: 3})
}

// notifyTest sends a test notification and reports the result.
func (s *Server) notifyTest(w http.ResponseWriter, r *http.Request) {
	if !s.notifier.Enabled() {
		jsonError(w, http.StatusConflict, "aucune notification configurée")
		return
	}
	err := s.notifier.Send(r.Context(), notify.Event{Kind: notify.Test, Title: "dockerr", Priority: 3,
		Message: "Notification de test, envoyée par " + auth.User(r.Context()) + "."})
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
