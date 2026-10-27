// Package composeedit reads a compose file into a form-friendly model and
// applies targeted edits to its YAML tree, keeping comments, anchors and
// everything it does not touch. The graphical editor works through it; the
// text editor works on the text directly.
package composeedit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"
)

type Doc struct{ root *yaml.Node } // document node

// Parse reads a compose file. Empty text gives an empty mapping.
func Parse(text string) (*Doc, error) {
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(text), &n); err != nil {
		return nil, err
	}
	if n.Kind == 0 || len(n.Content) == 0 {
		n = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	if n.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("le fichier compose doit être un dictionnaire de premier niveau")
	}
	return &Doc{root: &n}, nil
}

func (d *Doc) top() *yaml.Node { return d.root.Content[0] }

// String re-encodes the tree: two-space indent, comments kept.
func (d *Doc) String() string {
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	_ = e.Encode(d.root)
	_ = e.Close()
	return b.String()
}

// ---- model ----

type KV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Item is a list entry; Long entries (mapping syntax) are shown, not edited.
type Item struct {
	Value string `json:"value"`
	Long  bool   `json:"long,omitempty"`
}

type Service struct {
	Name        string   `json:"name"`
	Image       string   `json:"image"`
	Build       bool     `json:"build,omitempty"`
	Restart     string   `json:"restart"`
	Command     string   `json:"command"`
	NetworkMode string   `json:"network_mode"`
	Ports       []Item   `json:"ports"`
	Volumes     []Item   `json:"volumes"`
	Environment []KV     `json:"environment"`
	EnvFile     []string `json:"env_file"`
	Networks    []string `json:"networks"`
	DependsOn   []string `json:"depends_on"`
	Secrets     []string `json:"secrets"`
	Labels      []KV     `json:"labels"`
	// Locked fields can only change in text mode (aliases, merge keys,
	// non-scalar values the form cannot show faithfully).
	Locked []string `json:"locked"`
	// Other keys present, edited in text mode.
	Other []string `json:"other"`
}

type Secret struct {
	Name     string `json:"name"`
	File     string `json:"file"`
	External bool   `json:"external,omitempty"`
	Env      string `json:"environment,omitempty"`
}

type Model struct {
	Services []Service `json:"services"`
	Secrets  []Secret  `json:"secrets"`
	Networks []string  `json:"networks"`
	Volumes  []string  `json:"volumes"`
	Other    []string  `json:"other"`
}

// form fields of a service, in display order.
var serviceFields = []string{"image", "build", "restart", "command", "network_mode", "ports", "volumes",
	"environment", "env_file", "networks", "depends_on", "secrets", "labels"}

func (d *Doc) Model() Model {
	m := Model{Services: []Service{}, Secrets: []Secret{}, Networks: []string{}, Volumes: []string{}, Other: []string{}}
	top := d.top()
	for i := 0; i+1 < len(top.Content); i += 2 {
		k, v := top.Content[i].Value, resolve(top.Content[i+1])
		switch k {
		case "services":
			if v.Kind == yaml.MappingNode {
				for j := 0; j+1 < len(v.Content); j += 2 {
					m.Services = append(m.Services, service(v.Content[j].Value, v.Content[j+1]))
				}
			}
		case "secrets":
			if v.Kind == yaml.MappingNode {
				for j := 0; j+1 < len(v.Content); j += 2 {
					s := Secret{Name: v.Content[j].Value}
					if sv := resolve(v.Content[j+1]); sv.Kind == yaml.MappingNode {
						s.File = scalar(get(sv, "file"))
						s.Env = scalar(get(sv, "environment"))
						s.External = scalar(get(sv, "external")) == "true"
					}
					m.Secrets = append(m.Secrets, s)
				}
			}
		case "networks", "volumes":
			if v.Kind == yaml.MappingNode {
				for j := 0; j+1 < len(v.Content); j += 2 {
					if k == "networks" {
						m.Networks = append(m.Networks, v.Content[j].Value)
					} else {
						m.Volumes = append(m.Volumes, v.Content[j].Value)
					}
				}
			}
		case "name", "version":
		default:
			m.Other = append(m.Other, k)
		}
	}
	return m
}

