package server

import (
	"slices"
	"sort"

	"github.com/neotoxicfr/dockerr/internal/netdeps"
	"github.com/neotoxicfr/dockerr/internal/stacks"
)

// The network map is a bus diagram: networks are columns, each container is
// one row with a dot on every network it joins. Containers sharing another
// one's namespace (network_mode: container:) sit under it instead of
// getting a row: every container appears exactly once.

type mapCol struct {
	Name     string
	Driver   string
	Internal bool
	Count    int
}

type mapCell struct {
	On bool
	IP string
}

type mapRow struct {
	C        *stacks.Container
	Cells    []mapCell
	Children []*stacks.Container
	Missing  string // parent reference when the row is a child whose parent is gone
}

type mapGroup struct {
	Stack string
	Rows  []mapRow
}

type netMap struct {
	Cols   []mapCol
	Groups []mapGroup
	Total  int
}

func buildNetMap(all []*stacks.Container, drivers map[string]mapCol) netMap {
	var m netMap
	byName := map[string]*stacks.Container{}
	for _, c := range all {
		byName[c.Name] = c
	}
	children := map[string][]*stacks.Container{}
	var roots []*stacks.Container
	var orphans []*stacks.Container
	for _, c := range all {
		if netdeps.Ref(c.NetworkMode) == "" {
			roots = append(roots, c)
			continue
		}
		if p, ok := byName[c.NetParent]; ok && p != c {
			children[p.Name] = append(children[p.Name], c)
		} else {
			orphans = append(orphans, c)
		}
	}

	count := map[string]int{}
	for _, c := range roots {
		for _, n := range c.Networks {
			if n != "none" {
				count[n]++
			}
		}
	}
	for n, k := range count {
		col := drivers[n]
		col.Name, col.Count = n, k
		m.Cols = append(m.Cols, col)
	}
	// Busiest networks first, host last: it is not a real bus.
	sort.Slice(m.Cols, func(i, j int) bool {
		a, b := m.Cols[i], m.Cols[j]
		if (a.Name == "host") != (b.Name == "host") {
			return b.Name == "host"
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Name < b.Name
	})

	groups := map[string]*mapGroup{}
	add := func(stack string, r mapRow) {
		g := groups[stack]
		if g == nil {
			g = &mapGroup{Stack: stack}
			groups[stack] = g
		}
		g.Rows = append(g.Rows, r)
		m.Total += 1 + len(r.Children)
	}
	for _, c := range roots {
		r := mapRow{C: c, Cells: make([]mapCell, len(m.Cols))}
		for i, col := range m.Cols {
			if slices.Contains(c.Networks, col.Name) {
				r.Cells[i] = mapCell{On: true, IP: c.IPs[col.Name]}
			}
		}
		r.Children = children[c.Name]
		sort.Slice(r.Children, func(i, j int) bool { return r.Children[i].Name < r.Children[j].Name })
		add(c.Project, r)
	}
	for _, c := range orphans {
		add(c.Project, mapRow{C: c, Cells: make([]mapCell, len(m.Cols)), Missing: c.NetParent})
	}
	for _, g := range groups {
		sort.Slice(g.Rows, func(i, j int) bool { return g.Rows[i].C.Name < g.Rows[j].C.Name })
		m.Groups = append(m.Groups, *g)
	}
	// Stacks by name, containers outside compose last.
	sort.Slice(m.Groups, func(i, j int) bool {
		a, b := m.Groups[i].Stack, m.Groups[j].Stack
		if (a == "") != (b == "") {
			return b == ""
		}
		return a < b
	})
	return m
}
