package agent

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

type Registry struct {
	mu        sync.RWMutex
	agents    map[string]*AgentDefinition
	skills    map[string]*Skill
	lastLoad  time.Time
	lastErrs  []error
	lastWarns []string

	agentsDir string
	skillsDir string
	mongoColl *mongo.Collection
}

func NewRegistry(agentsDir, skillsDir string, mongoColl *mongo.Collection) *Registry {
	return &Registry{
		agentsDir: agentsDir,
		skillsDir: skillsDir,
		mongoColl: mongoColl,
		agents:    map[string]*AgentDefinition{},
		skills:    map[string]*Skill{},
	}
}

// Load (re-)reads files, validates, swaps the registry atomically, and
// persists a version snapshot to Mongo.
func (r *Registry) Load(ctx context.Context) error {
	skills, skillErrs := LoadSkillsDir(r.skillsDir)
	if len(skillErrs) > 0 {
		return errors.Join(skillErrs...)
	}

	agentRes, err := LoadAgentsDir(r.agentsDir, skills)
	if err != nil {
		return err
	}
	if len(agentRes.Errors) > 0 {
		return errors.Join(agentRes.Errors...)
	}

	r.mu.Lock()
	r.agents = agentRes.Agents
	r.skills = skills
	r.lastLoad = time.Now().UTC()
	r.lastErrs = nil
	r.lastWarns = agentRes.Warnings
	r.mu.Unlock()

	// Persist snapshot (best-effort; registry works even if Mongo is down)
	if r.mongoColl != nil {
		_ = r.persistSnapshot(ctx)
	}
	return nil
}

func (r *Registry) persistSnapshot(ctx context.Context) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	snap := map[string]any{
		"_id":      "snapshot-" + r.lastLoad.Format("20060102T150405Z"),
		"loadedAt": r.lastLoad,
		"agents":   r.agents,
		"skills":   r.skills,
		"warnings": r.lastWarns,
	}
	_, err := r.mongoColl.InsertOne(ctx, snap)
	return err
}

// ---- read API ----

func (r *Registry) GetAgent(name string) (*AgentDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[name]
	return a, ok
}

func (r *Registry) GetSkill(name string) (*Skill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.skills[name]
	return s, ok
}

func (r *Registry) ListAgents() []*AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*AgentDefinition, 0, len(r.agents))
	for _, a := range r.agents {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) ListSkills() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Skill, 0, len(r.skills))
	for _, s := range r.skills {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) Status() map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return map[string]any{
		"agentCount": len(r.agents),
		"skillCount": len(r.skills),
		"lastLoadAt": r.lastLoad,
		"warnings":   r.lastWarns,
	}
}