func service(name string, n *yaml.Node) Service {
	s := Service{Name: name, Locked: []string{}, Other: []string{}}
	n0 := n
	n = resolve(n)
	if n.Kind != yaml.MappingNode {
		s.Locked = slices.Clone(serviceFields)
		return s
	}
	merged := n0.Kind == yaml.AliasNode || n0.Anchor != "" || get(n, "<<") != nil
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, raw := n.Content[i].Value, n.Content[i+1]
		v := resolve(raw)
		if !slices.Contains(serviceFields, k) {
			if k != "<<" {
				s.Other = append(s.Other, k)
			}
			continue
		}
		if raw.Kind == yaml.AliasNode || raw.Anchor != "" {
			s.Locked = append(s.Locked, k)
		}
		switch k {
		case "image":
			s.Image = scalar(v)
		case "build":
			s.Build = true
		case "restart":
			s.Restart = scalar(v)
		case "network_mode":
			s.NetworkMode = scalar(v)
		case "command":
			if v.Kind == yaml.SequenceNode {
				var parts []string
				for _, c := range v.Content {
					parts = append(parts, quoteArg(c.Value))
				}
				s.Command = strings.Join(parts, " ")
				s.Locked = append(s.Locked, k)
			} else {
				s.Command = scalar(v)
			}
		case "ports", "volumes":
			items := items(v)
			if k == "ports" {
				s.Ports = items
			} else {
				s.Volumes = items
			}
		case "environment", "labels":
			kvs, ok := kvs(v)
			if !ok {
				s.Locked = append(s.Locked, k)
			}
			if k == "environment" {
				s.Environment = kvs
			} else {
				s.Labels = kvs
			}
		case "env_file":
			s.EnvFile = names(v)
		case "networks":
			s.Networks = names(v)
			if v.Kind == yaml.MappingNode && hasNonEmptyValues(v) {
				s.Locked = append(s.Locked, k) // per-network options: text mode
			}
		case "depends_on":
			s.DependsOn = names(v)
			if v.Kind == yaml.MappingNode {
				s.Locked = append(s.Locked, k) // conditions: text mode
			}
		case "secrets":
			s.Secrets = names(v)
			if hasMapping(v) {
				s.Locked = append(s.Locked, k)
			}
		}
	}
	if merged {
		s.Locked = slices.Clone(serviceFields)
	}
	// The page iterates every list: never send null.
	for _, l := range []*[]Item{&s.Ports, &s.Volumes} {
		if *l == nil {
			*l = []Item{}
		}
	}
	for _, l := range []*[]KV{&s.Environment, &s.Labels} {
		if *l == nil {
			*l = []KV{}
		}
	}
	for _, l := range []*[]string{&s.EnvFile, &s.Networks, &s.DependsOn, &s.Secrets} {
		if *l == nil {
			*l = []string{}
		}
	}
	slices.Sort(s.Locked)
	s.Locked = slices.Compact(s.Locked)
	return s
}

func items(v *yaml.Node) []Item {
	out := []Item{}
	if v.Kind != yaml.SequenceNode {
		return out
	}
	for _, c := range v.Content {
		c = resolve(c)
		if c.Kind == yaml.ScalarNode {
			out = append(out, Item{Value: c.Value})
			continue
		}
		var parts []string
		for i := 0; i+1 < len(c.Content); i += 2 {
			parts = append(parts, c.Content[i].Value+"="+scalar(resolve(c.Content[i+1])))
		}
		out = append(out, Item{Value: strings.Join(parts, ", "), Long: true})
	}
	return out
}

// kvs reads a mapping or a KEY=VALUE list; ok is false for anything else.
func kvs(v *yaml.Node) ([]KV, bool) {
	out := []KV{}
	switch v.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(v.Content); i += 2 {
			val := resolve(v.Content[i+1])
			if val.Kind != yaml.ScalarNode {
				return out, false
			}
			out = append(out, KV{v.Content[i].Value, nullAsEmpty(val)})
		}
	case yaml.SequenceNode:
		for _, c := range v.Content {
			k, val, _ := strings.Cut(resolve(c).Value, "=")
			out = append(out, KV{k, val})
		}
	default:
		return out, false
	}
	return out, true
}

