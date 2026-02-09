package providerspec

var builtinSpecs = map[string]Spec{
	"openai": {
		Key: "openai",
		API: &APISpec{
			Protocol:           ProtocolOpenAIResponses,
			DefaultBaseURL:     "https://api.openai.com",
			DefaultPath:        "/v1/responses",
			DefaultAPIKeyEnv:   "OPENAI_API_KEY",
			ProviderOptionsKey: "openai",
			ProfileFamily:      "openai",
		},
		CLI: &CLISpec{
			DefaultExecutable:  "codex",
			InvocationTemplate: []string{"exec", "--json", "--sandbox", "workspace-write", "-m", "{{model}}", "-C", "{{worktree}}"},
			PromptMode:         "stdin",
			HelpProbeArgs:      []string{"exec", "--help"},
			CapabilityAll:      []string{"--json", "--sandbox"},
		},
		Failover: []string{"anthropic", "google"},
	},
	"anthropic": {
		Key: "anthropic",
		API: &APISpec{
			Protocol:           ProtocolAnthropicMessages,
			DefaultBaseURL:     "https://api.anthropic.com",
			DefaultPath:        "/v1/messages",
			DefaultAPIKeyEnv:   "ANTHROPIC_API_KEY",
			ProviderOptionsKey: "anthropic",
			ProfileFamily:      "anthropic",
		},
		CLI: &CLISpec{
			DefaultExecutable:  "claude",
			InvocationTemplate: []string{"-p", "--output-format", "stream-json", "--verbose", "--model", "{{model}}", "{{prompt}}"},
			PromptMode:         "arg",
			HelpProbeArgs:      []string{"--help"},
			CapabilityAll:      []string{"--output-format", "stream-json", "--verbose"},
		},
		Failover: []string{"openai", "google"},
	},
	"google": {
		Key:     "google",
		Aliases: []string{"gemini"},
		API: &APISpec{
			Protocol:           ProtocolGoogleGenerateContent,
			DefaultBaseURL:     "https://generativelanguage.googleapis.com",
			DefaultPath:        "/v1beta/models/{model}:generateContent",
			DefaultAPIKeyEnv:   "GEMINI_API_KEY",
			ProviderOptionsKey: "google",
			ProfileFamily:      "google",
		},
		CLI: &CLISpec{
			DefaultExecutable:  "gemini",
			InvocationTemplate: []string{"-p", "--output-format", "stream-json", "--yolo", "--model", "{{model}}", "{{prompt}}"},
			PromptMode:         "arg",
			HelpProbeArgs:      []string{"--help"},
			CapabilityAll:      []string{"--output-format"},
			CapabilityAnyOf:    [][]string{{"--yolo", "--approval-mode"}},
		},
		Failover: []string{"openai", "anthropic"},
	},
	"kimi": {
		Key:     "kimi",
		Aliases: []string{"moonshot"},
		API: &APISpec{
			Protocol:           ProtocolAnthropicMessages,
			DefaultBaseURL:     "https://api.kimi.com/coding",
			DefaultPath:        "/v1/messages",
			DefaultAPIKeyEnv:   "KIMI_API_KEY",
			ProviderOptionsKey: "anthropic",
			ProfileFamily:      "openai",
		},
		Failover: []string{"openai", "zai"},
	},
	"zai": {
		Key:     "zai",
		Aliases: []string{"z-ai", "z.ai"},
		API: &APISpec{
			Protocol:           ProtocolOpenAIChatCompletions,
			DefaultBaseURL:     "https://api.z.ai",
			DefaultPath:        "/api/coding/paas/v4/chat/completions",
			DefaultAPIKeyEnv:   "ZAI_API_KEY",
			ProviderOptionsKey: "zai",
			ProfileFamily:      "openai",
		},
		Failover: []string{"openai", "kimi"},
	},
}

func Builtin(key string) (Spec, bool) {
	s, ok := builtinSpecs[CanonicalProviderKey(key)]
	if !ok {
		return Spec{}, false
	}
	return cloneSpec(s), true
}

func Builtins() map[string]Spec {
	out := make(map[string]Spec, len(builtinSpecs))
	for key, spec := range builtinSpecs {
		out[key] = cloneSpec(spec)
	}
	return out
}

func cloneSpec(in Spec) Spec {
	out := in
	if in.API != nil {
		api := *in.API
		out.API = &api
	}
	if in.CLI != nil {
		cli := *in.CLI
		cli.InvocationTemplate = append([]string{}, in.CLI.InvocationTemplate...)
		cli.HelpProbeArgs = append([]string{}, in.CLI.HelpProbeArgs...)
		cli.CapabilityAll = append([]string{}, in.CLI.CapabilityAll...)
		if len(in.CLI.CapabilityAnyOf) > 0 {
			cli.CapabilityAnyOf = make([][]string, 0, len(in.CLI.CapabilityAnyOf))
			for _, group := range in.CLI.CapabilityAnyOf {
				cli.CapabilityAnyOf = append(cli.CapabilityAnyOf, append([]string{}, group...))
			}
		}
		out.CLI = &cli
	}
	out.Aliases = append([]string{}, in.Aliases...)
	out.Failover = append([]string{}, in.Failover...)
	return out
}
