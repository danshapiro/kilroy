package codexappserver

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

const (
	envCommand      = "CODEX_APP_SERVER_COMMAND"
	envArgs         = "CODEX_APP_SERVER_ARGS"
	envCommandArgs  = "CODEX_APP_SERVER_COMMAND_ARGS"
	envAutoDiscover = "CODEX_APP_SERVER_AUTO_DISCOVER"
)

var (
	getenv          = os.Getenv
	lookPath        = exec.LookPath
	shellArgSplitRE = regexp.MustCompile(`(?:[^\s"']+|"[^"]*"|'[^']*')+`)
)

func parseArgs(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var parsed []string
		if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
			out := make([]string, 0, len(parsed))
			for _, arg := range parsed {
				if strings.TrimSpace(arg) != "" {
					out = append(out, arg)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	parts := shellArgSplitRE.FindAllString(trimmed, -1)
	if len(parts) == 0 {
		return nil
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) >= 2 {
			if (strings.HasPrefix(part, "\"") && strings.HasSuffix(part, "\"")) ||
				(strings.HasPrefix(part, "'") && strings.HasSuffix(part, "'")) {
				part = part[1 : len(part)-1]
			}
		}
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func transportOptionsFromEnv() (TransportOptions, bool) {
	opts := TransportOptions{}
	hasExplicitOverride := false
	if cmd := strings.TrimSpace(getenv(envCommand)); cmd != "" {
		opts.Command = cmd
		hasExplicitOverride = true
	}
	argsRaw := getenv(envArgs)
	if strings.TrimSpace(argsRaw) == "" {
		argsRaw = getenv(envCommandArgs)
	}
	if args := parseArgs(argsRaw); len(args) > 0 {
		opts.Args = args
		hasExplicitOverride = true
	}
	if hasExplicitOverride {
		return opts, true
	}

	// If no explicit overrides are provided, only enable env registration when
	// explicit auto-discovery is enabled and the default codex command is
	// available on PATH.
	if !isTruthyEnvValue(getenv(envAutoDiscover)) {
		return TransportOptions{}, false
	}
	if _, err := lookPath(defaultCommand); err == nil {
		return opts, true
	}
	return TransportOptions{}, false
}

func isTruthyEnvValue(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
