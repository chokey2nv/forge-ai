package agent

import (
	"fmt"
	"slices"
	"strings"
)

// Compose produces the final systemPrompt and tool schemas for an agent.
// This is the single place where "agent definition" becomes "what the model sees".
func Compose(a *AgentDefinition, skillLookup func(string) (*Skill, bool)) (systemPrompt string, tools []ToolSchema, err error) {
	toolNames := a.Tools
	if a.SubmissionTool != "" {
		// Verify it exists in catalog; append if not already listed.
		if _, ok := ToolCatalog[a.SubmissionTool]; !ok {
			return "", nil, fmt.Errorf("agent %q has unknown submission_tool %q", a.Name, a.SubmissionTool)
		}
		if !slices.Contains(toolNames, a.SubmissionTool) {
			toolNames = append(toolNames, a.SubmissionTool)
		}
	}

	resolved, missing := ResolveTools(toolNames)
	if len(missing) > 0 {
		return "", nil, fmt.Errorf("agent %q references unknown tools: %v", a.Name, missing)
	}

	var b strings.Builder
	b.WriteString(a.SystemPrompt)

	if len(a.Skills) > 0 {
		b.WriteString("\n\n# Skills\n\n")
		b.WriteString("The following skills define how work is done in this team. Follow them.\n")
		for _, name := range a.Skills {
			s, ok := skillLookup(name)
			if !ok {
				return "", nil, fmt.Errorf("agent %q references unknown skill %q", a.Name, name)
			}
			fmt.Fprintf(&b, "\n## Skill: %s\n\n%s\n", s.Name, s.Content)
		}
	}

	return b.String(), resolved, nil
}
