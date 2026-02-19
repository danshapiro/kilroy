package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/llm"
)

type EnvironmentInfo struct {
	WorkingDir            string
	Platform              string
	OSVersion             string
	Today                 string // YYYY-MM-DD
	KnowledgeCutoff       string // YYYY-MM-DD
	IsGitRepo             bool
	GitBranch             string
	GitModifiedFiles      int
	GitUntrackedFiles     int
	GitRecentCommitTitles []string
}

type ProviderProfile interface {
	ID() string
	Model() string
	ToolDefinitions() []llm.ToolDefinition
	SupportsParallelToolCalls() bool
	ContextWindowSize() int
	ProjectDocFiles() []string
	BuildSystemPrompt(env EnvironmentInfo, docs []ProjectDoc) string
}

type baseProfile struct {
	id            string
	model         string
	parallel      bool
	contextWindow int
	basePrompt    string
	toolDefs      []llm.ToolDefinition
	docFiles      []string
}

func (p *baseProfile) ID() string    { return p.id }
func (p *baseProfile) Model() string { return p.model }
func (p *baseProfile) ToolDefinitions() []llm.ToolDefinition {
	return append([]llm.ToolDefinition{}, p.toolDefs...)
}
func (p *baseProfile) SupportsParallelToolCalls() bool { return p.parallel }
func (p *baseProfile) ContextWindowSize() int          { return p.contextWindow }
func (p *baseProfile) ProjectDocFiles() []string {
	return append([]string{}, p.docFiles...)
}

func (p *baseProfile) BuildSystemPrompt(env EnvironmentInfo, docs []ProjectDoc) string {
	var b strings.Builder

	base := strings.TrimSpace(p.basePrompt)
	if base != "" {
		b.WriteString(base)
		if !strings.HasSuffix(base, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("<environment>\n")
	b.WriteString(fmt.Sprintf("Working directory: %s\n", env.WorkingDir))
	b.WriteString(fmt.Sprintf("Is git repository: %t\n", env.IsGitRepo))
	b.WriteString(fmt.Sprintf("Git branch: %s\n", env.GitBranch))
	b.WriteString(fmt.Sprintf("Platform: %s\n", env.Platform))
	b.WriteString(fmt.Sprintf("OS version: %s\n", env.OSVersion))
	b.WriteString(fmt.Sprintf("Today's date: %s\n", env.Today))
	b.WriteString(fmt.Sprintf("Model: %s\n", p.model))
	b.WriteString(fmt.Sprintf("Knowledge cutoff: %s\n", env.KnowledgeCutoff))
	b.WriteString("</environment>\n\n")

	if env.IsGitRepo {
		b.WriteString("<git>\n")
		b.WriteString(fmt.Sprintf("Branch: %s\n", env.GitBranch))
		b.WriteString(fmt.Sprintf("Modified files: %d\n", env.GitModifiedFiles))
		b.WriteString(fmt.Sprintf("Untracked files: %d\n", env.GitUntrackedFiles))
		if len(env.GitRecentCommitTitles) > 0 {
			b.WriteString("Recent commits:\n")
			for _, c := range env.GitRecentCommitTitles {
				b.WriteString("- " + c + "\n")
			}
		}
		b.WriteString("</git>\n\n")
	}

	b.WriteString("Tools:\n")
	for _, td := range p.toolDefs {
		desc := strings.TrimSpace(td.Description)
		if desc == "" {
			desc = "(no description)"
		}
		b.WriteString(fmt.Sprintf("- %s: %s\n", td.Name, desc))
	}
	b.WriteString("\nTool usage:\n")
	b.WriteString("- Use tools to inspect the codebase before editing.\n")
	b.WriteString("- When editing code, prefer the provider-aligned edit tool for this profile.\n")
	b.WriteString("- After running commands, read errors carefully and fix them.\n")

	for _, d := range docs {
		if strings.TrimSpace(d.Path) == "" {
			continue
		}
		b.WriteString("\n----- BEGIN " + d.Path + " -----\n")
		b.WriteString(d.Content)
		if !strings.HasSuffix(d.Content, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("----- END " + d.Path + " -----\n")
	}
	return b.String()
}

func NewOpenAIProfile(model string) ProviderProfile {
	return &baseProfile{
		id:            "openai",
		model:         strings.TrimSpace(model),
		parallel:      false,
		contextWindow: 128_000,
		basePrompt:    openAIProfileBasePrompt,
		docFiles:      []string{"AGENTS.md", ".codex/instructions.md"},
		toolDefs: []llm.ToolDefinition{
			defReadFile(),
			defApplyPatch(),
			defWriteFile(),
			defShell(),
			defGrep(),
			defGlob(),
			defSpawnAgent(),
			defSendInput(),
			defWait(),
			defCloseAgent(),
		},
	}
}

func NewAnthropicProfile(model string) ProviderProfile {
	return &baseProfile{
		id:            "anthropic",
		model:         strings.TrimSpace(model),
		parallel:      true,
		contextWindow: 200_000,
		basePrompt:    anthropicProfileBasePrompt,
		docFiles:      []string{"CLAUDE.md", "AGENTS.md"},
		toolDefs: []llm.ToolDefinition{
			defReadFile(),
			defWriteFile(),
			defEditFile(),
			defShell(),
			defGrep(),
			defGlob(),
			defSpawnAgent(),
			defSendInput(),
			defWait(),
			defCloseAgent(),
		},
	}
}

func NewGeminiProfile(model string) ProviderProfile {
	return &baseProfile{
		id:            "google",
		model:         strings.TrimSpace(model),
		parallel:      true,
		contextWindow: 128_000,
		basePrompt:    geminiProfileBasePrompt,
		docFiles:      []string{"GEMINI.md", "AGENTS.md"},
		toolDefs: []llm.ToolDefinition{
			defReadFile(),
			defReadManyFiles(),
			defWriteFile(),
			defEditFile(),
			defShell(),
			defGrep(),
			defGlob(),
			defListDir(),
			defSpawnAgent(),
			defSendInput(),
			defWait(),
			defCloseAgent(),
		},
	}
}

func envInfoFromEnv(env ExecutionEnvironment) EnvironmentInfo {
	wd := ""
	plat := ""
	osv := ""
	if env != nil {
		wd = env.WorkingDirectory()
		plat = env.Platform()
		osv = env.OSVersion()
	}
	return EnvironmentInfo{
		WorkingDir:      wd,
		Platform:        plat,
		OSVersion:       osv,
		Today:           time.Now().UTC().Format("2006-01-02"),
		KnowledgeCutoff: "2024-06-01",
	}
}

func defReadFile() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "read_file",
		Description: "Read a file from the filesystem. Returns line-numbered content.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string"},
				"offset":    map[string]any{"type": "integer"},
				"limit":     map[string]any{"type": "integer"},
			},
			"required": []string{"file_path"},
		},
	}
}

