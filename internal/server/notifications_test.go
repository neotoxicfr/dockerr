package server

import (
	"testing"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/neotoxicfr/dockerr/internal/notify"
)

func TestDeaths(t *testing.T) {
	msg := func(id string, a events.Action, attrs map[string]string) events.Message {
		if attrs == nil {
			attrs = map[string]string{}
		}
		attrs["name"] = "web-1"
		attrs["com.docker.compose.project"] = "web"
		return events.Message{Type: events.ContainerEventType, Action: a, Actor: events.Actor{ID: id, Attributes: attrs}}
	}
	now := time.Now()
	cases := []struct {
		name string
		pre  []events.Message // seen just before
		m    events.Message
		at   time.Time
		want string // event kind, "" = no notification
	}{
		{"crash", nil, msg("a", events.ActionDie, map[string]string{"exitCode": "1"}), now, notify.Died},
		{"clean exit", nil, msg("a", events.ActionDie, map[string]string{"exitCode": "0"}), now, ""},
		{"stopped on purpose", []events.Message{msg("a", events.ActionKill, nil)}, msg("a", events.ActionDie, map[string]string{"exitCode": "137"}), now, ""},
		{"stop long ago, then crash", []events.Message{msg("a", events.ActionStop, nil)}, msg("a", events.ActionDie, map[string]string{"exitCode": "2"}), now.Add(time.Minute), notify.Died},
		{"crash right after a restart", []events.Message{msg("a", events.ActionKill, nil), msg("a", events.ActionDie, map[string]string{"exitCode": "137"}), msg("a", events.ActionStop, nil), msg("a", events.ActionStart, nil)}, msg("a", events.ActionDie, map[string]string{"exitCode": "1"}), now, notify.Died},
		{"another container stopped", []events.Message{msg("b", events.ActionKill, nil)}, msg("a", events.ActionDie, map[string]string{"exitCode": "1"}), now, notify.Died},
		{"oom", nil, msg("a", events.ActionOOM, nil), now, notify.OOM},
		{"unhealthy", nil, msg("a", events.ActionHealthStatusUnhealthy, nil), now, notify.Unhealthy},
		{"healthy", nil, msg("a", events.ActionHealthStatusHealthy, nil), now, ""},
		{"start", nil, msg("a", events.ActionStart, nil), now, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &deaths{stopped: map[string]time.Time{}}
			for _, p := range c.pre {
				d.event(p, now)
			}
			e, ok := d.event(c.m, c.at)
			if got := map[bool]string{true: e.Kind, false: ""}[ok]; got != c.want {
				t.Fatalf("kind = %q, want %q", got, c.want)
			}
			if ok && (e.Stack != "web" || e.Container != "web-1" || e.Title == "") {
				t.Errorf("event = %+v", e)
			}
		})
	}
}
