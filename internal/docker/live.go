package docker

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"golang.org/x/sync/errgroup"
)

// Stat is a container's resource use, from two samples of the stats endpoint.
type Stat struct {
	CPU float64 // percent of one CPU
	Mem uint64  // bytes, page cache excluded
}

type sample struct {
	cpu, system uint64
	online      uint32
}

// Stats samples running containers while someone looks at them: sampling
// stops two minutes after the last read.
type Stats struct {
	api client.APIClient

	mu     sync.RWMutex
	stats  map[string]Stat
	prev   map[string]sample
	wanted time.Time
	wake   chan struct{}
}

func NewStats(api client.APIClient) *Stats {
	return &Stats{api: api, stats: map[string]Stat{}, prev: map[string]sample{}, wake: make(chan struct{}, 1)}
}

// Get returns the latest stats and keeps sampling alive.
func (s *Stats) Get() map[string]Stat {
	s.mu.Lock()
	idle := time.Since(s.wanted) > 2*time.Minute
	s.wanted = time.Now()
	out := make(map[string]Stat, len(s.stats))
	for k, v := range s.stats {
		out[k] = v
	}
	s.mu.Unlock()
	if idle {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return out
}

func (s *Stats) Loop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		s.mu.RLock()
		active := time.Since(s.wanted) < 2*time.Minute
		s.mu.RUnlock()
		if active {
			s.sample(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

func (s *Stats) sample(ctx context.Context) {
	list, err := s.api.ContainerList(ctx, client.ContainerListOptions{})
	if err != nil {
		return
	}
	var mu sync.Mutex
	next := map[string]Stat{}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for _, c := range list.Items {
		g.Go(func() error {
			res, err := s.api.ContainerStats(gctx, c.ID, client.ContainerStatsOptions{IncludePreviousSample: false})
			if err != nil {
				return nil
			}
			defer res.Body.Close()
			var r container.StatsResponse
			if json.NewDecoder(res.Body).Decode(&r) != nil {
				return nil
			}
			cur := sample{r.CPUStats.CPUUsage.TotalUsage, r.CPUStats.SystemUsage, r.CPUStats.OnlineCPUs}
			st := Stat{Mem: r.MemoryStats.Usage}
			if cache, ok := r.MemoryStats.Stats["inactive_file"]; ok && cache < st.Mem {
				st.Mem -= cache
			}
			mu.Lock()
			defer mu.Unlock()
			s.mu.RLock()
			p, ok := s.prev[c.ID]
			s.mu.RUnlock()
			if ok && cur.system > p.system && cur.cpu >= p.cpu {
				n := float64(cur.online)
				if n == 0 {
					n = 1
				}
				st.CPU = float64(cur.cpu-p.cpu) / float64(cur.system-p.system) * n * 100
			}
			next[c.ID] = st
			s.mu.Lock()
			s.prev[c.ID] = cur
			s.mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	s.mu.Lock()
	s.stats = next
	for id := range s.prev {
		if _, ok := next[id]; !ok {
			delete(s.prev, id)
		}
	}
	s.mu.Unlock()
}

// Events fans Docker events out to subscribers as a bare "something
// changed" signal; pages then reload what they show.
type Events struct {
	api client.APIClient

	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func NewEvents(api client.APIClient) *Events {
	return &Events{api: api, subs: map[chan struct{}]struct{}{}}
}

func (e *Events) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	e.mu.Lock()
	e.subs[ch] = struct{}{}
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		delete(e.subs, ch)
		e.mu.Unlock()
	}
}

// Notify signals every subscriber, coalescing bursts.
func (e *Events) Notify() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for ch := range e.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (e *Events) Loop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		f := make(client.Filters).Add("type", "container", "image", "network", "volume")
		res := e.api.Events(ctx, client.EventsListOptions{Filters: f})
		start := time.Now()
	read:
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-res.Messages:
				// exec_* and top events fire constantly and change nothing visible.
				if a := string(m.Action); len(a) >= 5 && a[:5] == "exec_" || a == "top" {
					continue
				}
				e.Notify()
			case err := <-res.Err:
				slog.Warn("docker events", "err", err)
				break read
			}
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}
