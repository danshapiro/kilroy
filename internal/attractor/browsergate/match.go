package browsergate

import "strings"

var verificationTokens = []string{
	"playwright test",
	"cypress run",
	"selenium",
	"webdriver",
	"npm run e2e",
	"npm run test:e2e",
	"pnpm test:e2e",
	"pnpm run e2e",
	"yarn test:e2e",
	"yarn run e2e",
	"make e2e",
	"make ui-test",
}

var setupTokens = []string{
	"npm ci",
	"npm install",
	"pnpm install",
	"yarn install",
	"npx playwright install",
	"playwright install",
	"apt-get install",
	"apt install",
	"brew install",
	"pip install",
	"pip3 install",
}

var verifyKeywords = []string{"verify", "validate", "check", "test"}
var browserKeywords = []string{"browser", "e2e", "ui"}

func IsBrowserVerificationNode(command, nodeID, nodeLabel string, attrs map[string]string) bool {
	if boolAttr(attrs, "collect_browser_artifacts") {
		return true
	}
	if IsBrowserSetupCommand(command) {
		return false
	}
	if containsAnyToken(normalizeCommand(command), verificationTokens) {
		return true
	}
	intent := normalizeNodeIntent(nodeID + " " + nodeLabel)
	return containsAnyToken(intent, browserKeywords) && containsAnyToken(intent, verifyKeywords)
}

func IsBrowserSetupCommand(command string) bool {
	cmd := normalizeCommand(command)
	if cmd == "" {
		return false
	}
	if containsAnyToken(cmd, setupTokens) {
		return true
	}
	// Mixed setup+verify commands should still be treated as verification.
	if containsAnyToken(cmd, verificationTokens) {
		return false
	}
	return false
}

func boolAttr(attrs map[string]string, key string) bool {
	if len(attrs) == 0 {
		return false
	}
	raw := strings.ToLower(strings.TrimSpace(attrs[key]))
	switch raw {
	case "1", "t", "true", "y", "yes", "on":
		return true
	default:
		return false
	}
}

func normalizeCommand(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(s)), " "))
}

func normalizeNodeIntent(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	replacer := strings.NewReplacer("-", " ", "_", " ", "/", " ", ".", " ", ":", " ")
	return strings.Join(strings.Fields(replacer.Replace(s)), " ")
}

func containsAnyToken(haystack string, tokens []string) bool {
	for _, token := range tokens {
		if strings.Contains(haystack, token) {
			return true
		}
	}
	return false
}
