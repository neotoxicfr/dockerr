// Package stacks finds compose stacks (folders under the stacks directory and
// projects seen on running containers) and aggregates their live state.
package stacks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/neotoxicfr/dockerr/internal/docker"
)

// ComposeFiles are tried in this order in each stack folder, like compose does.
var ComposeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ValidName reports whether s is usable as a project and folder name.
func ValidName(s string) bool { return len(s) <= 64 && nameRe.MatchString(s) }

type State string

const (
	Running State = "running"
	Partial State = "partial"
	Stopped State = "stopped"
	Problem State = "problem"
)

type Stack struct {
	Name    string
	Dir     string
	File    string // compose file path, empty when unknown
	Managed bool   // folder under the stacks directory: editable
	LoadErr string

	Services  []*Service
	State     State
	Up, Total int

	CPU     float64
	Mem     uint64
	Updates int // services whose image has a newer digest
	// Self is set on the stack running dockerr: operations on it would
	// stop dockerr halfway.
	Self bool
}

type Service struct {
	Name        string
	Image       string
	NetworkMode string // as declared
	Containers  []*Container
	Declared    bool // present in the compose file
	// OneShot services (restart: "no") are expected to exit: an exit code of
	// 0 counts as up.
	OneShot bool
	Update  bool
}

type Container struct {
	ID, Name, Image, ImageID string
	Project, Service         string
	State, Status, Health    string
	ExitCode                 int
	Created                  time.Time
	Ports                    []Port
	NetworkMode              string
	Networks                 []string
	Volumes                  []string // named volumes mounted
	Labels                   map[string]string

	// NetParent names the container whose network namespace this one joins.
	NetParent string
	// NetLost is set when that namespace is gone (parent missing, stopped,
	// or restarted after this container started).
	NetLost bool

	CPU float64
	Mem uint64
}

type Port struct {
	IP              string
	Public, Private uint16
	Proto           string
}

func (c *Container) Running() bool { return c.State == string(container.StateRunning) }

type Manager struct {
	Docker *docker.Docker
	Dir    string

	mu    sync.Mutex
	cache map[string]cachedProject // compose file path → parsed project
}

type cachedProject struct {
	mod     time.Time
	envMod  time.Time
	project *types.Project
	err     error
}

func NewManager(d *docker.Docker, dir string) *Manager {
	return &Manager{Docker: d, Dir: dir, cache: map[string]cachedProject{}}
}

// Compose returns a compose service whose progress events go to ep.
func (m *Manager) Compose(ep api.EventProcessor) (api.Compose, error) {
	opts := []compose.Option{compose.WithPrompt(compose.AlwaysOkPrompt()), compose.WithMaxConcurrency(4)}
	if ep != nil {
		opts = append(opts, compose.WithEventProcessor(ep))
	}
	return compose.NewComposeService(m.Docker.CLI, opts...)
}

// Load parses a stack's compose file the way `docker compose` would, with
// its .env. Results are cached until the file or .env changes.
func (m *Manager) Load(ctx context.Context, name, file string) (*types.Project, error) {
	st, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	var envMod time.Time
	if es, err := os.Stat(filepath.Join(filepath.Dir(file), ".env")); err == nil {
		envMod = es.ModTime()
	}
	m.mu.Lock()
	c, ok := m.cache[file]
	m.mu.Unlock()
	if ok && c.mod.Equal(st.ModTime()) && c.envMod.Equal(envMod) {
		return c.project, c.err
	}
	svc, err := m.Compose(nil)
	if err != nil {
		return nil, err
	}
	p, err := svc.LoadProject(ctx, api.ProjectLoadOptions{
		ProjectName: name,
		ConfigPaths: []string{file},
		WorkingDir:  filepath.Dir(file),
	})
	m.mu.Lock()
	m.cache[file] = cachedProject{mod: st.ModTime(), envMod: envMod, project: p, err: err}
	m.mu.Unlock()
	return p, err
}

