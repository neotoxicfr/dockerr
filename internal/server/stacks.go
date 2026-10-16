package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"github.com/neotoxicfr/dockerr/internal/auth"
	"github.com/neotoxicfr/dockerr/internal/diff"
	"github.com/neotoxicfr/dockerr/internal/netdeps"
	"github.com/neotoxicfr/dockerr/internal/ops"
	"github.com/neotoxicfr/dockerr/internal/stacks"
	"github.com/neotoxicfr/dockerr/internal/store"
)

// snapshot gathers containers, stacks, network state, stats and updates.
func (s *Server) snapshot(ctx context.Context) ([]*stacks.Container, []*stacks.Stack, error) {
	all, err := s.stacks.Containers(ctx)
	if err != nil {
		return nil, nil, err
	}
	netdeps.Analyze(ctx, all, s.runner.Started)
	stats := s.stats.Get()
	for _, c := range all {
		if st, ok := stats[c.ID]; ok {
			c.CPU, c.Mem = st.CPU, st.Mem
		}
	}
	// A tag whose local image differs from the container's was pulled but
	// not applied yet.
	tagID := map[string]string{}
	if s.docker.Caps.Images {
		if imgs, err := s.docker.API.ImageList(ctx, client.ImageListOptions{}); err == nil {
			for _, im := range imgs.Items {
				for _, t := range im.RepoTags {
					tagID[t] = im.ID
				}
			}
		}
	}
	list, err := s.stacks.List(ctx, all)
	if err != nil {
		return nil, nil, err
	}
	self, _ := os.Hostname()
	for _, st := range list {
		for _, svc := range st.Services {
			for _, c := range svc.Containers {
				if len(self) >= 12 && strings.HasPrefix(c.ID, self) {
					st.Self = true
				}
				st.CPU += c.CPU
				st.Mem += c.Mem
				if r, ok := s.updates.Get(c.Image); ok && r.Available {
					svc.Update = true
				}
				if id, ok := tagID[normalizeTag(c.Image)]; ok && id != c.ImageID {
					svc.Update = true
				}
			}
			if svc.Update {
				st.Updates++
			}
		}
	}
	return all, list, nil
}

func normalizeTag(ref string) string {
	if !strings.Contains(ref, ":") || strings.LastIndex(ref, ":") < strings.LastIndex(ref, "/") {
		return ref + ":latest"
	}
	return ref
}

type homeData struct {
	Stacks                             []*stacks.Stack
	Running, Partial, Stopped, Problem int
	Updates                            int
	LastCheck                          time.Time
	Checking                           bool
	Err                                string
}

func (s *Server) homeData(ctx context.Context) homeData {
	var d homeData
	_, list, err := s.snapshot(ctx)
	if err != nil {
		d.Err = err.Error()
		return d
	}
	d.Stacks = list
	for _, st := range list {
		switch st.State {
		case stacks.Running:
			d.Running++
		case stacks.Partial:
			d.Partial++
		case stacks.Problem:
			d.Problem++
		default:
			d.Stopped++
		}
		if st.Updates > 0 {
			d.Updates++
		}
	}
	// Problems first, then the rest by name.
	rank := map[stacks.State]int{stacks.Problem: 0, stacks.Partial: 1, stacks.Running: 2, stacks.Stopped: 3}
	slices.SortStableFunc(d.Stacks, func(a, b *stacks.Stack) int { return rank[a.State] - rank[b.State] })
	d.LastCheck, d.Checking = s.updates.LastRun()
	return d
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "home", "Piles", "stacks", s.homeData(r.Context()))
}

func (s *Server) homeRows(w http.ResponseWriter, r *http.Request) {
	s.partial(w, r, "stack-rows", s.homeData(r.Context()))
}

type stackData struct {
	Stack     *stacks.Stack
	Tab       string
	Op        *ops.Snapshot
	DozzleURL string
	PortHost  string                         // host for published port links
	Children  map[string][]*stacks.Container // parent container ID → children
	Versions  []store.Version
	Version   *versionView
}

func (s *Server) loadStack(w http.ResponseWriter, r *http.Request) (*stacks.Stack, []*stacks.Container, bool) {
	name := r.PathValue("name")
	all, list, err := s.snapshot(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return nil, nil, false
	}
	for _, st := range list {
		if st.Name == name {
			return st, all, true
		}
	}
	http.NotFound(w, r)
	return nil, nil, false
}

