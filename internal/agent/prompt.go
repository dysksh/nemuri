package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nemuri/nemuri/internal/llm"
)

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

// UnmarshalJSON handles cases where the LLM returns files as a string instead of an array.
func (r *AgentResponse) UnmarshalJSON(data []byte) error {
	type Alias AgentResponse
	aux := &struct {
		Files json.RawMessage `json:"files,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(r),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	if len(aux.Files) == 0 || string(aux.Files) == "null" {
		return nil
	}

	// Try as []OutputFile first
	var files []OutputFile
	if err := json.Unmarshal(aux.Files, &files); err == nil {
		r.Files = files
		return nil
	}

	// Fallback: string (LLM sometimes returns files as a JSON string)
	var filesStr string
	if err := json.Unmarshal(aux.Files, &filesStr); err == nil {
		// Try to parse the string as JSON array
		if err := json.Unmarshal([]byte(filesStr), &r.Files); err != nil {
			slog.Warn("files field returned as unparseable string, ignoring",
				"value_prefix", truncate(filesStr, 200),
			)
		}
		return nil
	}

	return fmt.Errorf("files field has unexpected type: %s", string(aux.Files))
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
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
	ResponseTypeText     = "text"
	ResponseTypeCode     = "code"
	ResponseTypeNewRepo  = "new_repo"
	ResponseTypeFile     = "file"
	ResponseTypeQuestion = "question" // internal: agent wants to ask user a question
)

const (
	toolName       = "deliver_result"
	askToolName    = "ask_user_question"
	reviewToolName = "submit_review"

	reviewMaxTokens  = 8192
	rewriteMaxTokens = 16384
)

// GatheringSystemPrompt instructs Claude during the gathering (reading) phase.
const GatheringSystemPrompt = `You are a task automation assistant called Nemuri. You are in the GATHERING phase — your job is to read and understand the codebase before generating output.

Use list_repo_files and read_repo_file to explore the repository and gather information needed to complete the user's request.

When you have gathered enough information, respond with a TEXT message (no tool call) containing:
1. A summary of your findings about the relevant code
2. Your implementation plan
3. A list of file paths that are essential for implementation, formatted as:
   NEEDED_FILES:
   - repo:path/to/file1
   - repo:path/to/file2

Rules:
- Focus on understanding the codebase structure, existing patterns, and dependencies.
- Read all files that are relevant to the task before finishing.
- When you have enough context to implement the task, stop calling tools and write your summary.
- If you need clarification from the user, use ask_user_question.
- For new_repo tasks (creating brand-new repositories), there is no existing code to read. Immediately provide your plan as a text response.
- For file generation tasks (documents, reports, configs, etc.) that do NOT mention a specific repository, treat them as standalone file generation. Do NOT ask the user whether a repository is involved — immediately provide your plan as a text response without calling repo tools.
- For document/report tasks, always use Markdown format (.md). The system automatically converts Markdown files to PDF. Do NOT ask the user about output format (LaTeX, HTML, etc.) — just produce Markdown.
- Infer the repository name from the user's request context. Only use ask_user_question about the repository if the user explicitly references a repository but the name is ambiguous.
- NEVER perform destructive operations without explicit confirmation. Use ask_user_question to confirm.
- Do NOT attempt to generate the final deliverable in this phase. Just gather information and plan.`

// GeneratingSystemPrompt instructs Claude during the generating (output) phase.
const GeneratingSystemPrompt = `You are a task automation assistant called Nemuri. You are in the GENERATING phase.

You have already gathered information about the codebase. Below you will find the original request, your analysis from the gathering phase, and the full content of relevant files.

Call deliver_result with the appropriate type to deliver your output.

Rules:
- For code changes to an existing repository, use type "code". Set "repo" to the repository name (without the owner).
- For creating a brand-new repository, use type "new_repo". The repo will be created under the authenticated user's account as a private repository.
- For non-code file deliverables (documents, reports, configs), use type "file". You MUST include at least one entry in "files" with a non-empty "name" and "content". Use Markdown format (.md) for documents — the system will automatically convert Markdown files to PDF.
- For questions, explanations, or when no deliverable is needed, use type "text".
- Include ALL files that need to be created or modified in the "files" array.
- Output complete file contents — do not use placeholders or partial files.`

// buildGatheringSendOptions returns the SendOptions for the gathering (reading) phase.
// Tools: list_repo_files, read_repo_file, ask_user_question. ToolChoice: auto.
func (a *Agent) buildGatheringSendOptions() *llm.SendOptions {
	tools := []llm.ToolDefinition{
		{
			Name:        askToolName,
			Description: "Ask the user a question. Use this when you need clarification, confirmation, or additional information before proceeding. The user will respond asynchronously.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"question": map[string]any{
						"type":        "string",
						"description": "The question to ask the user. Be specific and concise.",
					},
				},
				"required": []string{"question"},
			},
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
			Type: "auto",
		},
	}
}

// buildGeneratingSendOptions returns the SendOptions for the generating (output) phase.
// Tools: deliver_result only. ToolChoice: forced deliver_result.
func buildGeneratingSendOptions() *llm.SendOptions {
	return &llm.SendOptions{
		Tools: []llm.ToolDefinition{
			{
				Name:        toolName,
				Description: "Deliver the result of the task. Call this tool with your complete implementation.",
				InputSchema: buildDeliverResultSchema(),
			},
		},
		ToolChoice: &llm.ToolChoice{Type: "tool", Name: toolName},
	}
}

// reviewPrompt is the system prompt for the Reviewer.
const reviewPrompt = `You are a code reviewer. You receive generated code files and the original user request, and evaluate the code quality.

Evaluate the code on these dimensions (score 1-10):
- correctness: Does the code correctly implement what was requested? Are there bugs or logic errors?
- security: Are there security vulnerabilities (injection, XSS, hardcoded secrets, etc.)?
- maintainability: Is the code well-structured, readable, and following best practices?
- completeness: Does the code fully address the user's request? Are there missing pieces?

Call the submit_review tool with your evaluation.

Rules:
- Be strict but fair. Only flag real issues, not style preferences.
- If there are no issues, pass an empty issues array.
- Focus on issues that affect functionality, security, or correctness.
- For non-code deliverables (text, documents), evaluate content quality, accuracy, and completeness instead of code metrics.`

// rewritePrompt is the system prompt for the Rewriter.
const rewritePrompt = `You are a code rewriter. You receive generated code, the original user request, and a list of issues found during review. Your job is to fix ONLY the flagged issues — do not refactor, redesign, or add features beyond what's needed to resolve the issues.

You will receive the original output and a review with specific issues. Fix each issue while preserving the overall structure and intent of the code.

Rules:
- Fix ONLY the issues listed in the review. Do not make unrelated changes.
- Preserve file paths, naming conventions, and overall architecture.
- If an issue cannot be fixed without a major redesign, leave a TODO comment explaining why.
- Output the complete corrected files — do not output diffs or partial files.`

// buildReviewSendOptions returns the SendOptions for a review call.
func buildReviewSendOptions() *llm.SendOptions {
	return &llm.SendOptions{
		Tools: []llm.ToolDefinition{
			{
				Name:        reviewToolName,
				Description: "Submit the code review evaluation with scores and issues.",
				InputSchema: buildReviewResultSchema(),
			},
		},
		ToolChoice: &llm.ToolChoice{Type: "tool", Name: reviewToolName},
		MaxTokens:  reviewMaxTokens,
	}
}

// buildRewriteSendOptions returns the SendOptions for a rewrite call.
func buildRewriteSendOptions() *llm.SendOptions {
	return &llm.SendOptions{
		Tools: []llm.ToolDefinition{
			{
				Name:        toolName,
				Description: "Deliver the rewritten result. Use the same type and structure as the original.",
				InputSchema: buildDeliverResultSchema(),
			},
		},
		ToolChoice: &llm.ToolChoice{Type: "tool", Name: toolName},
		MaxTokens:  rewriteMaxTokens,
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

func buildReviewResultSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"scores": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"correctness":     map[string]any{"type": "number", "description": "Score 1-10: Does the code correctly implement what was requested?"},
					"security":        map[string]any{"type": "number", "description": "Score 1-10: Are there security vulnerabilities?"},
					"maintainability": map[string]any{"type": "number", "description": "Score 1-10: Is the code well-structured and readable?"},
					"completeness":    map[string]any{"type": "number", "description": "Score 1-10: Does the code fully address the request?"},
				},
				"required": []string{"correctness", "security", "maintainability", "completeness"},
			},
			"issues": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file":     map[string]any{"type": "string", "description": "Filename where the issue was found."},
						"line":     map[string]any{"type": "string", "description": "Line number or range, if applicable."},
						"severity": map[string]any{"type": "string", "enum": []string{"critical", "major", "minor"}, "description": "Issue severity."},
						"category": map[string]any{"type": "string", "enum": []string{"correctness", "security", "maintainability", "completeness"}, "description": "Issue category."},
						"message":  map[string]any{"type": "string", "description": "Description of the issue."},
					},
					"required": []string{"file", "severity", "category", "message"},
				},
				"description": "List of issues found. Empty array if no issues.",
			},
			"summary": map[string]any{
				"type":        "string",
				"description": "Brief overall assessment of the code quality.",
			},
		},
		"required": []string{"scores", "issues", "summary"},
	}
}

// buildReviewInput creates the user message for a review call.
func buildReviewInput(prompt string, resp *AgentResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Original Request\n\n%s\n\n## Generated Output\n\nType: %s\n", prompt, resp.Type)
	if resp.Title != "" {
		fmt.Fprintf(&b, "Title: %s\n", resp.Title)
	}
	if resp.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", resp.Description)
	}
	b.WriteByte('\n')
	writeFiles(&b, resp.Files)
	return b.String()
}

// buildRewriteInput creates the user message for a rewrite call.
func buildRewriteInput(prompt string, resp *AgentResponse, review *ReviewResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Original Request\n\n%s\n\n## Current Output\n\nType: %s\n", prompt, resp.Type)
	if resp.Repo != "" {
		fmt.Fprintf(&b, "Repo: %s\n", resp.Repo)
	}
	if resp.Title != "" {
		fmt.Fprintf(&b, "Title: %s\n", resp.Title)
	}
	if resp.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", resp.Description)
	}
	b.WriteByte('\n')
	writeFiles(&b, resp.Files)

	reviewJSON, err := json.MarshalIndent(review, "", "  ")
	if err != nil {
		slog.Warn("failed to marshal review for rewrite prompt", "error", err)
		reviewJSON = []byte(review.Summary)
	}
	fmt.Fprintf(&b, "## Review Results\n\n%s\n\nFix the issues listed above. Use the deliver_result tool with the corrected files. Keep the same type, repo, title format.\n", reviewJSON)
	return b.String()
}

// writeFiles appends file content blocks to a strings.Builder.
func writeFiles(b *strings.Builder, files []OutputFile) {
	for _, f := range files {
		name := f.Path
		if name == "" {
			name = f.Name
		}
		fmt.Fprintf(b, "### File: %s\n```\n%s\n```\n\n", name, f.Content)
	}
}
