#!/usr/bin/env python3
"""Live smoke for native Responses passthrough (needs the `openai` package).

Usage: uv run --no-project /opt/local/.venv/bin/python scripts/smoke/responses_smoke.py http://127.0.0.1:8080 <nexus-key> [provider/model ...]

Per backend: one turn, a previous_response_id continuation that must keep context (stateless
backends are expanded by the gateway), a streamed turn, and a function-call loop.
"""
import sys, openai
base, key = sys.argv[1], sys.argv[2]
models = sys.argv[3:] or ["deepseek/deepseek-chat", "ollama/gemma4:e2b", "openrouter/openrouter/free", "amd/DeepSeek-V4.1-Flash"]
oa = openai.OpenAI(base_url=base + "/v1", api_key=key, max_retries=0, timeout=180)
tools = [{"type": "function", "name": "get_time", "description": "current time", "parameters": {"type": "object", "properties": {"tz": {"type": "string"}}, "required": ["tz"]}}]

for model in models:
    try:
        r1 = oa.responses.with_raw_response.create(model=model, input="My name is Zed. Reply with just 'noted'.")
        route = r1.headers.get("x-nexus-route")
        r1 = r1.parse()
        r2 = oa.responses.with_raw_response.create(model=model, previous_response_id=r1.id, input="What is my name? Answer with one word.")
        route2 = r2.headers.get("x-nexus-route")
        r2 = r2.parse()
        kept = "zed" in r2.output_text.lower()
        streamed = "".join(e.delta for e in oa.responses.create(model=model, input="Say hi", stream=True) if e.type == "response.output_text.delta")
        t1 = oa.responses.create(model=model, input="What time is it in UTC? Use the tool.", tools=tools)
        calls = [o for o in t1.output if o.type == "function_call"]
        loop = "no tool call"
        if calls:
            t2 = oa.responses.create(model=model, previous_response_id=t1.id, tools=tools,
                                     input=[{"type": "function_call_output", "call_id": c.call_id, "output": "12:00 UTC"} for c in calls])
            loop = "12" in t2.output_text and "ok" or "missing time: " + t2.output_text[:40]
        ok = kept and streamed.strip() and loop == "ok"
        print(f"{'PASS' if ok else 'FAIL'} {model}: route={route} continuation={route2} context_kept={kept} stream={bool(streamed.strip())} tool_loop={loop}")
    except Exception as e:
        print(f"FAIL {model}: {type(e).__name__}: {str(e)[:200]}")
