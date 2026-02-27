package engine

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestResolveToolShellPathWith_NonWindowsUsesLookPath(t *testing.T) {
	lookPath := func(name string) (string, error) {
		if name == "bash" {
			return "/usr/bin/bash", nil
		}
		return "", errors.New("not found")
	}
	got := resolveToolShellPathWith("linux", lookPath, func(string) bool { return false })
	if got != "/usr/bin/bash" {
		t.Fatalf("shell path: got %q want %q", got, "/usr/bin/bash")
	}
}

func TestResolveToolShellPathWith_WindowsPrefersGitBashWhenBashIsWSLShim(t *testing.T) {
	lookPath := func(name string) (string, error) {
		switch name {
		case "bash":
			return `C:\Windows\System32\bash.exe`, nil
		case "git":
			return `D:\Tools\Git\cmd\git.exe`, nil
		default:
			return "", errors.New("not found")
		}
	}
	expected := filepath.Clean(`D:\Tools\Git\usr\bin\bash.exe`)
	exists := func(path string) bool {
		return filepath.Clean(path) == expected
	}
	got := resolveToolShellPathWith("windows", lookPath, exists)
	if got != expected {
		t.Fatalf("shell path: got %q want %q", got, expected)
	}
}

func TestResolveToolShellPathWith_WindowsFallsBackToCommonGitBashWhenBashMissing(t *testing.T) {
	lookPath := func(name string) (string, error) {
		return "", errors.New("not found")
	}
	expected := filepath.Clean(`C:\Program Files\Git\bin\bash.exe`)
	exists := func(path string) bool {
		return filepath.Clean(path) == expected
	}
	got := resolveToolShellPathWith("windows", lookPath, exists)
	if got != expected {
		t.Fatalf("shell path: got %q want %q", got, expected)
	}
}
