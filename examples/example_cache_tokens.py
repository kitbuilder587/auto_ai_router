#!/usr/bin/env python3
"""
Cache token counting verification.
Sends the same request twice — first creates cache, second reads from it.

Expected server stderr:
  Run 1: cache_creation > 0, cache_read == 0
  Run 2: cache_creation == 0, cache_read > 0

Verify: raw_input + cache_read + cache_creation == prompt_tokens.
"""

from openai import OpenAI

client = OpenAI(
    api_key="sk-your-master-key-here",
    base_url="http://localhost:8080/v1",
)

# Must exceed Anthropic's minimum cacheable size (~1024 tokens for Haiku).
SYSTEM_PROMPT = (
    "You are a helpful assistant specialized in explaining complex topics simply. "
    + ("Always be concise, accurate, and friendly. Provide clear and structured answers. " * 80)
)

MESSAGES = [
    {
        "role": "system",
        "content": [
            {
                "type": "text",
                "text": SYSTEM_PROMPT,
                "cache_control": {"type": "ephemeral"},
            }
        ],
    },
    {
        "role": "user",
        "content": "Say hello in one sentence.",
    },
]

# Haiku 4.5 may not support prompt caching on Bedrock.
# Use Claude 3.7 Sonnet or 3.5 Sonnet v2 which are confirmed in Bedrock docs.
model = "sonnet-4-5"  # change to whatever alias maps to claude-3-7-sonnet or claude-3-5-sonnet-v2
print(f"System prompt: ~{len(SYSTEM_PROMPT.split())} words")
print(f"Model: {model}")
print()


def run_request(label: str):
    print(f"--- {label} ---")
    stream = client.chat.completions.create(
        model=model,
        messages=MESSAGES,
        max_tokens=50,
        stream=True,
        stream_options={"include_usage": True},
    )

    final_usage = None
    for chunk in stream:
        if chunk.usage:
            final_usage = chunk.usage

    if final_usage:
        cached = 0
        if final_usage.prompt_tokens_details:
            cached = final_usage.prompt_tokens_details.cached_tokens or 0
        print(f"  prompt_tokens:     {final_usage.prompt_tokens}")
        print(f"  completion_tokens: {final_usage.completion_tokens}")
        print(f"  total_tokens:      {final_usage.total_tokens}")
        print(f"  cached_tokens:     {cached}")
    print()


run_request("Request 1 (cache creation expected)")
run_request("Request 2 (cache read expected)")
