#!/usr/bin/env python3
"""End-to-end smoke verification for the multi-backend API aggregation gateway.

Exercises:
- Gateway admin APIs (/api/gateway/providers, /api/gateway/models, /select)
- All 4 unified frontend inference protocols (OpenAI Chat Completions,
  OpenAI Responses, Anthropic Messages, Google Gemini generateContent)
- Optional SSE streaming across all 4 protocols (--stream)
- Header-based backend override (X-Nexus-Provider) and runtime DB switching (--switch-provider)

Uses Python standard library only so no extra dependencies are required.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any


@dataclass
class CheckResult:
    name: str
    ok: bool
    status: int
    detail: str


class GatewayClient:
    """Lightweight HTTP client using urllib for gateway smoke checks."""

    def __init__(
        self,
        base_url: str,
        api_key: str,
        admin_user: str,
        admin_password: str,
        timeout: float,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.admin_user = admin_user
        self.admin_password = admin_password
        self.timeout = timeout

    def _auth_headers(self, admin: bool = False) -> dict[str, str]:
        headers: dict[str, str] = {"Content-Type": "application/json"}
        if admin:
            if self.admin_password:
                token = base64.b64encode(
                    f"{self.admin_user}:{self.admin_password}".encode("utf-8")
                ).decode("ascii")
                headers["Authorization"] = f"Basic {token}"
        elif self.api_key:
            headers["Authorization"] = f"Bearer {self.api_key}"
        return headers

    def request_bytes(
        self,
        method: str,
        path: str,
        payload: dict[str, Any] | None = None,
        extra_headers: dict[str, str] | None = None,
        admin: bool = False,
    ) -> tuple[int, dict[str, str], bytes]:
        url = f"{self.base_url}{path}"
        headers = self._auth_headers(admin=admin)
        if extra_headers:
            headers.update(extra_headers)
        data = None if payload is None else json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                resp_headers = {k.lower(): v for k, v in resp.headers.items()}
                return resp.status, resp_headers, resp.read()
        except urllib.error.HTTPError as exc:
            resp_headers = {k.lower(): v for k, v in exc.headers.items()}
            return exc.code, resp_headers, exc.read()

    def request_json(
        self,
        method: str,
        path: str,
        payload: dict[str, Any] | None = None,
        extra_headers: dict[str, str] | None = None,
        admin: bool = False,
    ) -> tuple[int, dict[str, str], Any]:
        status, headers, raw = self.request_bytes(
            method, path, payload=payload, extra_headers=extra_headers, admin=admin
        )
        if not raw:
            return status, headers, {}
        try:
            return status, headers, json.loads(raw.decode("utf-8"))
        except Exception:
            return status, headers, {"raw": raw.decode("utf-8", errors="replace")}


def format_provenance(headers: dict[str, str]) -> str:
    """Extract X-Nexus-* provenance headers for compact diagnostic reporting."""
    keys = [
        "x-nexus-provider",
        "x-nexus-upstream-model",
        "x-nexus-route",
        "x-nexus-route-source",
    ]
    parts = [f"{k}={headers[k]}" for k in keys if k in headers]
    return " ".join(parts) if parts else "no provenance headers"


def check_sse_payload(raw: bytes, required_substrings: list[str]) -> tuple[bool, str]:
    """Verify SSE frame markers exist in streamed response bytes."""
    text = raw.decode("utf-8", errors="replace")
    if "data:" not in text and "event:" not in text:
        return False, f"expected SSE frames, got: {text[:160]!r}"
    for sub in required_substrings:
        if sub not in text:
            return False, f"missing SSE marker {sub!r} in: {text[:200]!r}"
    return True, f"received {len(raw)} SSE bytes"


def run_admin_checks(
    client: GatewayClient, model: str, switch_provider: str
) -> list[CheckResult]:
    results: list[CheckResult] = []

    status, _, body = client.request_json("GET", "/api/gateway/providers", admin=True)
    providers = body.get("providers", []) if isinstance(body, dict) else []
    ok = status == 200 and isinstance(providers, list) and len(providers) > 0
    results.append(
        CheckResult(
            name="admin.providers",
            ok=ok,
            status=status,
            detail=f"{len(providers)} providers loaded" if ok else f"body={body}",
        )
    )

    status, _, body = client.request_json("GET", "/api/gateway/models", admin=True)
    models = body.get("models", []) if isinstance(body, dict) else []
    ok = status == 200 and isinstance(models, list)
    results.append(
        CheckResult(
            name="admin.models",
            ok=ok,
            status=status,
            detail=f"{len(models)} models indexed" if ok else f"body={body}",
        )
    )

    if switch_provider:
        encoded_model = urllib.parse.quote(model, safe="")
        status, _, body = client.request_json(
            "POST",
            f"/api/gateway/models/{encoded_model}/select",
            payload={
                "provider": switch_provider,
                "target_model": model,
                "clear_cooldown": True,
            },
            admin=True,
        )
        active = body.get("active_provider") if isinstance(body, dict) else ""
        ok = status == 200 and active == switch_provider
        results.append(
            CheckResult(
                name="admin.select",
                ok=ok,
                status=status,
                detail=f"active_provider={active!r}" if ok else f"body={body}",
            )
        )

    return results


def run_inference_checks(
    client: GatewayClient,
    model: str,
    provider: str,
    stream: bool,
) -> list[CheckResult]:
    results: list[CheckResult] = []
    extra_headers: dict[str, str] = {}
    if provider:
        extra_headers["X-Nexus-Provider"] = provider

    prompt = "Reply with the exact word OK."

    # 1. OpenAI Chat Completions
    chat_payload = {
        "model": model,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": 32,
        "stream": False,
    }
    status, hdrs, body = client.request_json(
        "POST", "/v1/chat/completions", payload=chat_payload, extra_headers=extra_headers
    )
    ok = status == 200 and isinstance(body, dict) and bool(body.get("choices"))
    results.append(
        CheckResult(
            name="openai_chat.non_stream",
            ok=ok,
            status=status,
            detail=format_provenance(hdrs) if ok else f"{format_provenance(hdrs)} body={body}",
        )
    )

    if stream:
        chat_stream_payload = {**chat_payload, "stream": True}
        status, hdrs, raw = client.request_bytes(
            "POST", "/v1/chat/completions", payload=chat_stream_payload, extra_headers=extra_headers
        )
        sse_ok, detail = check_sse_payload(raw, ["[DONE]"]) if status == 200 else (False, raw.decode("utf-8", errors="replace")[:160])
        results.append(
            CheckResult(
                name="openai_chat.stream",
                ok=status == 200 and sse_ok,
                status=status,
                detail=f"{detail} ({format_provenance(hdrs)})",
            )
        )

    # 2. OpenAI Responses API
    resp_payload = {
        "model": model,
        "input": prompt,
        "max_output_tokens": 32,
        "stream": False,
    }
    status, hdrs, body = client.request_json(
        "POST", "/v1/responses", payload=resp_payload, extra_headers=extra_headers
    )
    ok = status == 200 and isinstance(body, dict) and bool(body.get("output") or body.get("id"))
    results.append(
        CheckResult(
            name="openai_responses.non_stream",
            ok=ok,
            status=status,
            detail=format_provenance(hdrs) if ok else f"{format_provenance(hdrs)} body={body}",
        )
    )

    if stream:
        resp_stream_payload = {**resp_payload, "stream": True}
        status, hdrs, raw = client.request_bytes(
            "POST", "/v1/responses", payload=resp_stream_payload, extra_headers=extra_headers
        )
        sse_ok, detail = check_sse_payload(raw, ["response."]) if status == 200 else (False, raw.decode("utf-8", errors="replace")[:160])
        results.append(
            CheckResult(
                name="openai_responses.stream",
                ok=status == 200 and sse_ok,
                status=status,
                detail=f"{detail} ({format_provenance(hdrs)})",
            )
        )

    # 3. Anthropic Messages API
    ant_payload = {
        "model": model,
        "max_tokens": 32,
        "messages": [{"role": "user", "content": prompt}],
        "stream": False,
    }
    status, hdrs, body = client.request_json(
        "POST", "/anthropic/v1/messages", payload=ant_payload, extra_headers=extra_headers
    )
    ok = status == 200 and isinstance(body, dict) and bool(body.get("content"))
    results.append(
        CheckResult(
            name="anthropic_messages.non_stream",
            ok=ok,
            status=status,
            detail=format_provenance(hdrs) if ok else f"{format_provenance(hdrs)} body={body}",
        )
    )

    if stream:
        ant_stream_payload = {**ant_payload, "stream": True}
        status, hdrs, raw = client.request_bytes(
            "POST", "/anthropic/v1/messages", payload=ant_stream_payload, extra_headers=extra_headers
        )
        sse_ok, detail = check_sse_payload(raw, ["message_stop"]) if status == 200 else (False, raw.decode("utf-8", errors="replace")[:160])
        results.append(
            CheckResult(
                name="anthropic_messages.stream",
                ok=status == 200 and sse_ok,
                status=status,
                detail=f"{detail} ({format_provenance(hdrs)})",
            )
        )

    # 4. Google Gemini API
    encoded_model = urllib.parse.quote(model, safe="")
    gem_payload = {
        "contents": [{"role": "user", "parts": [{"text": prompt}]}],
        "generationConfig": {"maxOutputTokens": 32},
    }
    status, hdrs, body = client.request_json(
        "POST",
        f"/v1beta/models/{encoded_model}:generateContent",
        payload=gem_payload,
        extra_headers=extra_headers,
    )
    ok = status == 200 and isinstance(body, dict) and bool(body.get("candidates"))
    results.append(
        CheckResult(
            name="gemini.non_stream",
            ok=ok,
            status=status,
            detail=format_provenance(hdrs) if ok else f"{format_provenance(hdrs)} body={body}",
        )
    )

    if stream:
        status, hdrs, raw = client.request_bytes(
            "POST",
            f"/v1beta/models/{encoded_model}:streamGenerateContent?alt=sse",
            payload=gem_payload,
            extra_headers=extra_headers,
        )
        sse_ok, detail = check_sse_payload(raw, ["candidates"]) if status == 200 else (False, raw.decode("utf-8", errors="replace")[:160])
        results.append(
            CheckResult(
                name="gemini.stream",
                ok=status == 200 and sse_ok,
                status=status,
                detail=f"{detail} ({format_provenance(hdrs)})",
            )
        )

    return results


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Smoke test 4 frontend protocols and runtime switching on oauth-llm-nexus."
    )
    parser.add_argument(
        "--base-url",
        default=os.environ.get("NEXUS_BASE_URL", "http://127.0.0.1:8080"),
        help="Gateway server base URL (default: http://127.0.0.1:8080 or NEXUS_BASE_URL)",
    )
    parser.add_argument(
        "--api-key",
        default=os.environ.get("NEXUS_API_KEY", ""),
        help="Client Bearer API key (or NEXUS_API_KEY env var)",
    )
    parser.add_argument(
        "--admin-user",
        default="admin",
        help="Admin HTTP BasicAuth username when NEXUS_ADMIN_PASSWORD is enabled",
    )
    parser.add_argument(
        "--admin-password",
        default=os.environ.get("NEXUS_ADMIN_PASSWORD", ""),
        help="Admin HTTP BasicAuth password (or NEXUS_ADMIN_PASSWORD env var)",
    )
    parser.add_argument(
        "--model",
        default="gpt-4o-mini",
        help="Client model ID sent in smoke requests (default: gpt-4o-mini)",
    )
    parser.add_argument(
        "--provider",
        default="",
        help="Optional backend provider sent via X-Nexus-Provider header",
    )
    parser.add_argument(
        "--switch-provider",
        default="",
        help="Optional provider to select at runtime via /api/gateway/models/{client}/select",
    )
    parser.add_argument(
        "--stream",
        action="store_true",
        help="Also test SSE streaming variants across all 4 protocols",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=30.0,
        help="Per-request HTTP timeout in seconds (default: 30.0)",
    )
    parser.add_argument(
        "--skip-admin",
        action="store_true",
        help="Skip /api/gateway/* admin API checks",
    )
    parser.add_argument(
        "--skip-inference",
        action="store_true",
        help="Skip 4-protocol inference checks",
    )
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    client = GatewayClient(
        base_url=args.base_url,
        api_key=args.api_key,
        admin_user=args.admin_user,
        admin_password=args.admin_password,
        timeout=args.timeout,
    )

    results: list[CheckResult] = []
    if not args.skip_admin:
        results.extend(run_admin_checks(client, args.model, args.switch_provider))
    if not args.skip_inference:
        effective_provider = args.provider or args.switch_provider
        results.extend(
            run_inference_checks(
                client=client,
                model=args.model,
                provider=effective_provider,
                stream=args.stream,
            )
        )

    if not results:
        print("No checks selected (both --skip-admin and --skip-inference specified).")
        return 1

    failed = 0
    for item in results:
        tag = "PASS" if item.ok else "FAIL"
        if not item.ok:
            failed += 1
        print(f"[{tag}] {item.name} (http={item.status}): {item.detail}")

    print(f"\nSummary: {len(results) - failed}/{len(results)} checks passed")
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