// FindFile returns the compose file in dir, or "".
func FindFile(dir string) string {
	for _, f := range ComposeFiles {
		p := filepath.Join(dir, f)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// Containers lists every container, compose-managed or not.
func (m *Manager) Containers(ctx context.Context) ([]*Container, error) {
	res, err := m.Docker.API.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, err
	}
	out := make([]*Container, 0, len(res.Items))
	for _, s := range res.Items {
		c := &Container{
			ID:          s.ID,
			Image:       s.Image,
			ImageID:     s.ImageID,
			Project:     s.Labels[api.ProjectLabel],
			Service:     s.Labels[api.ServiceLabel],
			State:       string(s.State),
			Status:      s.Status,
			Created:     time.Unix(s.Created, 0),
			NetworkMode: s.HostConfig.NetworkMode,
			Labels:      s.Labels,
		}
		if len(s.Names) > 0 {
			c.Name = strings.TrimPrefix(s.Names[0], "/")
		}
		if s.Health != nil {
			c.Health = string(s.Health.Status)
		}
		if c.State == string(container.StateExited) {
			_, _ = fmt.Sscanf(strings.TrimPrefix(c.Status, "Exited "), "(%d)", &c.ExitCode)
		}
		seen := map[string]bool{}
		for _, p := range s.Ports {
			if p.PublicPort == 0 {
				continue
			}
			k := fmt.Sprint(p.PublicPort, p.PrivatePort, p.Type)
			if seen[k] {
				continue // IPv4 and IPv6 bindings of the same port
			}
			seen[k] = true
			c.Ports = append(c.Ports, Port{IP: p.IP.String(), Public: p.PublicPort, Private: p.PrivatePort, Proto: p.Type})
		}
		sort.Slice(c.Ports, func(i, j int) bool { return c.Ports[i].Public < c.Ports[j].Public })
		if s.NetworkSettings != nil {
			for n := range s.NetworkSettings.Networks {
				c.Networks = append(c.Networks, n)
			}
			sort.Strings(c.Networks)
		}
		for _, m := range s.Mounts {
			if m.Type == "volume" && m.Name != "" {
				c.Volumes = append(c.Volumes, m.Name)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// List returns every stack: folders of the stacks directory holding a compose
// file, plus compose projects found on containers.
func (m *Manager) List(ctx context.Context, all []*Container) ([]*Stack, error) {
	byName := map[string]*Stack{}
	entries, err := os.ReadDir(m.Dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(m.Dir, e.Name())
		f := FindFile(dir)
		if f == "" {
			continue
		}
		st := &Stack{Name: projectName(e.Name()), Dir: dir, File: f, Managed: true}
		if p, err := m.Load(ctx, st.Name, f); err != nil {
			st.LoadErr = err.Error()
		} else {
			st.Name = p.Name
			for _, name := range p.ServiceNames() {
				s := p.Services[name]
				oneShot := (s.Restart == "" || s.Restart == types.RestartPolicyNo) && (s.Deploy == nil || s.Deploy.RestartPolicy == nil || s.Deploy.RestartPolicy.Condition == "none")
				st.Services = append(st.Services, &Service{Name: name, Image: s.Image, NetworkMode: s.NetworkMode, Declared: true, OneShot: oneShot})
			}
		}
		byName[st.Name] = st
	}
	for _, c := range all {
		if c.Project == "" {
			continue
		}
		st := byName[c.Project]
		if st == nil {
			st = &Stack{Name: c.Project}
			if files := c.Labels[api.ConfigFilesLabel]; files != "" {
				f, _, _ := strings.Cut(files, ",")
				if _, err := os.Stat(f); err == nil {
					st.File, st.Dir = f, filepath.Dir(f)
				}
			}
			byName[c.Project] = st
		}
		svc := st.service(c.Service)
		if svc.Image == "" {
			svc.Image = c.Image
		}
		svc.Containers = append(svc.Containers, c)
	}
	out := make([]*Stack, 0, len(byName))
	for _, st := range byName {
		st.summarize()
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns one stack by name.
func (m *Manager) Get(ctx context.Context, name string, all []*Container) (*Stack, error) {
	list, err := m.List(ctx, all)
	if err != nil {
		return nil, err
	}
	for _, s := range list {
		if s.Name == name {
			return s, nil
		}
	}
	return nil, os.ErrNotExist
}

func (st *Stack) service(name string) *Service {
	for _, s := range st.Services {
		if s.Name == name {
			return s
		}
	}
	s := &Service{Name: name}
	st.Services = append(st.Services, s)
	return s
}

func (st *Stack) summarize() {
	sort.SliceStable(st.Services, func(i, j int) bool { return st.Services[i].Name < st.Services[j].Name })
	st.Up, st.Total = 0, 0
	problem := false
	for _, s := range st.Services {
		st.Total++
		up := false
		for _, c := range s.Containers {
			switch {
			case c.Running():
				up = true
				problem = problem || c.Health == "unhealthy" || c.NetLost
			case c.State == string(container.StateRestarting), c.State == string(container.StateDead):
				problem = true
			case c.State == string(container.StateExited) && s.OneShot:
				up = up || c.ExitCode == 0
				// 137 and 143: killed or stopped on purpose.
				problem = problem || (c.ExitCode != 0 && c.ExitCode != 137 && c.ExitCode != 143)
			}
		}
		if up {
			st.Up++
		}
	}
	switch {
	case st.LoadErr != "" || problem:
		st.State = Problem
	case st.Up == 0:
		st.State = Stopped
	case st.Up < st.Total:
		st.State = Partial
	default:
		st.State = Running
	}
}

// Containers of the stack, flattened.
func (st *Stack) Containers() []*Container {
	var out []*Container
	for _, s := range st.Services {
		out = append(out, s.Containers...)
	}
	return out
}

// projectName mirrors compose's default: the folder name, lowercased, with
// characters other than [a-z0-9_-] removed.
func projectName(dir string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(dir) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return strings.TrimLeft(b.String(), "_-")
}
