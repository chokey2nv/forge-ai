package api

import (
	"fmt"
	"strings"
)

// graphTask is one node of a submitted task graph, by submission index.
type graphTask struct {
	ID        string
	DependsOn []string // submission IDs
	Scope     []string // diffScope patterns
}

// implicitDep records that task Of must wait for task On, and why.
type implicitDep struct {
	Of, On int
	Reason string
}

// findCycle returns the IDs of a dependency cycle, or nil. A cycle would
// leave its tasks waiting on each other forever.
func findCycle(tasks []graphTask) []string {
	index := make(map[string]int, len(tasks))
	for i, t := range tasks {
		index[t.ID] = i
	}
	const (
		unvisited = iota
		onStack
		done
	)
	state := make([]int, len(tasks))
	var stack []string
	var visit func(i int) []string
	visit = func(i int) []string {
		state[i] = onStack
		stack = append(stack, tasks[i].ID)
		for _, dep := range tasks[i].DependsOn {
			j, ok := index[dep]
			if !ok {
				continue
			}
			switch state[j] {
			case onStack:
				for k, id := range stack {
					if id == dep {
						return append(append([]string{}, stack[k:]...), dep)
					}
				}
			case unvisited:
				if c := visit(j); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[i] = done
		return nil
	}
	for i := range tasks {
		if state[i] == unvisited {
			if c := visit(i); c != nil {
				return c
			}
		}
	}
	return nil
}

// serializeOverlaps decides which independent tasks may not run in parallel.
// For each pair with no dependency path between them, in submission order,
// overlapping scopes (or a missing scope, which could touch anything) make
// the later task wait for the earlier. Parallelism is earned by declaring
// disjoint scopes; the diff-scope gate then keeps each child inside its own.
// The graph must be acyclic.
func serializeOverlaps(tasks []graphTask) []implicitDep {
	index := make(map[string]int, len(tasks))
	for i, t := range tasks {
		index[t.ID] = i
	}
	deps := make([]map[int]bool, len(tasks)) // i waits for each key
	for i, t := range tasks {
		deps[i] = map[int]bool{}
		for _, d := range t.DependsOn {
			deps[i][index[d]] = true
		}
	}
	// waitsFor reports whether a (transitively) waits for b.
	var waitsFor func(a, b int, seen map[int]bool) bool
	waitsFor = func(a, b int, seen map[int]bool) bool {
		if seen[a] {
			return false
		}
		seen[a] = true
		for d := range deps[a] {
			if d == b || waitsFor(d, b, seen) {
				return true
			}
		}
		return false
	}

	var out []implicitDep
	for j := range tasks {
		for i := 0; i < j; i++ {
			if waitsFor(j, i, map[int]bool{}) || waitsFor(i, j, map[int]bool{}) {
				continue
			}
			reason, overlap := scopeConflict(tasks[i].Scope, tasks[j].Scope)
			if !overlap {
				continue
			}
			deps[j][i] = true
			out = append(out, implicitDep{Of: j, On: i, Reason: reason})
		}
	}
	return out
}

// scopeConflict reports whether two scopes might touch the same file.
func scopeConflict(a, b []string) (string, bool) {
	if len(a) == 0 || len(b) == 0 {
		return "no diffScope declared, so it may touch any file", true
	}
	for _, p := range a {
		for _, q := range b {
			if patternsOverlap(p, q) {
				if p == q {
					return fmt.Sprintf("both may change %s", p), true
				}
				return fmt.Sprintf("overlapping scope (%s and %s)", p, q), true
			}
		}
	}
	return "", false
}

// patternsOverlap matches the diff-scope gate's patterns: an exact path, or
// dir/** for everything under dir. Any other wildcard is treated
// conservatively: it overlaps when the text before its first wildcard could
// lead to the other pattern.
func patternsOverlap(p, q string) bool {
	p, q = strings.TrimPrefix(p, "./"), strings.TrimPrefix(q, "./")
	if p == q {
		return true
	}
	pp, pWild := staticPrefix(p)
	qp, qWild := staticPrefix(q)
	switch {
	case !pWild && !qWild:
		return false // two different exact paths
	case pWild && !qWild:
		return strings.HasPrefix(q, pp)
	case !pWild && qWild:
		return strings.HasPrefix(p, qp)
	default:
		return strings.HasPrefix(pp, qp) || strings.HasPrefix(qp, pp)
	}
}

// staticPrefix is the pattern up to its first wildcard; "dir/**" yields "dir/".
func staticPrefix(p string) (string, bool) {
	if i := strings.IndexAny(p, "*?["); i >= 0 {
		return p[:i], true
	}
	return p, false
}