func (s *Server) stackData(r *http.Request, st *stacks.Stack, all []*stacks.Container) stackData {
	d := stackData{Stack: st, Tab: r.URL.Query().Get("tab"), DozzleURL: s.cfg.DozzleURL, PortHost: s.portHost(r), Children: map[string][]*stacks.Container{}}
	if d.Tab != "compose" && d.Tab != "history" {
		d.Tab = "services"
	}
	if op := s.runner.Running(st.Name); op != nil {
		snap := op.Snapshot()
		d.Op = &snap
	}
	for _, c := range st.Containers() {
		if ch := netdeps.Children(c, all); len(ch) > 0 {
			d.Children[c.ID] = ch
		}
	}
	return d
}

func (s *Server) stackPage(w http.ResponseWriter, r *http.Request) {
	st, all, ok := s.loadStack(w, r)
	if !ok {
		return
	}
	d := s.stackData(r, st, all)
	if d.Tab == "history" {
		d.Versions, _ = s.store.Versions(r.Context(), st.Name)
	}
	s.render(w, r, "stack", st.Name, "stacks", d)
}

func (s *Server) stackServices(w http.ResponseWriter, r *http.Request) {
	st, all, ok := s.loadStack(w, r)
	if !ok {
		return
	}
	s.partial(w, r, "stack-live", s.stackData(r, st, all))
}

func (s *Server) stackOp(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.loadStack(w, r)
	if !ok {
		return
	}
	kind := ops.Kind(r.FormValue("kind"))
	service := r.FormValue("service")
	if !ops.Valid(kind) {
		jsonError(w, http.StatusBadRequest, "opération inconnue")
		return
	}
	if st.Self && kind != ops.Repair && kind != ops.Pull {
		jsonError(w, http.StatusConflict, "cette pile fait tourner dockerr : la piloter depuis dockerr l'arrêterait en cours de route")
		return
	}
	if service != "" && !slices.ContainsFunc(st.Services, func(x *stacks.Service) bool { return x.Name == service }) {
		jsonError(w, http.StatusBadRequest, "service inconnu")
		return
	}
	caps := s.docker.Caps
	switch kind {
	case ops.Stop, ops.Restart:
		if !caps.Lifecycle {
			jsonError(w, http.StatusForbidden, "le proxy Docker refuse l'arrêt et le démarrage (ALLOW_START, ALLOW_STOP, ALLOW_RESTARTS)")
			return
		}
	case ops.Repair:
		if !caps.Lifecycle {
			jsonError(w, http.StatusForbidden, "le proxy Docker refuse les redémarrages")
			return
		}
	default:
		if !caps.Compose() {
			jsonError(w, http.StatusForbidden, "le proxy Docker n'autorise pas compose : il faut CONTAINERS, IMAGES, NETWORKS, VOLUMES et POST")
			return
		}
	}
	if kind == ops.Down && !s.advanced(r) {
		jsonError(w, http.StatusForbidden, "réservé au mode avancé")
		return
	}
	op, err := s.runner.Run(st, kind, service, auth.User(r.Context()))
	if err != nil {
		jsonError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": op.ID})
}

func (s *Server) opStream(w http.ResponseWriter, r *http.Request) {
	op := s.runner.Get(r.PathValue("id"))
	if op == nil {
		http.NotFound(w, r)
		return
	}
	send, ok := sse(w)
	if !ok {
		return
	}
	ch, cancel := op.Watch()
	defer cancel()
	last := -1
	for {
		snap := op.Snapshot()
		if snap.Version != last {
			last = snap.Version
			if send("op", snap) != nil {
				return
			}
		}
		if snap.Done {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ch:
		case <-time.After(15 * time.Second):
		}
	}
}

func (s *Server) checkUpdates(w http.ResponseWriter, r *http.Request) {
	refs := s.imageRefs(r.Context())
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		s.updates.Check(ctx, refs)
		s.events.Notify()
	}()
	writeJSON(w, http.StatusAccepted, map[string]int{"images": len(refs)})
}

// --- compose files ---

type filesBody struct {
	Compose string `json:"compose"`
	Env     string `json:"env"`
	Apply   bool   `json:"apply"`
}

func readBody(w http.ResponseWriter, r *http.Request) (filesBody, bool) {
	var b filesBody
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		jsonError(w, http.StatusBadRequest, "requête invalide")
		return b, false
	}
	return b, true
}

func (s *Server) stackFiles(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.loadStack(w, r)
	if !ok {
		return
	}
	c, e, err := st.ReadFiles()
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"compose": c, "env": e, "file": st.File, "editable": st.Managed})
}

