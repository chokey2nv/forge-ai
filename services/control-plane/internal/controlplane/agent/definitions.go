package agent

import "time"

type AgentDefinition struct {
	Name            string    `bson:"_id" json:"name"` // must match filename
	Description     string    `bson:"description" json:"description"`
	Version         int       `bson:"version" json:"version"`
	ModelPreference string    `bson:"modelPreference,omitempty" json:"modelPreference,omitempty"`
	Skills          []string  `bson:"skills,omitempty" json:"skills,omitempty"`
	Tools           []string  `bson:"tools" json:"tools"`
	Reviewers       []string  `bson:"reviewers,omitempty" json:"reviewers,omitempty"`
	SystemPrompt    string    `bson:"systemPrompt" json:"systemPrompt"`
	ContentHash     string    `bson:"contentHash" json:"contentHash"`
	LoadedAt        time.Time `bson:"loadedAt" json:"loadedAt"`
	SubmissionTool  string    `bson:"submissionTool,omitempty" json:"submissionTool,omitempty"`
}

type Skill struct {
	Name        string    `bson:"_id" json:"name"`
	Description string    `bson:"description" json:"description"`
	AppliesTo   []string  `bson:"appliesTo,omitempty" json:"appliesTo,omitempty"`
	Content     string    `bson:"content" json:"content"`
	ContentHash string    `bson:"contentHash" json:"contentHash"`
	LoadedAt    time.Time `bson:"loadedAt" json:"loadedAt"`
}