// names reads a list of strings or the keys of a mapping.
func names(v *yaml.Node) []string {
	out := []string{}
	switch v.Kind {
	case yaml.SequenceNode:
		for _, c := range v.Content {
			c = resolve(c)
			if c.Kind == yaml.ScalarNode {
				out = append(out, c.Value)
			} else if src := scalar(get(c, "source")); src != "" {
				out = append(out, src)
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(v.Content); i += 2 {
			out = append(out, v.Content[i].Value)
		}
	case yaml.ScalarNode:
		out = append(out, v.Value)
	}
	return out
}

func hasNonEmptyValues(v *yaml.Node) bool {
	for i := 1; i < len(v.Content); i += 2 {
		if c := resolve(v.Content[i]); c.Kind != yaml.ScalarNode || (c.Value != "" && c.Tag != "!!null") {
			return true
		}
	}
	return false
}

func hasMapping(v *yaml.Node) bool {
	for _, c := range v.Content {
		if resolve(c).Kind == yaml.MappingNode {
			return true
		}
	}
	return false
}

// ---- edits ----

// Edit is one change made in the graphical editor.
//
//	set    Path to a scalar (Value: string); "" removes the key
//	list   Path to a list of strings (Value: []string); empty removes it
//	map    Path to environment/labels (Value: []KV); empty removes it
//	remove Path to any key (Value unused)
type Edit struct {
	Op    string          `json:"op"`
	Path  []string        `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

// ErrLocked means the target can only be edited in text mode.
var ErrLocked = errors.New("cette partie utilise des ancres YAML ou une syntaxe longue : modifie-la en mode texte")

func (d *Doc) Apply(edits []Edit) error {
	for _, e := range edits {
		if err := d.apply(e); err != nil {
			return fmt.Errorf("%s %s : %w", e.Op, strings.Join(e.Path, "."), err)
		}
	}
	return nil
}

func (d *Doc) apply(e Edit) error {
	if len(e.Path) == 0 {
		return errors.New("chemin vide")
	}
	for _, p := range e.Path {
		if p == "" || p == "<<" {
			return errors.New("chemin invalide")
		}
	}
	parent, err := d.walk(e.Path[:len(e.Path)-1], e.Op != "remove")
	if err != nil {
		return err
	}
	if parent == nil {
		return nil // removing under a missing key: nothing to do
	}
	key := e.Path[len(e.Path)-1]
	old := get(parent, key)
	if old != nil && (old.Kind == yaml.AliasNode || old.Anchor != "") {
		return ErrLocked
	}
	switch e.Op {
	case "remove":
		del(parent, key)
		return nil
	case "set":
		var v string
		if err := json.Unmarshal(e.Value, &v); err != nil {
			return errors.New("valeur attendue : texte")
		}
		if v == "" {
			del(parent, key)
			return nil
		}
		n := str(v)
		if old != nil && old.Kind == yaml.ScalarNode {
			old.Value, old.Tag, old.Style = n.Value, n.Tag, n.Style
			return nil
		}
		put(parent, key, n)
		return nil
	case "list":
		var vs []string
		if err := json.Unmarshal(e.Value, &vs); err != nil {
			return errors.New("valeur attendue : liste de textes")
		}
		vs = slices.DeleteFunc(vs, func(s string) bool { return strings.TrimSpace(s) == "" })
		if old != nil && hasMapping(old) {
			return ErrLocked // long syntax entries would be lost
		}
		if len(vs) == 0 {
			del(parent, key)
			return nil
		}
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, s := range vs {
			n := str(s)
			if key == "ports" {
				n.Style = yaml.DoubleQuotedStyle // "22:22" must not read as a number
			}
			seq.Content = append(seq.Content, n)
		}
		keepComments(old, seq)
		put(parent, key, seq)
		return nil
	case "map":
		var kv []KV
		if err := json.Unmarshal(e.Value, &kv); err != nil {
			return errors.New("valeur attendue : liste clé/valeur")
		}
		kv = slices.DeleteFunc(kv, func(x KV) bool { return strings.TrimSpace(x.Key) == "" })
		if old != nil && old.Kind == yaml.MappingNode && hasNonScalar(old) {
			return ErrLocked
		}
		if len(kv) == 0 {
			del(parent, key)
			return nil
		}
		var n *yaml.Node
		if old != nil && old.Kind == yaml.SequenceNode {
			n = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			for _, x := range kv {
				n.Content = append(n.Content, str(strings.TrimSpace(x.Key)+"="+x.Value))
			}
		} else {
			n = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			for _, x := range kv {
				n.Content = append(n.Content, str(strings.TrimSpace(x.Key)), str(x.Value))
			}
		}
		keepComments(old, n)
		put(parent, key, n)
		return nil
	}
	return fmt.Errorf("opération inconnue %q", e.Op)
}

// walk follows path from the top mapping, creating mappings when create is
// set. It returns nil, nil when a key is missing and create is not set.
func (d *Doc) walk(path []string, create bool) (*yaml.Node, error) {
	n := d.top()
	for _, p := range path {
		if get(n, "<<") != nil {
			return nil, ErrLocked
		}
		c := get(n, p)
		if c == nil {
			if !create {
				return nil, nil
			}
			c = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			put(n, p, c)
		}
		if c.Kind == yaml.AliasNode || c.Anchor != "" {
			return nil, ErrLocked
		}
		if c.Kind == yaml.ScalarNode && (c.Tag == "!!null" || c.Value == "") && create {
			// "service:" with nothing under it
			c.Kind, c.Tag, c.Value, c.Style = yaml.MappingNode, "!!map", "", 0
		}
		if c.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s n'est pas un dictionnaire", p)
		}
		n = c
	}
	if get(n, "<<") != nil {
		return nil, ErrLocked
	}
	return n, nil
}

func hasNonScalar(v *yaml.Node) bool {
	for i := 1; i < len(v.Content); i += 2 {
		if resolve(v.Content[i]).Kind != yaml.ScalarNode {
			return true
		}
	}
	return false
}

// keepComments moves the comments of the replaced node to its replacement.
func keepComments(old, n *yaml.Node) {
	if old == nil {
		return
	}
	n.HeadComment, n.LineComment, n.FootComment = old.HeadComment, old.LineComment, old.FootComment
}

// ---- yaml helpers ----

func resolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

func get(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func put(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, str(key), v)
}

func del(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = slices.Delete(m.Content, i, i+2)
			return
		}
	}
}

func str(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

func scalar(n *yaml.Node) string {
	n = resolve(n)
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return nullAsEmpty(n)
}

func nullAsEmpty(n *yaml.Node) string {
	if n.Tag == "!!null" {
		return ""
	}
	return n.Value
}

func quoteArg(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\"'") {
		return fmt.Sprintf("%q", s)
	}
	return s
}
