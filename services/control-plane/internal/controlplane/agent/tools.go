package agent

type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolCatalog is the exhaustive set of tools any agent may reference.
// Adding a tool: add an entry here AND an executor in the adapter's executor.ts.
// Both are needed — CP owns the schema, adapter owns the execution.
var ToolCatalog = map[string]ToolSchema{
	"submit_task_graph": {
		Name:        "submit_task_graph",
		Description: "Submit the decomposition. Call this once you have analyzed the requirement and identified the specialized subtasks.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":            map[string]any{"type": "string", "description": "Short ID unique within this graph"},
							"title":         map[string]any{"type": "string"},
							"assignedAgent": map[string]any{"type": "string", "description": "Name of the agent from the registry"},
							"acceptance":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							"dependsOn":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "IDs of tasks that must integrate before this one starts"},
							"diffScope":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional file globs this task may modify"},
							"rationale":     map[string]any{"type": "string", "description": "Why this decomposition (visible in review)"},
						},
						"required": []string{"id", "title", "assignedAgent", "acceptance", "dependsOn"},
					},
				},
				"notes": map[string]any{"type": "string", "description": "Overall strategy in one or two sentences"},
			},
			"required": []string{"tasks"},
		},
	},
	"read_file": {
		Name:        "read_file",
		Description: "Read a file from the workspace. Optional startLine/endLine for a range.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":      map[string]any{"type": "string", "description": "Path relative to workspace root"},
				"startLine": map[string]any{"type": "number"},
				"endLine":   map[string]any{"type": "number"},
			},
			"required": []string{"path"},
		},
	},
	"list_dir": {
		Name:        "list_dir",
		Description: "List files under a directory, depth-limited.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":  map[string]any{"type": "string"},
				"depth": map[string]any{"type": "number"},
			},
			"required": []string{"path"},
		},
	},
	"search": {
		Name:        "search",
		Description: "Search for a literal pattern across the workspace (git grep).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
				"glob":  map[string]any{"type": "string"},
			},
			"required": []string{"query"},
		},
	},
	"write_file": {
		Name:        "write_file",
		Description: "Write or overwrite a file. Returns the resulting git diff.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			"required": []string{"path", "content"},
		},
	},
	"apply_patch": {
		Name:        "apply_patch",
		Description: "Apply a unified diff to the workspace. Prefer this over write_file for edits.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"diff": map[string]any{"type": "string"},
			},
			"required": []string{"diff"},
		},
	},
	"run_command": {
		Name:        "run_command",
		Description: "Run an allowlisted command in the workspace. No shell interpolation.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "Allowlisted binary, e.g. go, npm, tsc, git"},
				"args":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"required": []string{"command"},
		},
	},
	"git_diff": {
		Name:        "git_diff",
		Description: "Show the current uncommitted diff.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	"git_status": {
		Name:        "git_status",
		Description: "Show git status --porcelain.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
}

// ResolveTools returns the schemas for the given names, and reports any
// name that is not in the catalog. Empty missing means fully resolved.
func ResolveTools(names []string) ([]ToolSchema, []string) {
	out := make([]ToolSchema, 0, len(names))
	var missing []string
	for _, n := range names {
		if s, ok := ToolCatalog[n]; ok {
			out = append(out, s)
		} else {
			missing = append(missing, n)
		}
	}
	return out, missing
}