func defReadManyFiles() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "read_many_files",
		Description: "Read multiple files from the filesystem. Returns a concatenated, line-numbered output for each file.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"offset":     map[string]any{"type": "integer"},
				"limit":      map[string]any{"type": "integer"},
			},
			"required": []string{"file_paths"},
		},
	}
}

func defWriteFile() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "write_file",
		Description: "Write content to a file. Creates the file and parent directories if needed.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string"},
				"content":   map[string]any{"type": "string"},
			},
			"required": []string{"file_path", "content"},
		},
	}
}

func defListDir() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "list_dir",
		Description: "List directory contents. Depth controls recursion (1 = this directory only).",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"path":  map[string]any{"type": "string"},
				"depth": map[string]any{"type": "integer"},
			},
		},
	}
}

func defEditFile() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "edit_file",
		Description: "Replace an exact string occurrence in a file.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"file_path":   map[string]any{"type": "string"},
				"old_string":  map[string]any{"type": "string"},
				"new_string":  map[string]any{"type": "string"},
				"replace_all": map[string]any{"type": "boolean"},
			},
			"required": []string{"file_path", "old_string", "new_string"},
		},
	}
}

func defShell() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "shell",
		Description: "Execute a shell command. Returns stdout, stderr, and exit code.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"command":     map[string]any{"type": "string"},
				"timeout_ms":  map[string]any{"type": "integer"},
				"description": map[string]any{"type": "string"},
			},
			"required": []string{"command"},
		},
	}
}

func defGrep() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "grep",
		Description: "Search file contents using regex patterns.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"pattern":          map[string]any{"type": "string"},
				"path":             map[string]any{"type": "string"},
				"glob_filter":      map[string]any{"type": "string"},
				"case_insensitive": map[string]any{"type": "boolean"},
				"max_results":      map[string]any{"type": "integer"},
			},
			"required": []string{"pattern"},
		},
	}
}

func defGlob() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "glob",
		Description: "Find files matching a glob pattern.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string"},
				"path":    map[string]any{"type": "string"},
			},
			"required": []string{"pattern"},
		},
	}
}

func defApplyPatch() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "apply_patch",
		Description: "Apply code changes using the v4a patch format.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"patch": map[string]any{"type": "string"},
			},
			"required": []string{"patch"},
		},
	}
}

func defSpawnAgent() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "spawn_agent",
		Description: "Spawn a sub-agent to work on a scoped task.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"task": map[string]any{"type": "string"},
			},
			"required": []string{"task"},
		},
	}
}

func defSendInput() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "send_input",
		Description: "Send input to a sub-agent.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"agent_id": map[string]any{"type": "string"},
				"input":    map[string]any{"type": "string"},
			},
			"required": []string{"agent_id", "input"},
		},
	}
}

func defWait() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "wait",
		Description: "Wait for a sub-agent to finish and return its result.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"agent_id":   map[string]any{"type": "string"},
				"timeout_ms": map[string]any{"type": "integer"},
			},
			"required": []string{"agent_id"},
		},
	}
}

func defCloseAgent() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "close_agent",
		Description: "Close a sub-agent session.",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"agent_id": map[string]any{"type": "string"},
			},
			"required": []string{"agent_id"},
		},
	}
}
