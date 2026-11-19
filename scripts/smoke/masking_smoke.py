#!/usr/bin/env python3
"""Live smoke for privacy masking (run against a gateway with NEXUS_MASKING=on).

Usage: uv run --no-project /opt/local/.venv/bin/python scripts/smoke/masking_smoke.py http://127.0.0.1:8080 <nexus-key>

Checks: streamed restore through a translated backend (Gemini), that the vendor only saw a
placeholder (DeepSeek answers about the token it received), restored streamed tool-call arguments
(Anthropic SDK -> DeepSeek Anthropic endpoint), and the preview endpoint. The token is fake.
"""
import sys, json, openai, anthropic, httpx
base, key = sys.argv[1], sys.argv[2]
FAKE = "sk-proj-Q7mZp4Lr9XvT2bNc8WkE1sYdHa6UfJg3Rt5Vw0"
oa = openai.OpenAI(base_url=base + "/v1", api_key=key, max_retries=0)
an = anthropic.Anthropic(base_url=base, api_key=key, max_retries=0)

def check(name, ok, detail):
    print(("PASS " if ok else "FAIL ") + name + ": " + detail)

# 1. Streaming restore (translated to Gemini): client must get the real value back.
raw = oa.chat.completions.with_raw_response.create(model="gemini-2.5-flash", stream=True, messages=[
    {"role": "user", "content": f"Repeat this line exactly, with no other text:\nOPENAI_API_KEY={FAKE}"}])
masked = raw.headers.get("x-nexus-masked")
txt = "".join((c.choices[0].delta.content or "") for c in raw.parse() if c.choices)
check("stream restore via gemini", FAKE in txt and "{{" not in txt, f"x-nexus-masked={masked} reply={txt.strip()[:90]!r}")

# 2. The vendor never saw the value: ask the model about its characters (passthrough to DeepSeek).
r = oa.chat.completions.create(model="deepseek-chat", messages=[{"role": "user", "content":
    f"Here is a token: {FAKE}\nDoes the token you see contain the exact substring Q7mZp4? Answer only YES or NO, then show the token exactly as you see it."}])
ans = r.choices[0].message.content
check("vendor sees a placeholder (deepseek)", ans.strip().upper().startswith("NO"), repr(ans[:140]))

# 3. Tool-call arguments are restored (Anthropic SDK stream -> DeepSeek Anthropic passthrough).
tools = [{"name": "save_secret", "description": "store a credential", "input_schema": {"type": "object", "properties": {"value": {"type": "string"}}, "required": ["value"]}}]
with an.messages.stream(model="deepseek/deepseek-chat", max_tokens=300, tools=tools, tool_choice={"type": "tool", "name": "save_secret"},
                        messages=[{"role": "user", "content": f"Save this credential with the tool: {FAKE}"}]) as s:
    final = s.get_final_message()
args = [b.input for b in final.content if b.type == "tool_use"]
check("tool args restored (anthropic stream)", bool(args) and args[0].get("value") == FAKE, json.dumps(args)[:120])

# 4. Preview endpoint.
pv = httpx.post(base + "/api/gateway/masking/preview", json={"text": f"key={FAKE} mail me at jane.doe@example.org"}).json()
check("preview", FAKE not in json.dumps(pv), json.dumps(pv)[:160])
