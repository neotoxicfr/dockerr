package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/client"

	"github.com/neotoxicfr/dockerr/internal/auth"
	"github.com/neotoxicfr/dockerr/internal/netdeps"
	"github.com/neotoxicfr/dockerr/internal/stacks"
)

// advancedOnly guards the advanced-mode pages.
func (s *Server) advancedOnly(w http.ResponseWriter, r *http.Request) bool {
	if s.advanced(r) {
		return true
	}
	if r.Method == http.MethodGet {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
	} else {
		jsonError(w, http.StatusForbidden, "réservé au mode avancé")
	}
	return false
}

type imageRow struct {
	ID      string
	Tags    []string
	Size    int64
	Created time.Time
	Used    int
}

type networkRow struct {
	ID, Name, Driver, Project string
	Scope                     string
	Internal, Attachable      bool
	IPv6                      bool
	Created                   string
	Subnets                   []kv // subnet → gateway
	Options                   []kv
	Containers                []*stacks.Container
	Members                   []netMember
}

// netMember is a container's endpoint on a network.
type netMember struct {
	C               *stacks.Container // nil when not listed (another host, etc.)
	Name            string
	IPv4, IPv6, MAC string
}

type volumeRow struct {
	Name, Driver, Project string
	Created               string
	Users                 []*stacks.Container
}

type resourcesData struct {
	Tab        string
	Containers []*stacks.Container
	Images     []imageRow
	Networks   []networkRow
	Volumes    []volumeRow
	Err        string
}

func (s *Server) resources(w http.ResponseWriter, r *http.Request) {
	if !s.advancedOnly(w, r) {
		return
	}
	ctx := r.Context()
	d := resourcesData{Tab: r.URL.Query().Get("tab")}
	if !slices.Contains([]string{"containers", "images", "networks", "volumes"}, d.Tab) {
		d.Tab = "containers"
	}
	all, _, err := s.snapshot(ctx)
	if err != nil {
		d.Err = err.Error()
		s.render(w, r, "resources", "Ressources", "resources", d)
		return
	}
	switch d.Tab {
	case "containers":
		d.Containers = all
		sort.Slice(d.Containers, func(i, j int) bool { return d.Containers[i].Name < d.Containers[j].Name })
	case "images":
		d.Images, err = s.imageRows(ctx, all)
	case "networks":
		d.Networks, err = s.networkRows(ctx, all)
	case "volumes":
		d.Volumes, err = s.volumeRows(ctx, all)
	}
	if err != nil {
		d.Err = err.Error()
	}
	s.render(w, r, "resources", "Ressources", "resources", d)
}

func (s *Server) imageRows(ctx context.Context, all []*stacks.Container) ([]imageRow, error) {
	res, err := s.docker.API.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return nil, err
	}
	used := map[string]int{}
	for _, c := range all {
		used[c.ImageID]++
	}
	var out []imageRow
	for _, im := range res.Items {
		tags := slices.DeleteFunc(slices.Clone(im.RepoTags), func(t string) bool { return t == "<none>:<none>" })
		out = append(out, imageRow{ID: im.ID, Tags: tags, Size: im.Size, Created: time.Unix(im.Created, 0), Used: used[im.ID]})
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Used == 0) != (out[j].Used == 0) {
			return out[i].Used == 0
		}
		return out[i].Size > out[j].Size
	})
	return out, nil
}

