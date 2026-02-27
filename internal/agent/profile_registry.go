package agent

import (
	"fmt"
	"strings"
	"sync"
)

var (
	profileFactoriesMu sync.RWMutex
	profileFactories   = map[string]func(string) ProviderProfile{
		"openai":           NewOpenAIProfile,
		"anthropic":        NewAnthropicProfile,
		"google":           NewGeminiProfile,
		"codex-app-server": NewCodexAppServerProfile,
		"codex":            NewCodexAppServerProfile,
	}
)

func RegisterProfileFamily(family string, factory func(string) ProviderProfile) {
	key := strings.ToLower(strings.TrimSpace(family))
	if key == "" || factory == nil {
		return
	}
	profileFactoriesMu.Lock()
	profileFactories[key] = factory
	profileFactoriesMu.Unlock()
}

func NewProfileForFamily(family string, model string) (ProviderProfile, error) {
	key := strings.ToLower(strings.TrimSpace(family))
	profileFactoriesMu.RLock()
	factory, ok := profileFactories[key]
	profileFactoriesMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unsupported profile family: %s", family)
	}
	return factory(model), nil
}
