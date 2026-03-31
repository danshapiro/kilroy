package codexappserver

import (
	"errors"
	"testing"
)

func TestParseArgs_JSONAndShellFormats(t *testing.T) {
	if got := parseArgs(`["app-server","--listen","stdio://"]`); len(got) != 3 || got[0] != "app-server" {
		t.Fatalf("json parse args mismatch: %#v", got)
	}
	if got := parseArgs(`app-server --listen "stdio://"`); len(got) != 3 || got[2] != "stdio://" {
		t.Fatalf("shell parse args mismatch: %#v", got)
	}
}

func TestTransportOptionsFromEnv(t *testing.T) {
	origGetenv := getenv
	origLookPath := lookPath
	t.Cleanup(func() {
		getenv = origGetenv
		lookPath = origLookPath
	})
	values := map[string]string{
		envCommand: "codex-bin",
		envArgs:    `app-server --listen stdio://`,
	}
	getenv = func(key string) string { return values[key] }

	opts, ok := transportOptionsFromEnv()
	if !ok {
		t.Fatalf("expected enabled transport options")
	}
	if opts.Command != "codex-bin" {
		t.Fatalf("command: got %q", opts.Command)
	}
	if len(opts.Args) != 3 {
		t.Fatalf("args: %#v", opts.Args)
	}
}

func TestTransportOptionsFromEnv_DisabledWithoutExplicitOverridesOrOptIn(t *testing.T) {
	origGetenv := getenv
	origLookPath := lookPath
	t.Cleanup(func() {
		getenv = origGetenv
		lookPath = origLookPath
	})
	getenv = func(string) string { return "" }
	lookPath = func(string) (string, error) { return "/usr/bin/codex", nil }

	opts, ok := transportOptionsFromEnv()
	if ok {
		t.Fatalf("expected transport options to remain disabled without explicit opt-in")
	}
	if opts.Command != "" {
		t.Fatalf("command: got %q want empty", opts.Command)
	}
	if len(opts.Args) != 0 {
		t.Fatalf("args: %#v", opts.Args)
	}
}

func TestTransportOptionsFromEnv_EnabledWhenAutoDiscoverOptInAndCodexPresent(t *testing.T) {
	origGetenv := getenv
	origLookPath := lookPath
	t.Cleanup(func() {
		getenv = origGetenv
		lookPath = origLookPath
	})
	values := map[string]string{
		envAutoDiscover: "1",
	}
	getenv = func(key string) string { return values[key] }
	lookPath = func(string) (string, error) { return "/usr/bin/codex", nil }

	opts, ok := transportOptionsFromEnv()
	if !ok {
		t.Fatalf("expected transport options enabled with explicit auto-discover opt-in")
	}
	if opts.Command != "" {
		t.Fatalf("command: got %q want empty", opts.Command)
	}
	if len(opts.Args) != 0 {
		t.Fatalf("args: %#v", opts.Args)
	}
}

func TestTransportOptionsFromEnv_DisabledWhenAutoDiscoverOptInButCodexMissing(t *testing.T) {
	origGetenv := getenv
	origLookPath := lookPath
	t.Cleanup(func() {
		getenv = origGetenv
		lookPath = origLookPath
	})
	values := map[string]string{
		envAutoDiscover: "true",
	}
	getenv = func(key string) string { return values[key] }
	lookPath = func(string) (string, error) { return "", errors.New("not found") }

	opts, ok := transportOptionsFromEnv()
	if ok {
		t.Fatalf("expected disabled transport options when codex is unavailable")
	}
	if opts.Command != "" || len(opts.Args) != 0 {
		t.Fatalf("expected empty opts when disabled, got: %+v", opts)
	}
}
