# Codex `/v1/responses` Parameter Filtering

For Codex upstream routing, `/v1/responses` runs in internal-adapter mode.
Some OpenAI-compatible request parameters are filtered because the Codex upstream does not support them and would otherwise return `400`.

## Filtered Parameters

- `max_output_tokens`
- `max_completion_tokens`
- `temperature`
- `top_p`
- `service_tier`

## Runtime Transparency

When any request parameter is filtered, the proxy adds response header:

- `X-Nexus-Codex-Filtered-Params`: comma-separated list of removed keys

Example:

```text
X-Nexus-Codex-Filtered-Params: temperature,top_p,max_output_tokens
```

This behavior is intentional and keeps Codex internal API compatibility explicit.
