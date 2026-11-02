package agent

import (
	"path/filepath"
	"runtime"
	"testing"
)

// Every agent shipped in the repo must compose; a missing catalog entry
// (e.g. a submission tool) otherwise only surfaces mid-pipeline.
func TestRepoAgentsCompose(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..")

	skills, errs := LoadSkillsDir(filepath.Join(root, "skills"))
	for _, err := range errs {
		t.Errorf("skill: %v", err)
	}
	res, err := LoadAgentsDir(filepath.Join(root, "agents"), skills)
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range res.Errors {
		t.Errorf("agent: %v", err)
	}
	if len(res.Agents) == 0 {
		t.Fatal("no agents loaded")
	}

	lookup := func(name string) (*Skill, bool) { s, ok := skills[name]; return s, ok }
	for name, a := range res.Agents {
		if _, _, err := Compose(a, lookup); err != nil {
			t.Errorf("compose %s: %v", name, err)
		}
		for _, r := range a.Reviewers {
			if _, ok := res.Agents[r]; !ok {
				t.Errorf("agent %s lists unknown reviewer %q", name, r)
			}
		}
	}
}
