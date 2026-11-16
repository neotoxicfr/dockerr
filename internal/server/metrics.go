package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neotoxicfr/dockerr/internal/stacks"
	"github.com/neotoxicfr/dockerr/internal/store"
)

// metricsData is what /metrics exposes, gathered once per collection.
type metricsData struct {
	all  []*stacks.Container
	list []*stacks.Stack
	last time.Time
	ops  []store.OpCount
}

// promEscape escapes a label value for the Prometheus text format.
func promEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

// promWriter writes metric families: HELP and TYPE once, samples sorted so
// the output is stable between scrapes.
type promWriter struct {
	b       strings.Builder
	samples []string
}

func (p *promWriter) family(name, typ, help string) {
	p.flush()
	fmt.Fprintf(&p.b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

// sample adds name{k="v",…} value; labels are key, value pairs.
func (p *promWriter) sample(name string, value any, labels ...string) {
	var l []string
	for i := 0; i+1 < len(labels); i += 2 {
		l = append(l, labels[i]+`="`+promEscape(labels[i+1])+`"`)
	}
	s := name
	if len(l) > 0 {
		s += "{" + strings.Join(l, ",") + "}"
	}
	p.samples = append(p.samples, fmt.Sprintf("%s %v", s, value))
}

func (p *promWriter) flush() {
	sort.Strings(p.samples)
	for _, s := range p.samples {
		p.b.WriteString(s + "\n")
	}
	p.samples = nil
}

// renderMetrics writes the Prometheus text exposition (version 0.0.4).
func renderMetrics(d metricsData) string {
	var p promWriter
	p.family("dockerr_up", "gauge", "1 while dockerr answers.")
	p.sample("dockerr_up", 1)
	p.family("dockerr_info", "gauge", "dockerr version.")
	p.sample("dockerr_info", 1, "version", Version)

	p.family("dockerr_stacks", "gauge", "Stacks by state.")
	states := []stacks.State{stacks.Running, stacks.Partial, stacks.Paused, stacks.Stopped, stacks.Problem}
	n := map[stacks.State]int{}
	updates := 0
	for _, s := range d.list {
		n[s.State]++
		updates += s.Updates
	}
	for _, st := range states { // fixed order: stable output
		p.sample("dockerr_stacks", n[st], "state", string(st))
	}

	p.family("dockerr_containers", "gauge", "Containers by state.")
	byState := map[string]int{"running": 0, "paused": 0, "exited": 0}
	lost := 0
	for _, c := range d.all {
		byState[c.State]++
		if c.NetLost {
			lost++
		}
	}
	for st, c := range byState {
		p.sample("dockerr_containers", c, "state", st)
	}

	p.family("dockerr_container_cpu_percent", "gauge", "CPU use of a running container, percent of one CPU.")
	for _, c := range d.all {
		if c.Running() {
			p.sample("dockerr_container_cpu_percent", fmt.Sprintf("%.2f", c.CPU), "stack", c.Project, "service", c.Service, "container", c.Name)
		}
	}
	p.family("dockerr_container_memory_bytes", "gauge", "Memory use of a running container, page cache excluded.")
	for _, c := range d.all {
		if c.Running() {
			p.sample("dockerr_container_memory_bytes", c.Mem, "stack", c.Project, "service", c.Service, "container", c.Name)
		}
	}

	p.family("dockerr_updates_available", "gauge", "Services whose image has a newer version.")
	p.sample("dockerr_updates_available", updates)
	p.family("dockerr_update_last_check_timestamp_seconds", "gauge", "Unix time of the last image update check, 0 if none yet.")
	var ts int64
	if !d.last.IsZero() {
		ts = d.last.Unix()
	}
	p.sample("dockerr_update_last_check_timestamp_seconds", ts)
	p.family("dockerr_netdeps_lost", "gauge", "Containers that lost the network namespace they share.")
	p.sample("dockerr_netdeps_lost", lost)

	p.family("dockerr_operations_kept", "gauge", "Operations kept in the database (90-day retention), by kind and result; not a counter.")
	for _, o := range d.ops {
		res := "ok"
		if o.Failed {
			res = "error"
		}
		p.sample("dockerr_operations_kept", o.N, "kind", o.Kind, "result", res)
	}
	p.flush()
	return p.b.String()
}

var metricsCache struct {
	sync.Mutex
	at   time.Time
	body string
}

// metrics serves the Prometheus exposition, cached 15 s like /api/status.
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	if !apiKeyOK(r, s.cfg.APIKeysFile, "metrics") {
		w.Header().Set("WWW-Authenticate", `Bearer realm="dockerr"`)
		jsonError(w, http.StatusUnauthorized, "clé d'API absente ou invalide")
		return
	}
	metricsCache.Lock()
	defer metricsCache.Unlock()
	if time.Since(metricsCache.at) > 15*time.Second {
		all, list, err := s.snapshot(r.Context())
		if err != nil {
			jsonError(w, http.StatusBadGateway, "Docker injoignable")
			return
		}
		last, _ := s.updates.LastRun()
		ops, _ := s.store.OpCounts(r.Context())
		metricsCache.body, metricsCache.at = renderMetrics(metricsData{all, list, last, ops}), time.Now()
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(metricsCache.body))
}
