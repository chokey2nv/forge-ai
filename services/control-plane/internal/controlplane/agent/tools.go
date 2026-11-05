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
							"addresses":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Follow-up only: the review threads (T1, T2, …) this task deals with"},
						},
						"required": []string{"id", "title", "assignedAgent", "acceptance", "dependsOn"},
					},
				},
				"notes": map[string]any{"type": "string", "description": "Overall strategy in one or two sentences"},
				"responses": map[string]any{
					"type":        "array",
					"description": "Follow-up only: answers to review threads that need no code change",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"thread": map[string]any{"type": "string", "description": "T1, T2, …"},
							"body":   map[string]any{"type": "string"},
						},
						"required": []string{"thread", "body"},
					},
				},
			},
			"required": []string{"tasks"},
		},
	},
	// Given only to follow-up subtasks that address review threads (Phase 12.4).
	"reply_to_thread": {
		Name:        "reply_to_thread",
		Description: "Reply to a review thread your task addresses: say what you changed, or why not. The reply is posted on the thread once your change is integrated.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"thread": map[string]any{"type": "string", "description": "The thread as your task lists it (T1, T2, …)"},
				"body":   map[string]any{"type": "string"},
			},
			"required": []string{"thread", "body"},
		},
	},
	"report_out_of_scope": {
		Name:        "report_out_of_scope",
		Description: "Report an acceptance criterion you cannot meet: it needs files outside your scope, or data that does not exist. Finish everything else. If review then rejects the change, a person decides what happens instead of it being sent back to you.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"criterion": map[string]any{"type": "string", "description": "The acceptance criterion you cannot meet"},
				"needed":    map[string]any{"type": "string", "description": "What it would take: the files or data that are missing"},
			},
			"required": []string{"criterion", "needed"},
		},
	},
	"submit_plan": {
		Name:        "submit_plan",
		Description: "Submit the implementation plan. Call this when you have enough information about the codebase to describe the fix.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"steps": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"description": map[string]any{"type": "string"},
							"files":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						},
						"required": []string{"description"},
					},
				},
				"risks": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			"required": []string{"steps"},
		},
	},
	"submit_review": {
		Name:        "submit_review",
		Description: "Submit the review verdict. Call this when you have completed the review.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verdict": map[string]any{"type": "string", "enum": []string{"pass", "fail"}},
				"defects": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"severity":    map[string]any{"type": "string", "enum": []string{"critical", "major", "minor"}},
							"description": map[string]any{"type": "string"},
							"location":    map[string]any{"type": "string"},
						},
						"required": []string{"severity", "description"},
					},
				},
				"summary": map[string]any{"type": "string"},
			},
			"required": []string{"verdict", "summary"},
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

// WithThreadReplies adds reply_to_thread for a follow-up subtask that
// addresses review threads (Phase 12.4); other agents never get it.
func WithThreadReplies(tools []ToolSchema, addresses bool) []ToolSchema {
	if !addresses {
		return tools
	}
	for _, t := range tools {
		if t.Name == "reply_to_thread" {
			return tools
		}
	}
	return append(tools, ToolCatalog["reply_to_thread"])
}
