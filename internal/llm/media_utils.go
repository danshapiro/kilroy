package llm

import (
	"encoding/base64"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

func IsLocalPath(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "~"+string(os.PathSeparator))
}

func ExpandTilde(path string) string {
	path = strings.TrimSpace(path)
	if !strings.HasPrefix(path, "~"+string(os.PathSeparator)) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"+string(os.PathSeparator)))
}

func InferMimeTypeFromPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return ""
	}
	mt := mime.TypeByExtension(ext)
	// Drop charset if present (e.g. text/plain; charset=utf-8).
	if i := strings.Index(mt, ";"); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	return strings.TrimSpace(mt)
}

func DataURI(mimeType string, data []byte) string {
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(data))
}
