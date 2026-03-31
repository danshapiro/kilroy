package engine

import "strings"

// cliOnlyModelIDs lists models that MUST route through CLI backend regardless
// of provider backend configuration. These models have no API endpoint.
var cliOnlyModelIDs = map[string]bool{
	"gpt-5.3-codex-spark": true,
	"gpt-5.4-spark":       true,
}

// isCLIOnlyModel returns true if the given model ID (with or without provider
// prefix) must be routed exclusively through the CLI backend.
func isCLIOnlyModel(modelID string) bool {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return false
	}
	// Strip provider prefix (e.g. "openai/gpt-5.4-spark" -> "gpt-5.4-spark")
	if i := strings.LastIndex(modelID, "/"); i >= 0 {
		modelID = modelID[i+1:]
	}
	return cliOnlyModelIDs[strings.ToLower(modelID)]
}