func (s *Server) networkRows(ctx context.Context, all []*stacks.Container) ([]networkRow, error) {
	res, err := s.docker.API.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, err
	}
	byID := map[string]*stacks.Container{}
	for _, c := range all {
		byID[c.ID] = c
	}
	var out []networkRow
	for _, n := range res.Items {
		row := networkRow{ID: n.ID, Name: n.Name, Driver: n.Driver, Scope: n.Scope, Internal: n.Internal, Attachable: n.Attachable,
			IPv6: n.EnableIPv6, Project: n.Labels[api.ProjectLabel]}
		if !n.Created.IsZero() {
			row.Created = n.Created.Local().Format("02/01/2006 15:04")
		}
		for _, c := range all {
			if slices.Contains(c.Networks, n.Name) {
				row.Containers = append(row.Containers, c)
			}
		}
		// The list has no addresses: inspect each network.
		if in, err := s.docker.API.NetworkInspect(ctx, n.ID, client.NetworkInspectOptions{}); err == nil {
			for _, cfg := range in.Network.IPAM.Config {
				sub, gw := "", ""
				if cfg.Subnet.IsValid() {
					sub = cfg.Subnet.String()
				}
				if cfg.Gateway.IsValid() {
					gw = cfg.Gateway.String()
				}
				if sub != "" {
					row.Subnets = append(row.Subnets, kv{sub, gw})
				}
			}
			for k, v := range in.Network.Options {
				row.Options = append(row.Options, kv{k, v})
			}
			sort.Slice(row.Options, func(i, j int) bool { return row.Options[i].K < row.Options[j].K })
			for id, ep := range in.Network.Containers {
				m := netMember{C: byID[id], Name: ep.Name, MAC: ep.MacAddress.String()}
				if ep.IPv4Address.IsValid() {
					m.IPv4 = ep.IPv4Address.Addr().String()
				}
				if ep.IPv6Address.IsValid() {
					m.IPv6 = ep.IPv6Address.Addr().String()
				}
				row.Members = append(row.Members, m)
			}
			sort.Slice(row.Members, func(i, j int) bool { return row.Members[i].Name < row.Members[j].Name })
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Server) volumeRows(ctx context.Context, all []*stacks.Container) ([]volumeRow, error) {
	res, err := s.docker.API.VolumeList(ctx, client.VolumeListOptions{})
	if err != nil {
		return nil, err
	}
	var out []volumeRow
	for _, v := range res.Items {
		row := volumeRow{Name: v.Name, Driver: v.Driver, Project: v.Labels[api.ProjectLabel], Created: v.CreatedAt}
		if t, err := time.Parse(time.RFC3339, v.CreatedAt); err == nil {
			row.Created = t.Local().Format("02/01/2006")
		}
		for _, c := range all {
			if slices.Contains(c.Volumes, v.Name) {
				row.Users = append(row.Users, c)
			}
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if (len(out[i].Users) == 0) != (len(out[j].Users) == 0) {
			return len(out[i].Users) == 0
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

type kv struct{ K, V string }

type ctrView struct {
	*stacks.Container
	Cmd          string
	Created      string
	Started      string
	Restart      string
	RestartCount int
	Mounts       []kv
	Env          []kv
	Secret       []string // env keys whose value stays masked
	LabelList    []kv
	IPs          []kv
	Children     []*stacks.Container
	Raw          string
	DozzleURL    string
	ExecEnabled  bool
}

func (s *Server) findContainer(w http.ResponseWriter, r *http.Request) (*stacks.Container, []*stacks.Container, bool) {
	id := r.PathValue("id")
	all, _, err := s.snapshot(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return nil, nil, false
	}
	for _, c := range all {
		if c.ID == id || c.Name == id || (len(id) >= 12 && strings.HasPrefix(c.ID, id)) {
			return c, all, true
		}
	}
	http.NotFound(w, r)
	return nil, nil, false
}

func (s *Server) containerPage(w http.ResponseWriter, r *http.Request) {
	if !s.advancedOnly(w, r) {
		return
	}
	c, all, ok := s.findContainer(w, r)
	if !ok {
		return
	}
	res, err := s.docker.API.ContainerInspect(r.Context(), c.ID, client.ContainerInspectOptions{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	in := res.Container
	v := ctrView{Container: c, Created: isoDate(in.Created), RestartCount: in.RestartCount, Children: netdeps.Children(c, all),
		DozzleURL: s.cfg.DozzleURL, ExecEnabled: s.cfg.ExecEnabled && s.docker.Caps.Exec}
	if in.State != nil {
		v.Started = isoDate(in.State.StartedAt)
	}
	if in.Config != nil {
		v.Cmd = strings.Join(append(slices.Clone(in.Config.Entrypoint), in.Config.Cmd...), " ")
		for _, e := range in.Config.Env {
			k, val, _ := strings.Cut(e, "=")
			if sensitiveKey.MatchString(k) {
				val = "" // never in the page: fetched when the user clicks
				v.Secret = append(v.Secret, k)
			}
			v.Env = append(v.Env, kv{k, val})
		}
		for k, val := range in.Config.Labels {
			v.LabelList = append(v.LabelList, kv{k, val})
		}
		sort.Slice(v.LabelList, func(i, j int) bool { return v.LabelList[i].K < v.LabelList[j].K })
	}
	if in.HostConfig != nil {
		v.Restart = string(in.HostConfig.RestartPolicy.Name)
	}
	for _, m := range in.Mounts {
		src := m.Source
		if m.Name != "" {
			src = m.Name
		}
		mode := "lecture-écriture"
		if !m.RW {
			mode = "lecture seule"
		}
		v.Mounts = append(v.Mounts, kv{src + " → " + m.Destination, string(m.Type) + ", " + mode})
	}
	if in.NetworkSettings != nil {
		for name, ep := range in.NetworkSettings.Networks {
			if ep == nil {
				continue
			}
			ip := ""
			if ep.IPAddress.IsValid() {
				ip = ep.IPAddress.String()
			}
			v.IPs = append(v.IPs, kv{name, ip})
		}
		sort.Slice(v.IPs, func(i, j int) bool { return v.IPs[i].K < v.IPs[j].K })
	}
	var raw any
	if json.Unmarshal(res.Raw, &raw) == nil {
		b, _ := json.MarshalIndent(raw, "", "  ")
		v.Raw = string(b)
	}
	s.render(w, r, "container", c.Name, "resources", v)
}

func (s *Server) containerAction(w http.ResponseWriter, r *http.Request) {
	if !s.advancedOnly(w, r) {
		return
	}
	c, _, ok := s.findContainer(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var err error
	switch r.FormValue("action") {
	case "start":
		_, err = s.docker.API.ContainerStart(ctx, c.ID, client.ContainerStartOptions{})
	case "stop":
		_, err = s.docker.API.ContainerStop(ctx, c.ID, client.ContainerStopOptions{})
	case "restart":
		_, err = s.docker.API.ContainerRestart(ctx, c.ID, client.ContainerRestartOptions{})
	case "remove":
		_, err = s.docker.API.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true})
	default:
		jsonError(w, http.StatusBadRequest, "action inconnue")
		return
	}
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) prune(w http.ResponseWriter, r *http.Request) {
	if !s.advancedOnly(w, r) {
		return
	}
	ctx := r.Context()
	var freed uint64
	var n int
	var err error
	switch r.FormValue("kind") {
	case "images":
		var res client.ImagePruneResult
		res, err = s.docker.API.ImagePrune(ctx, client.ImagePruneOptions{Filters: make(client.Filters).Add("dangling", "false")})
		freed, n = res.Report.SpaceReclaimed, len(res.Report.ImagesDeleted)
	case "networks":
		var res client.NetworkPruneResult
		res, err = s.docker.API.NetworkPrune(ctx, client.NetworkPruneOptions{})
		n = len(res.Report.NetworksDeleted)
	case "volumes":
		// Anonymous volumes only: named ones hold data someone chose to keep.
		var res client.VolumePruneResult
		res, err = s.docker.API.VolumePrune(ctx, client.VolumePruneOptions{})
		freed, n = res.Report.SpaceReclaimed, len(res.Report.VolumesDeleted)
	case "containers":
		var res client.ContainerPruneResult
		res, err = s.docker.API.ContainerPrune(ctx, client.ContainerPruneOptions{})
		freed, n = res.Report.SpaceReclaimed, len(res.Report.ContainersDeleted)
	default:
		jsonError(w, http.StatusBadRequest, "type inconnu")
		return
	}
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": n, "freed": humanBytes(freed)})
}

func (s *Server) imageRemove(w http.ResponseWriter, r *http.Request) {
	if !s.advancedOnly(w, r) {
		return
	}
	if _, err := s.docker.API.ImageRemove(r.Context(), r.PathValue("id"), client.ImageRemoveOptions{PruneChildren: true}); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type networkData struct {
	Map netMap
	Err string
}

func (s *Server) networkPage(w http.ResponseWriter, r *http.Request) {
	if !s.advancedOnly(w, r) {
		return
	}
	var d networkData
	all, _, err := s.snapshot(r.Context())
	if err != nil {
		d.Err = err.Error()
		s.render(w, r, "network", "Réseau", "network", d)
		return
	}
	drivers := map[string]mapCol{}
	if s.docker.Caps.Networks {
		if res, err := s.docker.API.NetworkList(r.Context(), client.NetworkListOptions{}); err == nil {
			for _, n := range res.Items {
				drivers[n.Name] = mapCol{Driver: n.Driver, Internal: n.Internal}
			}
		}
	}
	d.Map = buildNetMap(all, drivers)
	s.render(w, r, "network", "Réseau", "network", d)
}

// isoDate formats a Docker RFC 3339 timestamp for display.
func isoDate(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 2000 {
		return ""
	}
	return t.Local().Format("02/01/2006 15:04:05")
}

// sensitiveKey matches environment variables that likely hold a secret.
var sensitiveKey = regexp.MustCompile(`(?i)pass|secret|token|key|pwd|auth|cred|private|salt`)

// containerEnv returns one environment value, for the reveal button.
func (s *Server) containerEnv(w http.ResponseWriter, r *http.Request) {
	if !s.advancedOnly(w, r) {
		return
	}
	c, _, ok := s.findContainer(w, r)
	if !ok {
		return
	}
	res, err := s.docker.API.ContainerInspect(r.Context(), c.ID, client.ContainerInspectOptions{})
	if err != nil || res.Container.Config == nil {
		jsonError(w, http.StatusBadGateway, "inspection impossible")
		return
	}
	key := r.URL.Query().Get("key")
	for _, e := range res.Container.Config.Env {
		if k, v, _ := strings.Cut(e, "="); k == key {
			slog.Info("env revealed", "user", auth.User(r.Context()), "container", c.Name, "key", key)
			writeJSON(w, http.StatusOK, map[string]string{"value": v})
			return
		}
	}
	jsonError(w, http.StatusNotFound, "variable inconnue")
}
