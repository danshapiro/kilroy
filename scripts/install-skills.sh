#!/usr/bin/env bash
# Install kilroy skills and binary symlinks for local agent discovery.
#
# Idempotent: safe to re-run after every `go build`. All links point back to
# this checkout so edits flow directly from the repo to installed locations.
#
# What this installs:
#
#   Binary:
#     ~/.local/bin/kilroy                         -> <repo>/kilroy
#
#   Workflow package root (stable path for --package):
#     ~/.local/share/kilroy/workflows             -> <repo>/workflows
#
#   Claude Code skills + slash command:
#     ~/.claude/skills/quick-launch               -> <repo>/skills/quick-launch
#     ~/.claude/skills/using-kilroy               -> <repo>/skills/using-kilroy
#     ~/.claude/commands/kilroy-quick.md          -> <repo>/skills/quick-launch/commands/kilroy-quick.md
#
#   Codex skills (codex discovers from ~/.agents/skills/, not ~/.codex/skills/):
#     ~/.agents/skills/quick-launch               -> <repo>/skills/quick-launch
#     ~/.agents/skills/using-kilroy               -> <repo>/skills/using-kilroy
#
#   Opencode skills (user-level opencode config dir):
#     ~/.config/opencode/skills/quick-launch      -> <repo>/skills/quick-launch
#     ~/.config/opencode/skills/using-kilroy      -> <repo>/skills/using-kilroy
#
# Codex and Opencode have no user-level slash-command system, so there is no
# `/kilroy-quick` in those agents — invoke by asking the agent to use the
# quick-launch skill by name.

set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
BINARY="$REPO/kilroy"

if [ ! -x "$BINARY" ]; then
    echo "error: kilroy binary not found or not executable at $BINARY" >&2
    echo "build it first: (cd $REPO && go build -o ./kilroy ./cmd/kilroy)" >&2
    exit 1
fi

say() { printf '  %s\n' "$1"; }

link() {
    local target="$1" linkname="$2"
    mkdir -p "$(dirname "$linkname")"
    ln -sfn "$target" "$linkname"
    say "$linkname -> $target"
}

echo "installing kilroy from $REPO"

echo
echo "binary + workflow root"
link "$BINARY"            "$HOME/.local/bin/kilroy"
link "$REPO/workflows"    "$HOME/.local/share/kilroy/workflows"

echo
echo "claude code"
link "$REPO/skills/quick-launch"                            "$HOME/.claude/skills/quick-launch"
link "$REPO/skills/using-kilroy"                            "$HOME/.claude/skills/using-kilroy"
link "$REPO/skills/quick-launch/commands/kilroy-quick.md"   "$HOME/.claude/commands/kilroy-quick.md"

echo
echo "codex (~/.agents/skills/ is the native discovery path)"
link "$REPO/skills/quick-launch"    "$HOME/.agents/skills/quick-launch"
link "$REPO/skills/using-kilroy"    "$HOME/.agents/skills/using-kilroy"

echo
echo "opencode (user-level)"
link "$REPO/skills/quick-launch"    "$HOME/.config/opencode/skills/quick-launch"
link "$REPO/skills/using-kilroy"    "$HOME/.config/opencode/skills/using-kilroy"

echo
echo "verifying binary on PATH..."
if command -v kilroy >/dev/null 2>&1; then
    say "which kilroy = $(command -v kilroy)"
else
    say "WARNING: ~/.local/bin is not on PATH — add it to your shell profile"
fi

echo
echo "done. invoke with:"
echo "  claude:    /kilroy-quick <task description>"
echo "  codex:     ask codex to use the quick-launch skill — mention it by name"
echo "  opencode:  ask opencode to use the quick-launch skill — mention it by name"
