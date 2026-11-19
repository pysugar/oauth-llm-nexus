# Release: v1.2.1

## Type

Patch release.

## Fixes

- **Thinking room above small reply limits**: models that think by default count reasoning in the reply limit, so a
  small `max_tokens` could leave no answer (Gemini 2.5 Flash with `max_tokens: 10` streamed nothing and surfaced as
  `502 upstream_incomplete`). When a client names a limit of at most 16384 and sets no reasoning controls, and the
  model thinks by default (Gemini 2.5/3/4, OpenAI o-series, `gpt-5*` except `-chat`, DeepSeek reasoner/R1), the
  gateway sends the limit plus a high effort's thinking budget, capped by the model's max output (models.dev).
  Claude is unchanged (it thinks only when asked).
- A reply that still comes back empty or cut short now names the limit and what to change.

## Verification

```bash
go vet ./...
go test -count=1 -race ./...
make build
```

Live: Gemini 2.5 Flash with `max_tokens: 10` (stream and non-stream) and `gpt-5-mini` with
`max_completion_tokens: 16` answer through the gateway.
