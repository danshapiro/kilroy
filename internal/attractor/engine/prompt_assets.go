package engine

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"
)

var (
	//go:embed prompts/preflight_probe_one_shot_user.txt
	preflightPromptProbeTextRaw string
	//go:embed prompts/preflight_probe_agent_loop_user.txt
	preflightPromptProbeAgentLoopTextRaw string
	//go:embed prompts/preflight_probe_agent_loop_system.txt
	preflightPromptProbeAgentLoopSystemRaw string
	//go:embed prompts/stage_status_contract_preamble.tmpl
	stageStatusContractPromptPreambleTemplateRaw string
)

var (
	preflightPromptProbeText              = mustEmbeddedPromptText("preflight_probe_one_shot_user", preflightPromptProbeTextRaw)
	preflightPromptProbeAgentLoopText     = mustEmbeddedPromptText("preflight_probe_agent_loop_user", preflightPromptProbeAgentLoopTextRaw)
	preflightPromptProbeAgentLoopSystem   = mustEmbeddedPromptText("preflight_probe_agent_loop_system", preflightPromptProbeAgentLoopSystemRaw)
	stageStatusContractPromptPreambleTmpl = template.Must(
		template.New("stage_status_contract_preamble").Parse(stageStatusContractPromptPreambleTemplateRaw),
	)
)

func mustRenderStageStatusContractPromptPreamble(primaryPath, fallbackPath string) string {
	var buf bytes.Buffer
	err := stageStatusContractPromptPreambleTmpl.Execute(&buf, map[string]string{
		"StageStatusPathEnvKey":         stageStatusPathEnvKey,
		"PrimaryPath":                   primaryPath,
		"StageStatusFallbackPathEnvKey": stageStatusFallbackPathEnvKey,
		"FallbackPath":                  fallbackPath,
	})
	if err != nil {
		panic(fmt.Sprintf("render stage status contract prompt preamble: %v", err))
	}
	text := strings.TrimRight(buf.String(), "\r\n")
	if strings.TrimSpace(text) == "" {
		panic("render stage status contract prompt preamble: empty output")
	}
	return text + "\n"
}

func mustEmbeddedPromptText(name, raw string) string {
	text := strings.TrimRight(raw, "\r\n")
	if strings.TrimSpace(text) == "" {
		panic(fmt.Sprintf("embedded prompt %q is empty", name))
	}
	return text
}
