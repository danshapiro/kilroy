## Long Runs (Detached)

For long `attractor run`/`resume` jobs, launch detached so the parent shell/session ending does not kill Kilroy:

```bash
RUN_ROOT=/path/to/run_root
setsid -f bash -lc 'cd /home/user/code/kilroy-wt-state-isolation-watchdog && ./kilroy attractor resume --logs-root "$RUN_ROOT/logs" >> "$RUN_ROOT/resume.out" 2>&1'
```

## Launch Modes: Production vs Test

Use explicit run configs and flags so the mode is unambiguous:

- **Production run (real providers, real cost):**
  - `llm.cli_profile` must be `real`
  - Do **not** use `--allow-test-shim`
  - Example:

```bash
./kilroy attractor run --detach --graph <graph.dot> --config <run_config_real.json> --run-id <run_id> --logs-root <logs_root>
```

- **Test run (fake/shim providers):**
  - `llm.cli_profile` must be `test_shim`
  - Provider executable overrides are expected in config
  - `--allow-test-shim` is required
  - Example:

```bash
./kilroy attractor run --detach --graph <graph.dot> --config <run_config_test_shim.json> --allow-test-shim --run-id <run_id> --logs-root <logs_root>
```

## Production Authorization Rule (Strict)

NEVER start a production run except precisely as the user requested, and only after an explicit user request for that production run. Production runs are expensive.

## Production Runs: Exact Command Only

- For production runs (`llm.cli_profile=real`), execute only the exact command the user explicitly approved.
- Do not change flags, env, config, paths, `--run-id`, `--detach`, or add overrides like `--force-model` unless explicitly approved.
- If the run fails, stop immediately, report the error, and wait for explicit approval of a new exact command.
