package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// internalFlags are flags that are intentionally absent from user-facing
// help text (e.g. for inter-process communication between kilroy processes).
var internalFlags = map[string]bool{
	"--skip-cli-headless-warning": true,
}

// extractFuncBody returns the source text of the named function by tracking
// brace depth from the opening "func funcName(" declaration.
func extractFuncBody(src, funcName string) (string, bool) {
	needle := "func " + funcName + "("
	idx := strings.Index(src, needle)
	if idx == -1 {
		return "", false
	}
	depth := 0
	started := false
	for i := idx; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
			started = true
		case '}':
			depth--
			if started && depth == 0 {
				return src[idx : i+1], true
			}
		}
	}
	return src[idx:], true
}

// caseFlagLineRe matches a `case "--foo"[, "--bar"]*:` arm.
// It deliberately does NOT match `case someVariable:` so internal flags
// represented by named constants are excluded automatically.
var caseFlagLineRe = regexp.MustCompile(`(?m)^\s+case\s+((?:"--[^"]+",?\s*)+):`)

// quotedFlagRe extracts `--flag` values from a string of quoted tokens.
var quotedFlagRe = regexp.MustCompile(`"(--[^"]+)"`)

// parseCaseFlags returns the sorted set of --flag names found in case arms
// within the given function body.
func parseCaseFlags(body string) []string {
	seen := map[string]bool{}
	for _, m := range caseFlagLineRe.FindAllStringSubmatch(body, -1) {
		for _, fm := range quotedFlagRe.FindAllStringSubmatch(m[1], -1) {
			seen[fm[1]] = true
		}
	}
	var out []string
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// usageLineRe matches fmt.Fprintln lines that emit "  kilroy ..." usage text.
var usageLineRe = regexp.MustCompile(`(?m)fmt\.Fprintln\(os\.Stderr,\s+"  kilroy[^"]*"\)`)

// dashFlagRe extracts --flag-name tokens (including hyphens in the name).
var dashFlagRe = regexp.MustCompile(`--([\w-]+)`)

// parseUsageFlags returns the set of --flag names mentioned in usage Fprintln
// lines within the given function body.
func parseUsageFlags(body string) map[string]bool {
	seen := map[string]bool{}
	for _, line := range usageLineRe.FindAllString(body, -1) {
		for _, m := range dashFlagRe.FindAllStringSubmatch(line, -1) {
			seen["--"+m[1]] = true
		}
	}
	return seen
}

// checkDrift asserts that every --flag handled by parserFunc is also mentioned
// in the usage text emitted by usageFunc, within the given source file.
// Both functions must reside in the same file.
func checkDrift(t *testing.T, file, parserFunc, usageFunc string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	src := string(data)

	parserBody, ok := extractFuncBody(src, parserFunc)
	if !ok {
		t.Fatalf("func %s not found in %s", parserFunc, file)
	}
	usageBody, ok := extractFuncBody(src, usageFunc)
	if !ok {
		t.Fatalf("func %s not found in %s", usageFunc, file)
	}

	parserFlags := parseCaseFlags(parserBody)
	usageFlags := parseUsageFlags(usageBody)

	for _, flag := range parserFlags {
		if internalFlags[flag] {
			continue
		}
		if !usageFlags[flag] {
			t.Errorf("%s: %s handles %q but %s does not mention it — add it to the help text",
				file, parserFunc, flag, usageFunc)
		}
	}
}

// TestHelpUsageDrift ensures that every --flag handled by the parser is also
// documented in the corresponding usage/help function.
//
// When you add a new case "--foo": arm to a parser, you MUST also add --foo to
// the usage function — otherwise this test will fail and remind you.
func TestHelpUsageDrift(t *testing.T) {
	t.Run("attractorRun", func(t *testing.T) {
		checkDrift(t, "main.go", "attractorRun", "usage")
	})
	t.Run("attractorRunsList", func(t *testing.T) {
		checkDrift(t, "attractor_runs.go", "attractorRunsList", "runsUsage")
	})
	t.Run("attractorRunsShow", func(t *testing.T) {
		checkDrift(t, "attractor_runs.go", "attractorRunsShow", "runsUsage")
	})
	t.Run("attractorRunsWait", func(t *testing.T) {
		checkDrift(t, "attractor_runs.go", "attractorRunsWait", "runsUsage")
	})
	t.Run("attractorRunsPrune", func(t *testing.T) {
		checkDrift(t, "attractor_runs.go", "attractorRunsPrune", "runsUsage")
	})
}
