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
	StageStatusContractPromptPreambleTemplateRaw string
	//go:embed prompts/input_materialization_preamble.tmpl
	inputMaterializationPromptPreambleTemplateRaw string
	//go:embed prompts/failure_dossier_preamble.tmpl
	failureDossierPromptPreambleTemplateRaw string
)

var (
	preflightPromptProbeText              = mustEmbeddedPromptText("preflight_probe_one_shot_user", preflightPromptProbeTextRaw)
	preflightPromptProbeAgentLoopText     = mustEmbeddedPromptText("preflight_probe_agent_loop_user", preflightPromptProbeAgentLoopTextRaw)
	preflightPromptProbeAgentLoopSystem   = mustEmbeddedPromptText("preflight_probe_agent_loop_system", preflightPromptProbeAgentLoopSystemRaw)
	StageStatusContractPromptPreambleTmpl = template.Must(
		template.New("stage_status_contract_preamble").Parse(StageStatusContractPromptPreambleTemplateRaw),
	)
	inputMaterializationPromptPreambleTmpl = template.Must(
		template.New("input_materialization_preamble").Parse(inputMaterializationPromptPreambleTemplateRaw),
	)
	failureDossierPromptPreambleTmpl = template.Must(
		template.New("failure_dossier_preamble").Parse(failureDossierPromptPreambleTemplateRaw),
	)
)

func mustRenderStageStatusContractPromptPreamble(primaryPath, fallbackPath string) string {
	var buf bytes.Buffer
	err := StageStatusContractPromptPreambleTmpl.Execute(&buf, map[string]string{
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

func MustRenderInputMaterializationPromptPreamble(manifestPath string) string {
	var buf bytes.Buffer
	err := inputMaterializationPromptPreambleTmpl.Execute(&buf, map[string]string{
		"InputsManifestPathEnvKey": inputsManifestEnvKey,
		"ManifestPath":             strings.TrimSpace(manifestPath),
	})
	if err != nil {
		panic(fmt.Sprintf("render input materialization prompt preamble: %v", err))
	}
	text := strings.TrimRight(buf.String(), "\r\n")
	if strings.TrimSpace(text) == "" {
		panic("render input materialization prompt preamble: empty output")
	}
	return text + "\n"
}

func MustRenderFailureDossierPromptPreamble(worktreePath, logsPath string) string {
	var buf bytes.Buffer
	err := failureDossierPromptPreambleTmpl.Execute(&buf, map[string]string{
		"WorktreePath": strings.TrimSpace(worktreePath),
		"LogsPath":     strings.TrimSpace(logsPath),
	})
	if err != nil {
		panic(fmt.Sprintf("render failure dossier prompt preamble: %v", err))
	}
	text := strings.TrimRight(buf.String(), "\r\n")
	if strings.TrimSpace(text) == "" {
		panic("render failure dossier prompt preamble: empty output")
	}
	return text + "\n"
}
