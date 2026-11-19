#!/usr/bin/env python3
"""SDK-level live smoke for the gateway (needs `openai` and `anthropic` packages).

Usage: uv run --no-project /opt/local/.venv/bin/python scripts/smoke/sdk_smoke.py http://127.0.0.1:8080 <nexus-key>

Covers OpenAI/Anthropic SDK streaming and tool loops through translated and passthrough routes
(Gemini, OpenAI, DeepSeek Chat and DeepSeek's Anthropic endpoint) and Gemini embeddings with the
SDK's default base64 encoding. Model names assume the default presets with keys for those vendors.
"""
import json, sys, time, openai, anthropic
base, key = sys.argv[1], sys.argv[2]
oa = openai.OpenAI(base_url=base + "/v1", api_key=key, max_retries=0)
an = anthropic.Anthropic(base_url=base, api_key=key, max_retries=0)
tools_oa = [{"type": "function", "function": {"name": "get_time", "description": "current time", "parameters": {"type": "object", "properties": {"tz": {"type": "string"}}, "required": ["tz"]}}}]
tools_an = [{"name": "get_time", "description": "current time", "input_schema": {"type": "object", "properties": {"tz": {"type": "string"}}, "required": ["tz"]}}]

def step(name, fn):
    t = time.time()
    try:
        out = fn()
        print(f"PASS {name} ({time.time()-t:.1f}s): {out}")
    except Exception as e:
        print(f"FAIL {name}: {type(e).__name__}: {str(e)[:300]}")

def oa_stream(model):
    txt = "".join((c.choices[0].delta.content or "") for c in oa.chat.completions.create(model=model, stream=True, messages=[{"role": "user", "content": "Say hi in 3 words"}]) if c.choices)
    assert txt.strip(), "empty"
    return txt.strip()[:40]

def oa_tool_loop(model, **kw):
    msgs = [{"role": "user", "content": "What time is it in UTC? Use the tool."}]
    r = oa.chat.completions.create(model=model, messages=msgs, tools=tools_oa, **kw)
    m = r.choices[0].message
    assert m.tool_calls, f"no tool call, finish={r.choices[0].finish_reason}"
    msgs.append(m.model_dump(exclude_none=True))  # SDK drops reasoning_content
    for tc in m.tool_calls:
        msgs.append({"role": "tool", "tool_call_id": tc.id, "content": "12:00 UTC"})
    r2 = oa.chat.completions.create(model=model, messages=msgs, tools=tools_oa, **kw)
    return (r2.choices[0].message.content or "")[:50]

def an_tool_loop(model):
    msgs = [{"role": "user", "content": "What time is it in UTC? Use the tool."}]
    with an.messages.stream(model=model, max_tokens=512, messages=msgs, tools=tools_an) as s:
        final = s.get_final_message()
    uses = [b for b in final.content if b.type == "tool_use"]
    assert uses, f"no tool_use, stop={final.stop_reason}"
    assert final.stop_reason == "tool_use", final.stop_reason
    msgs.append({"role": "assistant", "content": [b.model_dump(exclude_none=True) for b in final.content]})
    msgs.append({"role": "user", "content": [{"type": "tool_result", "tool_use_id": u.id, "content": "12:00 UTC"} for u in uses]})
    r2 = an.messages.create(model=model, max_tokens=256, messages=msgs, tools=tools_an)
    return "".join(b.text for b in r2.content if b.type == "text")[:50]

step("openai-sdk stream -> gemini (translated)", lambda: oa_stream("gemini-2.5-flash"))
step("openai-sdk stream -> openai (passthrough)", lambda: oa_stream("gpt-4o-mini"))
step("openai-sdk tool loop -> deepseek-chat (passthrough, max_completion_tokens)", lambda: oa_tool_loop("deepseek-chat", max_completion_tokens=300))
step("openai-sdk tool loop -> deepseek-reasoner (reasoning_content patch)", lambda: oa_tool_loop("deepseek-reasoner", max_completion_tokens=2000))
step("anthropic-sdk stream tool loop -> gemini (translated)", lambda: an_tool_loop("gemini-2.5-flash"))
step("anthropic-sdk stream tool loop -> deepseek (passthrough anthropic)", lambda: an_tool_loop("deepseek/deepseek-chat"))
step("embeddings gemini base64 (sdk default)", lambda: len(oa.embeddings.create(model="gemini-embedding-001", input=["note"]).data[0].embedding))
