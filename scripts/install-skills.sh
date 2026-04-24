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
#   Claude Code skills:
#     ~/.claude/skills/using-kilroy               -> <repo>/skills/using-kilroy
#
#   Codex skills (codex discovers from ~/.agents/skills/, not ~/.codex/skills/):
#     ~/.agents/skills/using-kilroy               -> <repo>/skills/using-kilroy
#
#   Opencode skills (user-level opencode config dir):
#     ~/.config/opencode/skills/using-kilroy      -> <repo>/skills/using-kilroy
#
# The `quick-launch` and `pr-review` skills (and their workflow packages) now
# live in the gf-software-factory repo at sibling location
# `gf-software-factory/skills/{quick-launch,pr-review}` and
# `gf-software-factory/workflows/{quick-launch,pr-review}`. Install those via
# `gf-software-factory/scripts/install-kilroy-host.sh`.

set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
BINARY="$REPO/kilroy"

# Always build before installing so the binary on PATH reflects the current
# checkout. Stale binaries are a silent failure mode for skills that depend on
# newer flags (--tmux, --prompt-file, --label, ...). One extra compile at
# install time is cheap insurance.
echo "building kilroy binary..."
(cd "$REPO" && go build -o "$BINARY" ./cmd/kilroy)

if [ ! -x "$BINARY" ]; then
    echo "error: kilroy binary missing after build at $BINARY" >&2
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
link "$REPO/skills/using-kilroy"    "$HOME/.claude/skills/using-kilroy"

echo
echo "codex (~/.agents/skills/ is the native discovery path)"
link "$REPO/skills/using-kilroy"    "$HOME/.agents/skills/using-kilroy"

echo
echo "opencode (user-level)"
link "$REPO/skills/using-kilroy"    "$HOME/.config/opencode/skills/using-kilroy"

echo
echo "verifying binary on PATH..."
if command -v kilroy >/dev/null 2>&1; then
    say "which kilroy = $(command -v kilroy)"
else
    say "WARNING: ~/.local/bin is not on PATH — add it to your shell profile"
fi

echo
echo "done. for the quick-launch and pr-review skills, install them from"
echo "gf-software-factory: gf-software-factory/scripts/install-kilroy-host.sh"
