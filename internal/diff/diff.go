// Package diff computes a line diff for showing compose changes.
package diff

import "strings"

type Op byte

const (
	Same Op = ' '
	Add  Op = '+'
	Del  Op = '-'
)

type Line struct {
	Op       Op
	Text     string
	Old, New int // 1-based line numbers, 0 when absent on that side
}

// Lines diffs a and b line by line (longest common subsequence). Inputs are
// compose files, a few hundred lines at most.
func Lines(a, b string) []Line {
	x, y := split(a), split(b)
	n, m := len(x), len(y)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []Line
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && x[i] == y[j]:
			out = append(out, Line{Same, x[i], i + 1, j + 1})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			out = append(out, Line{Del, x[i], i + 1, 0})
			i++
		default:
			out = append(out, Line{Add, y[j], 0, j + 1})
			j++
		}
	}
	return out
}

// Changed reports whether the diff has any addition or deletion.
func Changed(ls []Line) bool {
	for _, l := range ls {
		if l.Op != Same {
			return true
		}
	}
	return false
}

// Hunks keeps changed lines with ctx lines of context around them; a nil
// entry marks skipped lines.
func Hunks(ls []Line, ctx int) []*Line {
	keep := make([]bool, len(ls))
	for i, l := range ls {
		if l.Op == Same {
			continue
		}
		for k := max(0, i-ctx); k <= min(len(ls)-1, i+ctx); k++ {
			keep[k] = true
		}
	}
	var out []*Line
	gap := false
	for i := range ls {
		if !keep[i] {
			gap = true
			continue
		}
		if gap && len(out) > 0 {
			out = append(out, nil)
		}
		gap = false
		out = append(out, &ls[i])
	}
	return out
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
