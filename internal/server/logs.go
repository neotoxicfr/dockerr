package server

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/client"
	"github.com/neotoxicfr/dockerr/internal/docker"
	"github.com/neotoxicfr/dockerr/internal/stacks"
)

// maxLogContainers bounds one logs view (a stack's containers).
const maxLogContainers = 30

type logsData struct {
	Title, Back, BackLabel string
	Stack                  string
	Containers             []*stacks.Container
}

// stackLogs shows the logs of every container of a stack, merged.
func (s *Server) stackLogs(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.loadStack(w, r)
	if !ok {
		return
	}
	d := logsData{Title: st.Name, Back: "/s/" + st.Name, BackLabel: st.Name, Stack: st.Name}
	for _, svc := range st.Services {
		d.Containers = append(d.Containers, svc.Containers...)
	}
	s.render(w, r, "logs", "Logs · "+st.Name, "stacks", d)
}

// containerLogs shows one container's logs.
func (s *Server) containerLogs(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.findContainer(w, r)
	if !ok {
		return
	}
	d := logsData{Title: c.Name, Containers: []*stacks.Container{c}, Stack: c.Project}
	if c.Project != "" {
		d.Back, d.BackLabel = "/s/"+c.Project, c.Project
	} else {
		d.Back, d.BackLabel = "/resources", "Ressources"
	}
	s.render(w, r, "logs", "Logs · "+c.Name, "stacks", d)
}

// logStream sends the logs of the containers in ?c= as server-sent
// events: "lines" batches, "end" when a container's stream stops.
// ?tail=N (default 300, "all"), ?since=seconds ago, ?until=seconds ago
// (a past window, not followed).
func (s *Server) logStream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ids := q["c"]
	if len(ids) == 0 || len(ids) > maxLogContainers {
		http.Error(w, "conteneurs manquants ou trop nombreux", http.StatusBadRequest)
		return
	}
	opts := client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "300"}
	if t := q.Get("tail"); t == "all" {
		opts.Tail = "all"
	} else if n, err := strconv.Atoi(t); err == nil && n > 0 && n <= 100000 {
		opts.Tail = strconv.Itoa(n)
	}
	now := time.Now()
	ago := func(k string) string {
		n, err := strconv.Atoi(q.Get(k))
		if err != nil || n <= 0 {
			return ""
		}
		return strconv.FormatInt(now.Add(-time.Duration(n)*time.Second).Unix(), 10)
	}
	opts.Since, opts.Until = ago("since"), ago("until")
	if opts.Since != "" && q.Get("tail") == "" {
		opts.Tail = "all" // a time window shows all of it
	}

	ctx := r.Context()
	all, _, err := s.snapshot(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	var list []*stacks.Container
	for _, id := range ids {
		i := slices.IndexFunc(all, func(c *stacks.Container) bool { return c.ID == id })
		if i < 0 {
			http.Error(w, "conteneur introuvable", http.StatusNotFound)
			return
		}
		list = append(list, all[i])
	}
	send, ok := sse(w)
	if !ok {
		return
	}
	lines := make(chan docker.LogLine, 512)
	ends := make(chan int, len(list))
	var wg sync.WaitGroup
	for i, c := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { ends <- i }()
			o := opts
			tty := false
			if in, err := s.docker.API.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{}); err == nil {
				tty = in.Container.Config != nil && in.Container.Config.Tty
				o.Follow = in.Container.State != nil && in.Container.State.Running && o.Until == ""
			}
			if err := docker.StreamLogs(ctx, s.docker.API, c.ID, i, tty, 0, o, lines); err != nil && ctx.Err() == nil {
				select { // the client may be gone: never block here
				case lines <- docker.LogLine{C: i, T: time.Now(), Err: true, M: "dockerr : " + err.Error()}:
				case <-ctx.Done():
				}
			}
		}()
	}
	go func() { wg.Wait(); close(lines) }()

	batch := make([]docker.LogLine, 0, 256)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		ok := send("lines", batch) == nil
		batch = batch[:0]
		return ok
	}
	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case l, more := <-lines:
			if !more {
				flush()
				_ = send("done", true)
				return
			}
			batch = append(batch, l)
			if len(batch) >= 500 && !flush() {
				return
			}
		case i := <-ends:
			if !flush() || send("end", i) != nil {
				return
			}
		case <-tick.C:
			if !flush() {
				return
			}
		case <-keepAlive.C:
			if send("ping", true) != nil {
				return
			}
		}
	}
}

// logName is a container's short name in a stack's logs: its service.
func logName(c *stacks.Container) string {
	if c.Service != "" {
		return c.Service
	}
	return strings.TrimPrefix(c.Name, "/")
}
