// Package ops runs compose operations in the background and keeps their
// progress for live display. One operation runs at a time per stack.
package ops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/client"

	"github.com/neotoxicfr/dockerr/internal/netdeps"
	"github.com/neotoxicfr/dockerr/internal/stacks"
)

type Kind string

const (
	Start    Kind = "start"    // up -d
	Stop     Kind = "stop"     // stop
	Restart  Kind = "restart"  // restart
	Update   Kind = "update"   // pull, then up -d
	Pull     Kind = "pull"     // pull only
	Recreate Kind = "recreate" // up -d --force-recreate
	Down     Kind = "down"     // down --remove-orphans
	Repair   Kind = "repair"   // rejoin lost network namespaces
)

var Labels = map[Kind]string{
	Start: "Démarrage", Stop: "Arrêt", Restart: "Redémarrage", Update: "Mise à jour",
	Pull: "Téléchargement", Recreate: "Recréation", Down: "Suppression", Repair: "Réparation réseau",
}

// Valid kinds a client may request.
func Valid(k Kind) bool { _, ok := Labels[k]; return ok }

type Task struct {
	ID, Text, Details string
	Status            string // working, done, warning, error
	Percent           int
}

type Op struct {
	ID, Stack, Service, User string
	Kind                     Kind
	Started                  time.Time

	mu      sync.Mutex
	ended   time.Time
	err     string
	tasks   []*Task
	notify  map[chan struct{}]struct{}
	version int
}

// Snapshot is a consistent copy of an operation's progress.
type Snapshot struct {
	ID, Stack, Service, User, Label string
	Kind                            Kind
	Started, Ended                  time.Time
	Done                            bool
	Err                             string
	Tasks                           []Task
	Version                         int
}

func (o *Op) Snapshot() Snapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	s := Snapshot{ID: o.ID, Stack: o.Stack, Service: o.Service, User: o.User, Kind: o.Kind, Label: Labels[o.Kind],
		Started: o.Started, Ended: o.ended, Done: !o.ended.IsZero(), Err: o.err, Version: o.version}
	for _, t := range o.tasks {
		s.Tasks = append(s.Tasks, *t)
	}
	return s
}

// Watch returns a channel signalled on each change, and a cancel func.
func (o *Op) Watch() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	o.mu.Lock()
	o.notify[ch] = struct{}{}
	o.mu.Unlock()
	return ch, func() {
		o.mu.Lock()
		delete(o.notify, ch)
		o.mu.Unlock()
	}
}

func (o *Op) changed() {
	o.version++
	for ch := range o.notify {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (o *Op) set(t Task) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, x := range o.tasks {
		if x.ID == t.ID {
			*x = t
			o.changed()
			return
		}
	}
	o.tasks = append(o.tasks, &t)
	o.changed()
}

func (o *Op) finish(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ended = time.Now()
	if err != nil {
		o.err = err.Error()
	}
	o.changed()
}

// processor feeds compose progress events into an Op.
type processor struct{ op *Op }

func (p processor) Start(context.Context, string) {}
func (p processor) Done(string, bool)             {}
func (p processor) On(events ...api.Resource) {
	for _, e := range events {
		if e.ParentID != "" {
			continue // per-layer pull progress: too fine for the UI
		}
		p.op.set(Task{ID: e.ID, Text: e.Text, Details: e.Details, Status: strings.ToLower(e.StatusText()), Percent: e.Percent})
	}
}

type Runner struct {
	Stacks *stacks.Manager
	// RepairAfter runs a network repair pass after each operation.
	RepairAfter bool
	// OnDone is called after each operation, e.g. to refresh update checks.
	OnDone func(Snapshot)

	mu      sync.Mutex
	ops     map[string]*Op
	order   []string
	running map[string]*Op
}

func NewRunner(m *stacks.Manager, repair bool) *Runner {
	return &Runner{Stacks: m, RepairAfter: repair, ops: map[string]*Op{}, running: map[string]*Op{}}
}

var ErrBusy = errors.New("une opération est déjà en cours sur cette pile")

func (r *Runner) Get(id string) *Op {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ops[id]
}

// Running returns the operation in progress on a stack, if any.
func (r *Runner) Running(stack string) *Op {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running[stack]
}

// Recent returns the last operations, newest first.
func (r *Runner) Recent(n int) []Snapshot {
	r.mu.Lock()
	ids := slices.Clone(r.order)
	r.mu.Unlock()
	var out []Snapshot
	for i := len(ids) - 1; i >= 0 && len(out) < n; i-- {
		if op := r.Get(ids[i]); op != nil {
			out = append(out, op.Snapshot())
		}
	}
	return out
}

// Run starts an operation on a stack (service "" = whole stack).
func (r *Runner) Run(stack *stacks.Stack, kind Kind, service, user string) (*Op, error) {
	r.mu.Lock()
	if _, busy := r.running[stack.Name]; busy {
		r.mu.Unlock()
		return nil, ErrBusy
	}
	op := &Op{ID: newID(), Stack: stack.Name, Service: service, User: user, Kind: kind, Started: time.Now(), notify: map[chan struct{}]struct{}{}}
	r.ops[op.ID] = op
	r.order = append(r.order, op.ID)
	if len(r.order) > 100 {
		delete(r.ops, r.order[0])
		r.order = r.order[1:]
	}
	r.running[stack.Name] = op
	r.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		err := r.exec(ctx, op, stack)
		if r.RepairAfter && (kind == Start || kind == Restart || kind == Update || kind == Recreate) {
			if rerr := r.repair(ctx, op); rerr != nil && err == nil {
				err = rerr
			}
		}
		op.finish(err)
		slog.Info("op", "stack", op.Stack, "kind", op.Kind, "service", op.Service, "user", op.User, "err", err)
		r.mu.Lock()
		delete(r.running, stack.Name)
		r.mu.Unlock()
		if r.OnDone != nil {
			r.OnDone(op.Snapshot())
		}
	}()
	return op, nil
}

