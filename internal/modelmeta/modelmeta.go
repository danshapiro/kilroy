package modelmeta

import (
	"strconv"
	"strings"
)

func NormalizeProvider(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	switch p {
	case "gemini", "google_ai_studio", "google":
		return "google"
	default:
		return p
	}
}

func ProviderFromModelID(id string) string {
	id = strings.TrimSpace(id)
	parts := strings.SplitN(id, "/", 2)
	if len(parts) < 2 {
		return ""
	}
	return NormalizeProvider(parts[0])
}

func ContainsFold(values []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	for _, v := range values {
		if strings.ToLower(strings.TrimSpace(v)) == target {
			return true
		}
	}
	return false
}

func ParseFloatStringPtr(v string) *float64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil
	}
	return &f
}
