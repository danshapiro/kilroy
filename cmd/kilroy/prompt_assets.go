package main

import (
	_ "embed"
	"fmt"
	"strings"
)

var (
	//go:embed prompts/cli_headless_warning.txt
	cliHeadlessWarningPromptRaw string
	cliHeadlessWarningPrompt    = mustEmbeddedPromptText("cli_headless_warning", cliHeadlessWarningPromptRaw)
)

func mustEmbeddedPromptText(name, raw string) string {
	text := strings.TrimRight(raw, "\r\n")
	if strings.TrimSpace(text) == "" {
		panic(fmt.Sprintf("embedded prompt %q is empty", name))
	}
	return text
}