func (s *Server) stackValidate(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.loadStack(w, r)
	if !ok {
		return
	}
	b, ok := readBody(w, r)
	if !ok {
		return
	}
	issues := stacks.Validate(r.Context(), st.Name, st.Dir, b.Compose, b.Env)
	writeJSON(w, http.StatusOK, map[string]any{"issues": issues})
}

type diffView struct {
	Compose []*diff.Line `json:"compose"`
	Env     []*diff.Line `json:"env"`
	Changed bool         `json:"changed"`
}

func makeDiff(oldC, newC, oldE, newE string) diffView {
	c, e := diff.Lines(oldC, newC), diff.Lines(oldE, newE)
	return diffView{Compose: diff.Hunks(c, 3), Env: diff.Hunks(e, 3), Changed: diff.Changed(c) || diff.Changed(e)}
}

func (s *Server) stackDiff(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.loadStack(w, r)
	if !ok {
		return
	}
	b, ok := readBody(w, r)
	if !ok {
		return
	}
	c, e, err := st.ReadFiles()
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, makeDiff(c, b.Compose, e, b.Env))
}

func (s *Server) stackSave(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.loadStack(w, r)
	if !ok {
		return
	}
	b, ok := readBody(w, r)
	if !ok {
		return
	}
	if !st.Managed {
		jsonError(w, http.StatusForbidden, "pile hors du dossier des piles : lecture seule")
		return
	}
	if issues := stacks.Validate(r.Context(), st.Name, st.Dir, b.Compose, b.Env); issues != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"issues": issues})
		return
	}
	user := auth.User(r.Context())
	if err := s.saveFiles(r.Context(), st, b.Compose, b.Env, user, ""); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	res := map[string]string{}
	if b.Apply {
		if !s.docker.Caps.Compose() {
			jsonError(w, http.StatusForbidden, "enregistré, mais le proxy Docker n'autorise pas compose")
			return
		}
		op, err := s.runner.Run(st, ops.Start, "", user)
		if err != nil {
			jsonError(w, http.StatusConflict, "enregistré, mais "+err.Error())
			return
		}
		res["id"] = op.ID
	}
	writeJSON(w, http.StatusOK, res)
}

// saveFiles records the files as found on disk when they differ from the
// last version (first edit, or a change made outside dockerr), then writes
// the new content and records it.
func (s *Server) saveFiles(ctx context.Context, st *stacks.Stack, composeText, envText, user, note string) error {
	oldC, oldE, err := st.ReadFiles()
	if err != nil {
		return err
	}
	for _, v := range []store.Version{
		{Stack: st.Name, File: "compose", Content: oldC, Author: "disque", Note: "état sur le disque"},
		{Stack: st.Name, File: "env", Content: oldE, Author: "disque", Note: "état sur le disque"},
	} {
		if v.File == "env" && v.Content == "" {
			continue
		}
		if err := s.store.SaveVersion(ctx, v); err != nil {
			return err
		}
	}
	if err := st.WriteFiles(composeText, envText); err != nil {
		return err
	}
	if err := s.store.SaveVersion(ctx, store.Version{Stack: st.Name, File: "compose", Content: composeText, Author: user, Note: note}); err != nil {
		return err
	}
	if envText != "" || oldE != "" {
		return s.store.SaveVersion(ctx, store.Version{Stack: st.Name, File: "env", Content: envText, Author: user, Note: note})
	}
	return nil
}

// --- history ---

type versionView struct {
	store.Version
	Prev  *store.Version
	Lines []*diff.Line
}

func (s *Server) stackHistory(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/s/"+r.PathValue("name")+"?tab=history", http.StatusSeeOther)
}

func (s *Server) stackVersion(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.loadStack(w, r)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	v, err := s.store.Version(r.Context(), st.Name, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	vv := &versionView{Version: v}
	prev, err := s.store.PreviousVersion(r.Context(), v)
	if err == nil {
		vv.Prev = &prev
	}
	vv.Lines = diff.Hunks(diff.Lines(prev.Content, v.Content), 3)
	s.partial(w, r, "version", vv)
}

func (s *Server) stackRestore(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.loadStack(w, r)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	v, err := s.store.Version(r.Context(), st.Name, id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "version introuvable")
		return
	}
	c, e, err := st.ReadFiles()
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	if v.File == "compose" {
		c = v.Content
	} else {
		e = v.Content
	}
	if issues := stacks.Validate(r.Context(), st.Name, st.Dir, c, e); issues != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"issues": issues})
		return
	}
	if err := s.saveFiles(r.Context(), st, c, e, auth.User(r.Context()), "restauration de la version "+strconv.FormatInt(id, 10)); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- new stack ---

