package engine

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

type InputReferenceKind string

const (
	InputReferenceKindPath InputReferenceKind = "path"
	InputReferenceKindGlob InputReferenceKind = "glob"
)

type DiscoveredInputReference struct {
	SourceFile string             `json:"source_file"`
	Matched    string             `json:"matched_token"`
	Pattern    string             `json:"pattern"`
	Kind       InputReferenceKind `json:"kind"`
	Confidence string             `json:"confidence"`
}

type InputReferenceScanner interface {
	Scan(sourceFile string, content []byte) []DiscoveredInputReference
}

type deterministicInputReferenceScanner struct{}

var (
	markdownLinkRE      = regexp.MustCompile(`\[[^\]]+\]\(([^)\n]+)\)`)
	doubleQuotedTokenRE = regexp.MustCompile(`"([^"\n]+)"`)
	singleQuotedTokenRE = regexp.MustCompile(`'([^'\n]+)'`)
	backtickTokenRE     = regexp.MustCompile("`([^`\\n]+)`")
	windowsAbsPathRE    = regexp.MustCompile(`^[A-Za-z]:[\\/].*`)
)

func (deterministicInputReferenceScanner) Scan(sourceFile string, content []byte) []DiscoveredInputReference {
	text := string(content)
	seen := map[string]bool{}
	out := make([]DiscoveredInputReference, 0, 8)

	appendCandidate := func(token string, structured bool) {
		normalized := normalizeReferenceToken(token)
		if normalized == "" {
			return
		}
		if structured {
			if !looksLikeStructuredReferenceToken(normalized) {
				return
			}
		} else if !looksLikeReferenceToken(normalized) {
			return
		}
		kind := classifyReferenceKind(normalized)
		key := strings.ToLower(strings.TrimSpace(normalized)) + "|" + string(kind)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, DiscoveredInputReference{
			SourceFile: strings.TrimSpace(sourceFile),
			Matched:    token,
			Pattern:    normalized,
			Kind:       kind,
			Confidence: "explicit",
		})
	}

	for _, m := range markdownLinkRE.FindAllStringSubmatch(text, -1) {
		if len(m) < 2 {
			continue
		}
		appendCandidate(extractMarkdownLinkTarget(m[1]), true)
	}
	for _, m := range doubleQuotedTokenRE.FindAllStringSubmatch(text, -1) {
		if len(m) < 2 {
			continue
		}
		appendCandidate(m[1], true)
	}
	for _, m := range singleQuotedTokenRE.FindAllStringSubmatch(text, -1) {
		if len(m) < 2 {
			continue
		}
		appendCandidate(m[1], true)
	}
	for _, m := range backtickTokenRE.FindAllStringSubmatch(text, -1) {
		if len(m) < 2 {
			continue
		}
		appendCandidate(m[1], true)
	}

	for _, field := range strings.Fields(text) {
		appendCandidate(field, false)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pattern != out[j].Pattern {
			return out[i].Pattern < out[j].Pattern
		}
		return out[i].SourceFile < out[j].SourceFile
	})
	return out
}

func extractMarkdownLinkTarget(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if idx := strings.IndexAny(raw, " \t"); idx > 0 {
		raw = raw[:idx]
	}
	return raw
}

func normalizeReferenceToken(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.Trim(s, "\"'`<>()[]{}")
	s = strings.TrimRight(s, ".,;:")
	s = strings.TrimPrefix(s, "file://")
	s = strings.ReplaceAll(s, "\\", "/")
	if s == "" {
		return ""
	}
	if windowsAbsPathRE.MatchString(s) || strings.Contains(s, "*") {
		return s
	}
	if strings.HasPrefix(s, "~"+"/") {
		return s
	}
	cleaned := path.Clean(s)
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func looksLikeReferenceToken(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	lower := strings.ToLower(token)
	if strings.Contains(lower, "://") {
		return false
	}
	if strings.HasPrefix(lower, "mailto:") {
		return false
	}
	if strings.HasPrefix(token, "$") {
		return false
	}
	if strings.Contains(token, "](") {
		return false
	}
	if strings.ContainsAny(token, "<>|") {
		return false
	}
	// Reject tokens with spaces - they are natural language, not paths/globs.
	if strings.ContainsAny(token, " \t") {
		return false
	}
	if strings.Contains(token, "/") || strings.Contains(token, "\\") {
		return true
	}
	if windowsAbsPathRE.MatchString(token) {
		return true
	}
	if looksLikeGlobPattern(token) {
		return true
	}
	return false
}

func looksLikeStructuredReferenceToken(token string) bool {
	if looksLikeReferenceToken(token) {
		return true
	}
	if strings.Contains(token, "](") || strings.ContainsAny(token, "<>|") {
		return false
	}
	if looksLikeGlobPattern(token) {
		return true
	}
	// Structured captures (markdown links/quoted tokens) may contain local file
	// names without slashes (for example "b.md").
	if strings.Contains(token, ".") && !strings.Contains(token, "://") {
		return true
	}
	return false
}

func classifyReferenceKind(pattern string) InputReferenceKind {
	if looksLikeGlobPattern(pattern) {
		return InputReferenceKindGlob
	}
	return InputReferenceKindPath
}

// looksLikeGlobPattern returns true if token contains valid glob metacharacters.
// Tokens containing * or ? are always treated as globs. Tokens containing [
// are only treated as globs if the bracket forms a valid character class (i.e.
// has a matching ]) AND the token also has path-like context (a path separator,
// * or ?) - this prevents natural language fragments like
// "DEFAULT_TOOL_LIMITS[tool_name" and programming constructs like
// "map[string]any" from being classified as globs.
func looksLikeGlobPattern(token string) bool {
	if strings.ContainsAny(token, "*?") {
		return true
	}
	if !strings.Contains(token, "[") {
		return false
	}
	// Validate that every [ has a matching ] (valid character class).
	hasValidBracket := false
	for i := 0; i < len(token); i++ {
		if token[i] == '[' {
			j := strings.IndexByte(token[i+1:], ']')
			if j < 0 {
				// Unmatched [ - not a valid glob.
				return false
			}
			hasValidBracket = true
			// Skip past the matched ]
			i += j + 1
		}
	}
	if !hasValidBracket {
		return false
	}
	// Brackets alone (e.g. "map[string]any") are not enough to be a glob.
	// Require a path separator to disambiguate from programming constructs.
	return strings.ContainsAny(token, "/\\")
}
