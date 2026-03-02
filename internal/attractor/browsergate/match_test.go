package browsergate

import "testing"

func TestIsBrowserVerificationNode(t *testing.T) {
	cases := []struct {
		name  string
		cmd   string
		id    string
		label string
		attrs map[string]string
		want  bool
	}{
		{
			name: "command token: playwright",
			cmd:  "npx playwright test",
			id:   "verify_ui",
			want: true,
		},
		{
			name: "node id + label intent",
			cmd:  "sh scripts/validate-browser.sh",
			id:   "browser_check",
			want: true,
		},
		{
			name: "setup command excluded",
			cmd:  "npx playwright install --with-deps",
			id:   "setup_browser",
			want: false,
		},
		{
			name:  "explicit collect override",
			cmd:   "sh scripts/validate-browser.sh",
			id:    "verify_assets",
			attrs: map[string]string{"collect_browser_artifacts": "true"},
			want:  true,
		},
		{
			name: "non-browser command",
			cmd:  "go test ./...",
			id:   "verify_unit",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsBrowserVerificationNode(tc.cmd, tc.id, tc.label, tc.attrs)
			if got != tc.want {
				t.Fatalf("IsBrowserVerificationNode(%q, %q) = %v, want %v", tc.cmd, tc.id, got, tc.want)
			}
		})
	}
}

func TestIsBrowserVerificationNode_WrapperCommands(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		{
			name: "bash wrapper with playwright token",
			cmd:  "bash -lc 'npm run e2e -- --project=chromium'",
			want: true,
		},
		{
			name: "sh wrapper with cypress token",
			cmd:  "sh -lc 'pnpm cypress run --browser chrome'",
			want: true,
		},
		{
			name: "wrapper around setup command",
			cmd:  "bash -lc 'npm ci && npx playwright install --with-deps'",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsBrowserVerificationNode(tc.cmd, "verify_browser", "Verify Browser", nil)
			if got != tc.want {
				t.Fatalf("IsBrowserVerificationNode(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestIsBrowserSetupCommand(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		{name: "npm ci", cmd: "npm ci", want: true},
		{name: "playwright install", cmd: "npx playwright install --with-deps", want: true},
		{name: "apt install", cmd: "sudo apt-get install -y xvfb", want: true},
		{name: "pip install", cmd: "pip install selenium", want: true},
		{name: "browser verify command", cmd: "npx playwright test", want: false},
		{name: "generic test command", cmd: "go test ./...", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsBrowserSetupCommand(tc.cmd)
			if got != tc.want {
				t.Fatalf("IsBrowserSetupCommand(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}