func (r *Runner) exec(ctx context.Context, op *Op, st *stacks.Stack) error {
	if op.Kind == Repair {
		return r.repair(ctx, op)
	}
	svc, err := r.Stacks.Compose(processor{op})
	if err != nil {
		return err
	}
	var services []string
	if op.Service != "" {
		services = []string{op.Service}
	}
	var project *types.Project
	if st.File != "" {
		if project, err = r.Stacks.Load(ctx, st.Name, st.File); err != nil {
			return fmt.Errorf("compose invalide : %w", err)
		}
	}
	needProject := func() error {
		if project == nil {
			return errors.New("fichier compose introuvable pour cette pile")
		}
		return nil
	}
	up := func(recreate string) error {
		if err := needProject(); err != nil {
			return err
		}
		return svc.Up(ctx, project, api.UpOptions{
			Create: api.CreateOptions{Services: services, Recreate: recreate, RecreateDependencies: api.RecreateDiverged, QuietPull: true},
			Start:  api.StartOptions{Project: project, Services: services},
		})
	}
	pull := func() error {
		if err := needProject(); err != nil {
			return err
		}
		p := project
		if len(services) > 0 {
			if p, err = project.WithSelectedServices(services); err != nil {
				return err
			}
		}
		return svc.Pull(ctx, p, api.PullOptions{IgnoreBuildable: true})
	}
	switch op.Kind {
	case Start:
		if project == nil {
			return svc.Start(ctx, st.Name, api.StartOptions{Services: services})
		}
		return up(api.RecreateDiverged)
	case Stop:
		return svc.Stop(ctx, st.Name, api.StopOptions{Project: project, Services: services})
	case Restart:
		if project == nil {
			return svc.Restart(ctx, st.Name, api.RestartOptions{Services: services})
		}
		// compose restarts services in parallel unless depends_on says
		// restart: true, so a container sharing another's network namespace
		// restarts while its parent is down and fails. Stop then start
		// instead: both follow the dependency order, dependents included.
		names := services
		if len(services) > 0 {
			p, err := project.WithSelectedServices(services, types.IncludeDependents)
			if err != nil {
				return err
			}
			names = p.ServiceNames()
		}
		if err := svc.Stop(ctx, st.Name, api.StopOptions{Project: project, Services: names}); err != nil {
			return err
		}
		return svc.Start(ctx, st.Name, api.StartOptions{Project: project, Services: names})
	case Pull:
		return pull()
	case Update:
		if err := pull(); err != nil {
			return err
		}
		return up(api.RecreateDiverged)
	case Recreate:
		return up(api.RecreateForce)
	case Down:
		return svc.Down(ctx, st.Name, api.DownOptions{Project: project, Services: services, RemoveOrphans: len(services) == 0})
	}
	return fmt.Errorf("opération inconnue %q", op.Kind)
}

// repair finds every container that lost its parent's network namespace,
// in any stack, and restarts or recreates it.
func (r *Runner) repair(ctx context.Context, op *Op) error {
	all, err := r.Stacks.Containers(ctx)
	if err != nil {
		return err
	}
	fixes := netdeps.Analyze(ctx, all, r.Started)
	var errs []error
	for _, f := range fixes {
		if f.Parent != nil && !f.Parent.Running() {
			continue // nothing to rejoin until the parent runs again
		}
		id := "Réseau " + f.Child.Name
		target := "Reconnexion"
		if f.Parent != nil {
			target += " à " + f.Parent.Name
		}
		op.set(Task{ID: id, Text: target, Status: "working"})
		var err error
		switch f.Action {
		case netdeps.Restart:
			_, err = r.Stacks.Docker.API.ContainerRestart(ctx, f.Child.ID, client.ContainerRestartOptions{})
		case netdeps.Recreate:
			err = r.recreate(ctx, op, f.Child)
		}
		if err != nil {
			op.set(Task{ID: id, Text: "Échec", Details: err.Error(), Status: "error"})
			errs = append(errs, fmt.Errorf("%s : %w", f.Child.Name, err))
			continue
		}
		if f.Parent != nil {
			op.set(Task{ID: id, Text: "Reconnecté à " + f.Parent.Name, Status: "done"})
		} else {
			op.set(Task{ID: id, Text: "Recréé", Status: "done"})
		}
	}
	return errors.Join(errs...)
}

func (r *Runner) recreate(ctx context.Context, op *Op, c *stacks.Container) error {
	if c.Project == "" || c.Service == "" {
		return errors.New("conteneur hors compose : à recréer à la main")
	}
	file, _, _ := strings.Cut(c.Labels[api.ConfigFilesLabel], ",")
	if file == "" {
		return errors.New("fichier compose inconnu")
	}
	project, err := r.Stacks.Load(ctx, c.Project, file)
	if err != nil {
		return err
	}
	svc, err := r.Stacks.Compose(processor{op})
	if err != nil {
		return err
	}
	services := []string{c.Service}
	return svc.Up(ctx, project, api.UpOptions{
		Create: api.CreateOptions{Services: services, Recreate: api.RecreateForce, RecreateDependencies: api.RecreateNever},
		Start:  api.StartOptions{Project: project, Services: services},
	})
}

// Started returns when a container last started.
func (r *Runner) Started(ctx context.Context, id string) (time.Time, error) {
	res, err := r.Stacks.Docker.API.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return time.Time{}, err
	}
	if res.Container.State == nil {
		return time.Time{}, errors.New("no state")
	}
	return time.Parse(time.RFC3339Nano, res.Container.State.StartedAt)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
