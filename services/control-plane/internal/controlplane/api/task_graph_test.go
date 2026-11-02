package api

import (
	"strings"
	"testing"
)

func TestPatternsOverlap(t *testing.T) {
	cases := []struct {
		p, q string
		want bool
	}{
		{"src/tax.js", "src/tax.js", true},
		{"src/tax.js", "src/format.js", false},
		{"src/**", "src/tax.js", true},
		{"src/tax.js", "src/**", true},
		{"src/**", "tests/**", false},
		{"src/**", "src/util/**", true},
		{"./src/tax.js", "src/tax.js", true},
		{"src/*.js", "src/tax.js", true},  // other wildcards: conservative
		{"src/*.js", "tests/a.js", false}, // ... but not when prefixes differ
		{"src/taxes/**", "src/tax.js", false},
	}
	for _, c := range cases {
		if got := patternsOverlap(c.p, c.q); got != c.want {
			t.Errorf("patternsOverlap(%q, %q) = %v, want %v", c.p, c.q, got, c.want)
		}
	}
}

func deps(out []implicitDep) map[[2]int]string {
	m := map[[2]int]string{}
	for _, d := range out {
		m[[2]int{d.Of, d.On}] = d.Reason
	}
	return m
}

func TestSerializeOverlaps(t *testing.T) {
	t.Run("disjoint scopes run in parallel", func(t *testing.T) {
		got := serializeOverlaps([]graphTask{
			{ID: "a", Scope: []string{"src/tax.js", "tests/tax.test.js"}},
			{ID: "b", Scope: []string{"src/format.js", "tests/format.test.js"}},
		})
		if len(got) != 0 {
			t.Errorf("want no implicit deps, got %v", got)
		}
	})

	t.Run("same file: the later waits for the earlier", func(t *testing.T) {
		got := deps(serializeOverlaps([]graphTask{
			{ID: "a", Scope: []string{"src/price.js"}},
			{ID: "b", Scope: []string{"src/price.js", "tests/b.js"}},
		}))
		if why, ok := got[[2]int{1, 0}]; !ok || !strings.Contains(why, "src/price.js") {
			t.Errorf("b must wait for a, naming the file; got %v", got)
		}
	})

	t.Run("a task without a scope waits for every earlier one", func(t *testing.T) {
		got := deps(serializeOverlaps([]graphTask{
			{ID: "a", Scope: []string{"src/a.js"}},
			{ID: "b", Scope: []string{"src/b.js"}},
			{ID: "c"},
		}))
		if len(got) != 2 || got[[2]int{2, 0}] == "" || got[[2]int{2, 1}] == "" {
			t.Errorf("c must wait for a and b; got %v", got)
		}
		if !strings.Contains(got[[2]int{2, 0}], "no diffScope") {
			t.Errorf("reason should say no scope was declared: %q", got[[2]int{2, 0}])
		}
	})

	t.Run("an existing dependency path needs no implicit edge", func(t *testing.T) {
		got := serializeOverlaps([]graphTask{
			{ID: "a", Scope: []string{"src/x.js"}},
			{ID: "b", Scope: []string{"src/y.js"}, DependsOn: []string{"a"}},
			{ID: "c", Scope: []string{"src/x.js"}, DependsOn: []string{"b"}},
		})
		if len(got) != 0 {
			t.Errorf("c already waits for a through b; got %v", got)
		}
	})

	t.Run("an earlier task that waits for a later one is not reversed", func(t *testing.T) {
		got := serializeOverlaps([]graphTask{
			{ID: "a", Scope: []string{"src/x.js"}, DependsOn: []string{"b"}},
			{ID: "b", Scope: []string{"src/x.js"}},
		})
		if len(got) != 0 {
			t.Errorf("adding b -> a would create a cycle; got %v", got)
		}
	})
}

func TestFindCycle(t *testing.T) {
	if c := findCycle([]graphTask{{ID: "a"}, {ID: "b", DependsOn: []string{"a"}}, {ID: "c", DependsOn: []string{"a", "b"}}}); c != nil {
		t.Errorf("acyclic graph reported cycle %v", c)
	}
	c := findCycle([]graphTask{{ID: "a", DependsOn: []string{"c"}}, {ID: "b", DependsOn: []string{"a"}}, {ID: "c", DependsOn: []string{"b"}}})
	if len(c) != 4 || c[0] != c[3] {
		t.Errorf("want a closed cycle of three, got %v", c)
	}
	if c := findCycle([]graphTask{{ID: "a", DependsOn: []string{"a"}}}); c == nil {
		t.Error("self-dependency is a cycle")
	}
}
