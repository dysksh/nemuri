package agent

import "github.com/nemuri/nemuri/internal/llm"

// AgentResponse is the structured response from Claude.
type AgentResponse struct {
	Type            string       `json:"type"` // "text", "code", "new_repo", "file"
	Content         string       `json:"content,omitempty"`
	Repo            string       `json:"repo,omitempty"`
	RepoDescription string       `json:"repo_description,omitempty"`
	Title           string       `json:"title,omitempty"`
	Description     string       `json:"description,omitempty"`
	Files           []OutputFile `json:"files,omitempty"`
}

// OutputFile represents a file in the response.
// For code/new_repo: Path is the file path in the repo.
// For file: Name is the filename for S3 upload.
type OutputFile struct {
	Path    string `json:"path,omitempty"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content"`
}

// Response type constants.
const (
	ResponseTypeText    = "text"
	ResponseTypeCode    = "code"
	ResponseTypeNewRepo = "new_repo"
	ResponseTypeFile    = "file"
)

const toolName = "deliver_result"

// SystemPrompt instructs Claude on how to behave.
const SystemPrompt = `You are a task automation assistant called Nemuri. You receive requests from users and deliver results by calling the deliver_result tool.

You have access to GitHub repository tools (list_repo_files, read_repo_file) to inspect existing code before making changes. Use these tools when the task involves modifying an existing repository — read the relevant files first to understand the codebase.

Rules:
- When the task involves an existing repository, use list_repo_files and read_repo_file to understand the code before generating changes.
- Once you have gathered enough context, call deliver_result with the appropriate type.
- For code changes to an existing repository, use type "code". Set "repo" to the repository name (without the owner).
- For creating a brand-new repository, use type "new_repo". The repo will be created under the authenticated user's account as a private repository. Do NOT use repo tools for new_repo — there is no existing code to read.
- For non-code file deliverables (documents, reports, configs), use type "file". You MUST include at least one entry in "files" with a non-empty "name" and "content". Use Markdown format (.md) for documents — the system will automatically convert Markdown files to PDF. You CAN produce documents, reports, and any written content this way.
- For questions, explanations, or when no deliverable is needed, use type "text". Do NOT use "text" to ask follow-up questions when the user's intent is clear enough to produce a deliverable.
- Infer the repository name from the user's request context. If truly ambiguous, use type "text" to ask for clarification.
- NEVER perform destructive operations (deleting repos, branches, files, etc.) without explicit confirmation. If a request seems destructive, use type "text" to ask the user to confirm.`

// buildSendOptions returns the SendOptions with all available tools.
func (a *Agent) buildSendOptions() *llm.SendOptions {
	tools := []llm.ToolDefinition{
		{
			Name:        toolName,
			Description: "Deliver the result of the task. Call this tool when you have completed the task.",
			InputSchema: buildDeliverResultSchema(),
		},
	}

	// Add repo tools only when GitHub client is available
	if a.github != nil {
		tools = append(tools,
			llm.ToolDefinition{
				Name:        "list_repo_files",
				Description: "List all files in a GitHub repository. Returns the file tree with paths, types, and sizes.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"repo": map[string]any{
							"type":        "string",
							"description": "Repository name (without owner).",
						},
						"ref": map[string]any{
							"type":        "string",
							"description": "Branch, tag, or commit SHA. Defaults to the repository's default branch.",
						},
					},
					"required": []string{"repo"},
				},
			},
			llm.ToolDefinition{
				Name:        "read_repo_file",
				Description: "Read the content of a file from a GitHub repository.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"repo": map[string]any{
							"type":        "string",
							"description": "Repository name (without owner).",
						},
						"path": map[string]any{
							"type":        "string",
							"description": "File path in the repository (e.g. \"src/main.go\").",
						},
						"ref": map[string]any{
							"type":        "string",
							"description": "Branch, tag, or commit SHA. Defaults to the repository's default branch.",
						},
					},
					"required": []string{"repo", "path"},
				},
			},
		)
	}

	return &llm.SendOptions{
		Tools: tools,
		ToolChoice: &llm.ToolChoice{
			Type: "any",
		},
	}
}

func buildDeliverResultSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"type": map[string]any{
				"type":        "string",
				"enum":        []string{"text", "code", "new_repo", "file"},
				"description": "The type of deliverable.",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Text content. Required for type=text.",
			},
			"repo": map[string]any{
				"type":        "string",
				"description": "Repository name (without owner).",
			},
			"repo_description": map[string]any{
				"type":        "string",
				"description": "Description for newly created repos (type=new_repo only).",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "PR title (type=code or type=new_repo).",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "PR description (type=code or type=new_repo).",
			},
			"files": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "File path in the repo (for code/new_repo).",
						},
						"name": map[string]any{
							"type":        "string",
							"description": "Filename for S3 upload (for file type).",
						},
						"content": map[string]any{
							"type":        "string",
							"description": "File content.",
						},
					},
					"required": []string{"content"},
				},
				"description": "Files to include in the deliverable. Required and must be non-empty for type=code, type=new_repo, and type=file.",
			},
		},
		"required": []string{"type"},
	}
}
