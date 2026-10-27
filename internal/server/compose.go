package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/moby/moby/client"

	"github.com/neotoxicfr/dockerr/internal/auth"
	"github.com/neotoxicfr/dockerr/internal/composeedit"
	"github.com/neotoxicfr/dockerr/internal/stacks"
)

// host gathers what the compose checks compare against.
func (s *Server) host(ctx context.Context, all []*stacks.Container) stacks.Host {
	h := stacks.Host{Containers: map[string]bool{}, Ports: map[string]stacks.PortUser{}}
	for _, c := range all {
		h.Containers[c.Name] = true
		h.Containers[c.ID] = true
		if len(c.ID) >= 12 {
			h.Containers[c.ID[:12]] = true
		}
		if !c.Running() {
			continue
		}
		for _, p := range c.Ports {
			h.Ports[strconv.Itoa(int(p.Public))+"/"+p.Proto] = stacks.PortUser{Container: c.Name, Project: c.Project}
		}
	}
	if s.docker.Caps.Networks {
		if res, err := s.docker.API.NetworkList(ctx, client.NetworkListOptions{}); err == nil {
			h.Networks = map[string]bool{}
			for _, n := range res.Items {
				h.Networks[n.Name] = true
			}
		}
	}
	if s.docker.Caps.Volumes {
		if res, err := s.docker.API.VolumeList(ctx, client.VolumeListOptions{}); err == nil {
			h.Volumes = map[string]bool{}
			for _, v := range res.Items {
				h.Volumes[v.Name] = true
			}
		}
	}
	return h
}

type checkBody struct {
	Stack   string             `json:"stack"` // existing stack, or "" for a new one
	Name    string             `json:"name"`  // new stack name
	Compose string             `json:"compose"`
	Env     string             `json:"env"`
	Edits   []composeedit.Edit `json:"edits"`
}

type checkResult struct {
	Compose  string             `json:"compose"`
	Model    *composeedit.Model `json:"model"`
	Issues   []stacks.Issue     `json:"issues"`
	Warnings []stacks.Issue     `json:"warnings"`
	EditErr  string             `json:"edit_error,omitempty"`
}

// composeCheck applies graphical edits (if any) to the compose text, then
// checks the result. The text it returns is the one both editors show.
func (s *Server) composeCheck(w http.ResponseWriter, r *http.Request) {
	var b checkBody
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		jsonError(w, http.StatusBadRequest, "requête invalide")
		return
	}
	all, list, err := s.snapshot(r.Context())
	if err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	name := strings.TrimSpace(b.Name)
	var dir string
	if b.Stack != "" {
		i := slices.IndexFunc(list, func(st *stacks.Stack) bool { return st.Name == b.Stack })
		if i < 0 {
			jsonError(w, http.StatusNotFound, "pile introuvable")
			return
		}
		name, dir = list[i].Name, list[i].Dir
	} else {
		if !stacks.ValidName(name) {
			name = "nouvelle-pile"
		}
		dir = filepath.Join(s.stacks.Dir, name)
	}
	res := checkResult{Compose: b.Compose, Issues: []stacks.Issue{}, Warnings: []stacks.Issue{}}
	doc, perr := composeedit.Parse(b.Compose)
	if perr == nil && len(b.Edits) > 0 {
		if err := doc.Apply(b.Edits); err != nil {
			res.EditErr = err.Error()
		} else {
			res.Compose = doc.String()
		}
	}
	if perr == nil {
		m := doc.Model()
		res.Model = &m
	}
	rep := stacks.Check(r.Context(), name, dir, res.Compose, b.Env, s.host(r.Context(), all))
	res.Issues, res.Warnings = rep.Issues, rep.Warnings
	writeJSON(w, http.StatusOK, res)
}

// secretFile finds a secret's file in the stack's compose file.
func secretFile(st *stacks.Stack, name string) (string, bool) {
	c, _, err := st.ReadFiles()
	if err != nil {
		return "", false
	}
	doc, err := composeedit.Parse(c)
	if err != nil {
		return "", false
	}
	for _, sec := range doc.Model().Secrets {
		if sec.Name == name && sec.File != "" {
			return sec.File, true
		}
	}
	return "", false
}

