# Rogue Fast Change Log

## 2026-02-26

- Switched `check_toolchain` routing to explicit success/fail edges and added `toolchain_fail` hard-stop so failed prerequisites abort immediately instead of continuing or reporting false success.
- Removed the ambiguous `check_dod` branch split and forced linear DoD/planning flow: `check_dod -> consolidate_dod -> debate_consolidate`.
- Updated `check_dod` to an audit-only step (no `has_dod/needs_dod` branching contract) so the prompt matches linear fast-path routing.
- Added `prepare_ai_inputs` tool stage to deterministically scaffold missing `.ai` artifacts (`spec`, `definition_of_done`, `plan_final`, and baseline review/log files) before `implement`.
- Added `fix_fmt` auto-format stage before `verify_fmt` to avoid postmortem cycles from trivial formatting-only failures.
- Enabled `auto_status=true` on codergen stages that were repeatedly failing with `missing status.json (auto_status=false)` (`check_dod`, `consolidate_dod`, `debate_consolidate`, `implement`, `verify_fidelity`, `review_consensus`, `postmortem`).
- Simplified implement/verify flow by removing diamond check nodes that depended on status files and routing directly with condition gates between tool stages.
- Kept postmortem recovery but changed non-transient failures to re-enter planning (`debate_consolidate`) instead of hard-exiting the run.
- Removed graph-level `retry_target`/`fallback_retry_target` so stage failures do not silently jump to unrelated nodes (for example, `toolchain_fail -> implement`).
- Minimal rollback: removed `toolchain_fail` hard-stop routing and restored `check_toolchain -> expand_spec` unconditional flow so runs do not die at startup when host toolchain preconditions are missing.
- Minimal stabilization pass: restored explicit toolchain gate routing (`check_toolchain -> expand_spec` on success, fail to `postmortem`), added unconditional fallback edges for conditional-only routing nodes, and removed explicit `$KILROY_STAGE_STATUS_PATH` write instructions from `auto_status=true` codergen prompts.
- Toolchain hardening pass: made `check_toolchain` install-method agnostic by removing `cargo install --list` dependency for `wasm-bindgen-cli`, adding explicit `rustup` presence check, and prepending both `$HOME/.cargo/bin` and `$USERPROFILE/.cargo/bin` to `PATH` in all Rust/WASM tool stages (`check_toolchain`, `fix_fmt`, `verify_fmt`, `verify_build`, `verify_test`) so Windows runs can find user-local Rust tools without shell profile assumptions.

### Run Ops Log (same day)

- Reproduced/fixed codex prompt-probe auth seeding bug in engine (PR #43); prompt probe now passes with seeded `auth.json`/`config.toml`.
- Launched detached real run `rogue-fast-20260226T214843Z`; preflight passed; run failed early due missing CXDB service (`127.0.0.1:9010` unreachable).
- Relaunched with `--no-cxdb` as approved: run `rogue-fast-20260226T215515Z`; failed at `check_toolchain` (`FAIL: cargo not found`) and entered `postmortem`.
- Installed Rustup (`Rustlang.Rustup` 1.28.2), added `wasm32-unknown-unknown` target; `cargo install wasm-pack wasm-bindgen-cli` failed because MSVC linker (`link.exe`) is not present.
- Installed prebuilt `wasm-pack` binary (`v0.14.0`) to `C:\Users\dan\.cargo\bin\wasm-pack.exe` and verified `wasm-pack --version`.
- Relaunched with current dotfile + `--no-cxdb`: run `rogue-fast-20260226T231941Z`; run is currently blocked in `check_toolchain` with `bash` process behavior indicating Windows shell-resolution issues (system `bash.exe`/WSL pathing), not codex auth/CXDB.

### Dependency-order fix (same day)

- Root cause identified: `debate_consolidate` required `.ai/spec.md` and `.ai/definition_of_done.md` before `prepare_ai_inputs` created them.
- Fixed flow ordering in `demo/rogue/rogue-fast.dot`:
  - `consolidate_dod -> prepare_ai_inputs -> debate_consolidate -> implement`
  - removed the incorrect `debate_consolidate -> prepare_ai_inputs` dependency inversion.
- Relaunch attempt `rogue-fast-20260226T235930Z` failed immediately due config mismatch (`missing llm.providers.openai.backend` from `demo/rogue/run.yaml` in this environment).
- Relaunched with prior known-good real/no-CXDB config: `rogue-fast-20260227T002621Z`.
- Polled every 5 minutes (9 polls from `2026-02-26 16:27` to `17:07` PT):
  - Run progressed through `expand_spec`, `check_dod`, `consolidate_dod`, `prepare_ai_inputs`, `debate_consolidate`, and `implement`.
  - This confirms the ordering fix executed as intended (the run no longer fails at `debate_consolidate` due missing `.ai` scaffolding).
  - Run ultimately failed later at `fix_fmt` with deterministic cycle breaker: `/usr/bin/bash: line 1: cd: demo/rogue/rogue-wasm: No such file or directory`.

### Root-cause correction (same day)

- Root cause isolated: `implement` was set to `auto_status=true`, so the engine marked success when the stage finished without writing status, even when the model output explicitly reported `outcome: fail`.
- Corrected `implement` status contract in `demo/rogue/rogue-fast.dot`:
  - set `auto_status=false`
  - restored explicit instruction to write status to `$KILROY_STAGE_STATUS_PATH` with `outcome=success|fail` and failure fields.
- This prevents false-positive routing into `fix_fmt` after a failed implement stage.
