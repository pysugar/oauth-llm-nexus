# Release Draft: v0.2.18

## Type

Patch release.

## Highlights

- Fix `HTTP 400 INVALID_ARGUMENT` on Claude thinking models via OpenAI `/v1/chat/completions` when routed to Google antigravity upstream.
- Align Claude request mapping semantics with `CLIProxyAPI` on OpenAI path:
  - `developer/system` -> `systemInstruction`
  - `assistant` -> `model`
  - `tool/function` -> `user` + `functionResponse`
- Add Claude-specific tool-call closure handling:
  - two-pass `tool_call_id -> function name` mapping
  - sentinel `skip_thought_signature_validator` fallback when signature is unavailable
- Normalize request part signature field to camelCase `thoughtSignature`.

## API / Behavior Changes

- No external API route changes.
- Affected internal behavior (Claude models only on OpenAI chat path):
  - `contents.role` no longer carries `developer` for Claude path.
  - Tool call / tool result mapping follows Antigravity-compatible structure.

## Files Changed

- `internal/proxy/mappers/openai.go`
- `internal/proxy/mappers/openai_test.go`

## Verification

Go tests:

```bash
GOCACHE=/tmp/go-build-cache go test ./internal/proxy/mappers -v
```

Integration contract regression (`nexus_test`, local `8080`):

```bash
uv run -- python -m pytest nexus_test/tests/test_openai.py nexus_test/tests/test_openai_responses.py nexus_test/tests/test_codex.py -q -rs -m "contract or stream_contract" --test-interval-seconds 1 --retry-429 1 --retry-429-wait-seconds 15
uv run -- python -m pytest nexus_test/tests -q -rs -m "contract or stream_contract" --test-interval-seconds 1 --retry-429 1 --retry-429-wait-seconds 15
```

Observed result:

- `15 passed, 26 deselected`
- `30 passed, 93 deselected`

## Release Steps

1. Merge feature branch to `main`.
2. Create and push tag:

```bash
git tag -a v0.2.18 -m "v0.2.18: fix claude openai antigravity role/tool mapping"
git push origin main
git push origin v0.2.18
```

3. Create GitHub release using this draft.
4. Attach binaries from release workflow artifacts.
