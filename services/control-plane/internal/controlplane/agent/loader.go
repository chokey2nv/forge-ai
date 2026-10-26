package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type LoadResult struct {
	Agents   map[string]*AgentDefinition
	Skills   map[string]*Skill
	Errors   []error   // files that failed validation
	Warnings []string  // non-fatal (e.g. unknown reviewers)
}

// LoadAgentsDir reads every *.md under dir, parses frontmatter + body,
// validates references against the skill set and tool catalog.
func LoadAgentsDir(dir string, skills map[string]*Skill) (*LoadResult, error) {
	res := &LoadResult{
		Agents: map[string]*AgentDefinition{},
		Skills: skills,
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		agent, err := loadAgentFile(path)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		res.Agents[agent.Name] = agent
	}

	// Second pass: validate references (skills, reviewers)
	for _, a := range res.Agents {
		for _, s := range a.Skills {
			if _, ok := skills[s]; !ok {
				res.Errors = append(res.Errors, fmt.Errorf("agent %q references unknown skill %q", a.Name, s))
			}
		}
		for _, t := range a.Tools {
			if _, ok := ToolCatalog[t]; !ok {
				res.Errors = append(res.Errors, fmt.Errorf("agent %q references unknown tool %q", a.Name, t))
			}
		}
		for _, r := range a.Reviewers {
			if _, ok := res.Agents[r]; !ok {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("agent %q lists reviewer %q which is not yet loaded", a.Name, r))
			}
		}
	}

	return res, nil
}

// LoadSkillsDir reads every *.md under dir into Skill structs.
func LoadSkillsDir(dir string) (map[string]*Skill, []error) {
	out := map[string]*Skill{}
	var errs []error

	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, []error{err}
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		skill, err := loadSkillFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		out[skill.Name] = skill
	}
	return out, errs
}

// ---- internal helpers ----

func loadAgentFile(path string) (*AgentDefinition, error) {
	front, body, err := parseFrontmatter(path)
	if err != nil {
		return nil, err
	}

	var meta struct {
		Name            string   `yaml:"name"`
		Description     string   `yaml:"description"`
		Version         int      `yaml:"version"`
		ModelPreference string   `yaml:"model_preference"`
		Skills          []string `yaml:"skills"`
		Tools           []string `yaml:"tools"`
		Reviewers       []string `yaml:"reviewers"`
	}
	if err := yaml.Unmarshal(front, &meta); err != nil {
		return nil, fmt.Errorf("frontmatter yaml: %w", err)
	}

	baseName := strings.TrimSuffix(filepath.Base(path), ".md")
	if meta.Name != baseName {
		return nil, fmt.Errorf("frontmatter name %q must match filename %q", meta.Name, baseName)
	}
	if meta.Description == "" {
		return nil, fmt.Errorf("description is required")
	}
	if meta.Version == 0 {
		meta.Version = 1
	}
	if len(meta.Tools) == 0 {
		return nil, fmt.Errorf("at least one tool is required")
	}

	sort.Strings(meta.Tools)
	sort.Strings(meta.Skills)

	return &AgentDefinition{
		Name:            meta.Name,
		Description:     meta.Description,
		Version:         meta.Version,
		ModelPreference: meta.ModelPreference,
		Skills:          meta.Skills,
		Tools:           meta.Tools,
		Reviewers:       meta.Reviewers,
		SystemPrompt:    strings.TrimSpace(body),
		ContentHash:     hash(front, body),
		LoadedAt:        time.Now().UTC(),
	}, nil
}

func loadSkillFile(path string) (*Skill, error) {
	front, body, err := parseFrontmatter(path)
	if err != nil {
		return nil, err
	}

	var meta struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		AppliesTo   []string `yaml:"applies_to"`
	}
	if err := yaml.Unmarshal(front, &meta); err != nil {
		return nil, fmt.Errorf("frontmatter yaml: %w", err)
	}

	baseName := strings.TrimSuffix(filepath.Base(path), ".md")
	if meta.Name != baseName {
		return nil, fmt.Errorf("frontmatter name %q must match filename %q", meta.Name, baseName)
	}
	if meta.Description == "" {
		return nil, fmt.Errorf("description is required")
	}

	return &Skill{
		Name:        meta.Name,
		Description: meta.Description,
		AppliesTo:   meta.AppliesTo,
		Content:     strings.TrimSpace(body),
		ContentHash: hash(front, body),
		LoadedAt:    time.Now().UTC(),
	}, nil
}

// parseFrontmatter splits a `---\nYAML\n---\nbody` file.
func parseFrontmatter(path string) (front, body []byte, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	s := string(raw)
	if !strings.HasPrefix(s, "---\n") {
		return nil, nil, fmt.Errorf("missing frontmatter (file must start with '---')")
	}
	rest := s[4:]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return nil, nil, fmt.Errorf("unterminated frontmatter")
	}
	front = []byte(rest[:idx])
	body = []byte(rest[idx+5:])
	return front, body, nil
}

func hash(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}