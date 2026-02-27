package llmclient

import (
	"github.com/danshapiro/kilroy/internal/llm"
	_ "github.com/danshapiro/kilroy/internal/llm/providers/anthropic"
	_ "github.com/danshapiro/kilroy/internal/llm/providers/codexappserver"
	_ "github.com/danshapiro/kilroy/internal/llm/providers/google"
	_ "github.com/danshapiro/kilroy/internal/llm/providers/openai"
)

// NewFromEnv registers any provider adapters that can be constructed from environment variables.
// The first successfully registered provider becomes the default provider.
func NewFromEnv() (*llm.Client, error) {
	return llm.NewFromEnv()
}