func (s *Server) newStack(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "new", "Nouvelle pile", "stacks", nil)
}

func (s *Server) validateNew(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name    string `json:"name"`
		Compose string `json:"compose"`
		Env     string `json:"env"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		jsonError(w, http.StatusBadRequest, "requête invalide")
		return
	}
	name := strings.TrimSpace(b.Name)
	if !stacks.ValidName(name) {
		name = "nouvelle-pile"
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": stacks.Validate(r.Context(), name, s.stacks.Dir+"/"+name, b.Compose, b.Env)})
}

func (s *Server) createStack(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name    string `json:"name"`
		Compose string `json:"compose"`
		Env     string `json:"env"`
		Start   bool   `json:"start"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		jsonError(w, http.StatusBadRequest, "requête invalide")
		return
	}
	b.Name = strings.TrimSpace(b.Name)
	if !stacks.ValidName(b.Name) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"field": "name", "error": "Minuscules, chiffres, - et _ seulement."})
		return
	}
	if _, err := os.Stat(s.stacks.Dir + "/" + b.Name); err == nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"field": "name", "error": "Ce dossier existe déjà."})
		return
	}
	if issues := stacks.Validate(r.Context(), b.Name, s.stacks.Dir+"/"+b.Name, b.Compose, b.Env); issues != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"issues": issues})
		return
	}
	user := auth.User(r.Context())
	if _, err := s.stacks.Create(b.Name, b.Compose, b.Env); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.SaveVersion(r.Context(), store.Version{Stack: b.Name, File: "compose", Content: b.Compose, Author: user, Note: "création"})
	if b.Env != "" {
		_ = s.store.SaveVersion(r.Context(), store.Version{Stack: b.Name, File: "env", Content: b.Env, Author: user, Note: "création"})
	}
	res := map[string]string{"url": "/s/" + b.Name}
	if b.Start {
		_, list, err := s.snapshot(r.Context())
		if err == nil {
			for _, st := range list {
				if st.Name == b.Name {
					if op, err := s.runner.Run(st, ops.Start, "", user); err == nil {
						res["id"] = op.ID
					}
				}
			}
		}
	}
	writeJSON(w, http.StatusCreated, res)
}

// --- settings ---

type settingsData struct {
	Host, DaemonVersion, APIVersion string
	Caps                            []capView
	LastCheck                       time.Time
	Checking                        bool
	ExecEnabled                     bool
	ExecLog                         []store.ExecEntry
	StacksDir                       string
	RecentOps                       []ops.Snapshot
}

type capView struct {
	Name, Env string
	OK        bool
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	d := settingsData{Host: s.docker.Host, ExecEnabled: s.cfg.ExecEnabled, StacksDir: s.cfg.StacksDir, RecentOps: s.runner.Recent(20)}
	if v, err := s.docker.API.ServerVersion(r.Context(), client.ServerVersionOptions{}); err == nil {
		d.DaemonVersion, d.APIVersion = v.Version, v.APIVersion
	}
	for _, c := range s.docker.Caps.List() {
		d.Caps = append(d.Caps, capView{c.Name, c.Env, c.OK})
	}
	d.LastCheck, d.Checking = s.updates.LastRun()
	d.ExecLog, _ = s.store.ExecLog(r.Context(), 20)
	s.render(w, r, "settings", "Réglages", "settings", d)
}

func (s *Server) setMode(w http.ResponseWriter, r *http.Request) {
	v := "0"
	if r.FormValue("advanced") == "1" {
		v = "1"
	}
	if err := s.store.SetPref(r.Context(), auth.User(r.Context()), "advanced", v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	back := "/settings"
	if u, err := url.Parse(r.Referer()); err == nil && strings.HasPrefix(u.Path, "/") {
		back = u.Path
		if u.RawQuery != "" {
			back += "?" + u.RawQuery
		}
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) portHost(r *http.Request) string {
	if s.cfg.PortHost != "" {
		return s.cfg.PortHost
	}
	h := r.Host
	if i := strings.LastIndex(h, ":"); i > strings.LastIndex(h, "]") {
		h = h[:i]
	}
	return h
}
