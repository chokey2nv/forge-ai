package agent

import (
	"fmt"
	"strings"
)

// Compose produces the final systemPrompt and tool schemas for an agent.
// This is the single place where "agent definition" becomes "what the model sees".
func Compose(a *AgentDefinition, skillLookup func(string) (*Skill, bool)) (systemPrompt string, tools []ToolSchema, err error) {
	resolved, missing := ResolveTools(a.Tools)
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