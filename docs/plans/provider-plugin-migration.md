# Provider Plug-in Migration (Kimi/Z Rollout)

## Overview

Kilroy now resolves providers through runtime provider metadata instead of hard-coded provider switches.

- Built-in providers: `openai`, `anthropic`, `google`, `kimi`, `zai`
- Built-in aliases: `gemini`/`google_ai_studio -> google`, `moonshot`/`moonshotai -> kimi`, `z-ai`/`z.ai -> zai`
- API routing is protocol-based (`openai_responses`, `anthropic_messages`, `google_generate_content`, `openai_chat_completions`)

## Config Changes

`llm.providers.<provider>.backend` remains required (`api|cli`), and API providers now support explicit API settings:

```yaml
llm:
  providers:
    kimi:
      backend: api
      api:
        protocol: anthropic_messages
        api_key_env: KIMI_API_KEY
        base_url: https://api.kimi.com/coding
        path: /v1/messages
        profile_family: openai
    zai:
      backend: api
      api:
        protocol: openai_chat_completions
        api_key_env: ZAI_API_KEY
        base_url: https://api.z.ai
        path: /api/coding/paas/v4/chat/completions
        profile_family: openai
```

Supported `llm.providers.<provider>.api.*` fields:

- `protocol`
- `base_url`
- `path`
- `api_key_env`
- `provider_options_key`
- `profile_family`
- `headers`

## Backward Compatibility

- Existing `openai`, `anthropic`, and `google` provider configs continue to work without adding `api.protocol`; defaults are filled from built-ins.
- Existing provider aliases continue to resolve to canonical provider keys.
- Existing CLI preflight and executable policy behavior remains enforced (`llm.cli_profile`, `--allow-test-shim`, capability probes).

## Behavioral Notes

- `kimi` and `zai` are API-only in this release.
- Default Kimi built-in route targets Kimi Coding Anthropic-compatible messages API.
- Moonshot Open Platform users can still override `kimi.api` to OpenAI chat-completions (`https://api.moonshot.ai`, `/v1/chat/completions`).
- `profile_family` controls agent behavior/tooling profile; provider routing still uses the node/config provider key.
- CLI contracts remain built-in for `openai`, `anthropic`, and `google`.
- Provider/model catalog validation still applies and uses canonical provider keys.
- `--force-model <provider=model>` accepts built-ins `openai`, `anthropic`, `google`, `kimi`, `zai` and their aliases.
- Failover order/profile selection are now driven by runtime provider metadata (with config overrides when provided).
