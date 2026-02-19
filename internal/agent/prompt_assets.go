package agent

import (
	_ "embed"
	"fmt"
	"strings"
)

var (
	//go:embed prompts/openai_profile_system.txt
	openAIProfileBasePromptRaw string
	//go:embed prompts/anthropic_profile_system.txt
	anthropicProfileBasePromptRaw string
	//go:embed prompts/gemini_profile_system.txt
	geminiProfileBasePromptRaw string
	//go:embed prompts/loop_detection_steering_user.txt
	loopDetectionSteeringPromptRaw string
)

var (
	openAIProfileBasePrompt     = mustEmbeddedPromptText("openai_profile_system", openAIProfileBasePromptRaw)
	anthropicProfileBasePrompt  = mustEmbeddedPromptText("anthropic_profile_system", anthropicProfileBasePromptRaw)
	geminiProfileBasePrompt     = mustEmbeddedPromptText("gemini_profile_system", geminiProfileBasePromptRaw)
	loopDetectionSteeringPrompt = mustEmbeddedPromptText(
		"loop_detection_steering_user",
		loopDetectionSteeringPromptRaw,
	)
)

func mustEmbeddedPromptText(name, raw string) string {
	text := strings.TrimRight(raw, "\r\n")
	if strings.TrimSpace(text) == "" {
		panic(fmt.Sprintf("embedded prompt %q is empty", name))
	}
	return text
}