// secretReveal returns a secret's value: the page asks only when the user
// clicks "Afficher", and every reveal is logged.
func (s *Server) secretReveal(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.loadStack(w, r)
	if !ok {
		return
	}
	name := r.PathValue("secret")
	file, ok := secretFile(st, name)
	if !ok {
		jsonError(w, http.StatusNotFound, "secret inconnu ou sans fichier")
		return
	}
	v, err := stacks.ReadSecret(st.Dir, file)
	if err != nil {
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}
	slog.Info("secret revealed", "user", auth.User(r.Context()), "stack", st.Name, "secret", name)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"value": v})
}

type secretBody struct {
	Name     string   `json:"name"`
	Value    *string  `json:"value"` // nil: keep the current value
	Services []string `json:"services"`
	Remove   bool     `json:"remove"`
	Base     string   `json:"base"`
}

// secretSave creates, updates or removes a secret: its file under the stack
// folder (0600) and its declaration and use in the compose file.
func (s *Server) secretSave(w http.ResponseWriter, r *http.Request) {
	st, _, ok := s.loadStack(w, r)
	if !ok {
		return
	}
	if !st.Managed {
		jsonError(w, http.StatusForbidden, "pile hors du dossier des piles : lecture seule")
		return
	}
	var b secretBody
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || !stacks.ValidSecretName(b.Name) {
		jsonError(w, http.StatusBadRequest, "nom de secret invalide : lettres, chiffres, . _ et -")
		return
	}
	c, e, err := st.ReadFiles()
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	if b.Base != "" && filesHash(c, e) != b.Base {
		jsonError(w, http.StatusConflict, "le fichier a changé sur le disque depuis l'ouverture : recharge avant de continuer")
		return
	}
	doc, err := composeedit.Parse(c)
	if err != nil {
		jsonError(w, http.StatusUnprocessableEntity, "compose illisible : corrige-le en mode texte d'abord")
		return
	}
	m := doc.Model()
	file := "./secrets/" + b.Name
	declared := false
	for _, sec := range m.Secrets {
		if sec.Name == b.Name {
			declared = true
			if sec.External || sec.File == "" {
				jsonError(w, http.StatusConflict, "ce secret n'est pas un fichier de la pile : modifie-le en mode texte")
				return
			}
			file = sec.File
		}
	}
	raw := func(v any) json.RawMessage { j, _ := json.Marshal(v); return j }
	var edits []composeedit.Edit
	for _, svc := range m.Services {
		want := slices.Contains(b.Services, svc.Name) && !b.Remove
		has := slices.Contains(svc.Secrets, b.Name)
		if want == has {
			continue
		}
		list := slices.DeleteFunc(slices.Clone(svc.Secrets), func(x string) bool { return x == b.Name })
		if want {
			list = append(list, b.Name)
		}
		edits = append(edits, composeedit.Edit{Op: "list", Path: []string{"services", svc.Name, "secrets"}, Value: raw(list)})
	}
	if b.Remove {
		edits = append(edits, composeedit.Edit{Op: "remove", Path: []string{"secrets", b.Name}})
	} else if !declared {
		edits = append(edits, composeedit.Edit{Op: "set", Path: []string{"secrets", b.Name, "file"}, Value: raw(file)})
	}
	if _, err := stacks.SecretPath(st.Dir, file); err != nil {
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}
	user := auth.User(r.Context())
	if len(edits) > 0 {
		if err := doc.Apply(edits); err != nil {
			jsonError(w, http.StatusConflict, err.Error())
			return
		}
		c = doc.String()
	}
	// The file first: compose refuses a declared secret whose file is missing.
	switch {
	case b.Remove:
		if err := stacks.RemoveSecret(st.Dir, file); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
	case b.Value != nil:
		if err := stacks.WriteSecret(st.Dir, file, *b.Value); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if len(edits) > 0 {
		if err := s.saveFiles(r.Context(), st, c, e, user, "secret "+b.Name); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	slog.Info("secret saved", "user", user, "stack", st.Name, "secret", b.Name, "remove", b.Remove, "value_changed", b.Value != nil)
	writeJSON(w, http.StatusOK, map[string]string{"compose": c, "hash": filesHash(c, e)})
}
