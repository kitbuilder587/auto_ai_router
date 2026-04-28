#!/usr/bin/env python3
"""
Image + prompt caching example.

Usage:
  python3 examples/example_image_cache.py [image_path_or_url]

If no argument given, downloads a small public test image.
Runs the same request twice to verify:
  - Run 1: cache_creation_input_tokens > 0
  - Run 2: cache_read_input_tokens > 0

Check server stderr for the raw Anthropic token breakdown.
"""

import base64
import sys
from pathlib import Path

from openai import OpenAI

client = OpenAI(
    api_key="sk-your-master-key-here",
    base_url="http://localhost:8080/v1",
)

# Long system prompt to trigger prompt caching (needs >1024 tokens).
SYSTEM_PROMPT = (
    "You are an expert image analyst with deep knowledge of visual content. "
    + (
        "Always describe images in detail, noting colors, shapes, objects, "
        "text, and any relevant context. Be precise and thorough. "
    ) * 60
)

# Model that supports both images and prompt caching on Bedrock.
MODEL = "sonnet-4-5"


def load_image(source: str) -> dict:
    """Return an OpenAI image_url content block from a path or URL."""
    if source.startswith("http://") or source.startswith("https://"):
        return {
            "type": "image_url",
            "image_url": {"url": source},
        }

    data = Path(source).read_bytes()
    ext = Path(source).suffix.lstrip(".").lower()
    mime = {"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png",
            "gif": "image/gif", "webp": "image/webp"}.get(ext, "image/jpeg")
    b64 = base64.b64encode(data).decode()
    return {
        "type": "image_url",
        "image_url": {"url": f"data:{mime};base64,{b64}"},
    }


def download_test_image() -> str:
    """Download a small public domain test image and return its local path."""
    path = "/tmp/test_image.jpg"
    if not Path(path).exists():
        # 1x1 white JPEG — minimal valid image for testing
        white_1x1_jpg = (
            b"\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00"
            b"\xff\xdb\x00C\x00\x08\x06\x06\x07\x06\x05\x08\x07\x07\x07\t\t"
            b"\x08\n\x0c\x14\r\x0c\x0b\x0b\x0c\x19\x12\x13\x0f\x14\x1d\x1a"
            b"\x1f\x1e\x1d\x1a\x1c\x1c $.' \",#\x1c\x1c(7),01444\x1f'9=82<.342\x1e"
            b"\xff\xc0\x00\x0b\x08\x00\x01\x00\x01\x01\x01\x11\x00\xff\xc4\x00"
            b"\x1f\x00\x00\x01\x05\x01\x01\x01\x01\x01\x01\x00\x00\x00\x00\x00"
            b"\x00\x00\x00\x01\x02\x03\x04\x05\x06\x07\x08\t\n\x0b\xff\xc4\x00"
            b"\xb5\x10\x00\x02\x01\x03\x03\x02\x04\x03\x05\x05\x04\x04\x00\x00"
            b"\x01}\x01\x02\x03\x00\x04\x11\x05\x12!1A\x06\x13Qa\x07\"q\x142\x81"
            b"\x91\xa1\x08#B\xb1\xc1\x15R\xd1\xf0$3br\x82\t\n\x16\x17\x18\x19"
            b"\x1a%&'()*456789:CDEFGHIJSTUVWXYZcdefghijstuvwxyz\x83\x84\x85\x86"
            b"\x87\x88\x89\x8a\x92\x93\x94\x95\x96\x97\x98\x99\x9a\xa2\xa3\xa4"
            b"\xa5\xa6\xa7\xa8\xa9\xaa\xb2\xb3\xb4\xb5\xb6\xb7\xb8\xb9\xba\xc2"
            b"\xc3\xc4\xc5\xc6\xc7\xc8\xc9\xca\xd2\xd3\xd4\xd5\xd6\xd7\xd8\xd9"
            b"\xda\xe1\xe2\xe3\xe4\xe5\xe6\xe7\xe8\xe9\xea\xf1\xf2\xf3\xf4\xf5"
            b"\xf6\xf7\xf8\xf9\xfa\xff\xda\x00\x08\x01\x01\x00\x00?\x00\xfb\xd3"
            b"\xff\xd9"
        )
        Path(path).write_bytes(white_1x1_jpg)
        print(f"Created minimal test image: {path}")
    return path


# Resolve image source
if len(sys.argv) > 1:
    image_source = sys.argv[1]
else:
    image_source = download_test_image()

print(f"Image source: {image_source}")
print(f"System prompt: ~{len(SYSTEM_PROMPT.split())} words")
print(f"Model: {MODEL}")
print()

image_block = load_image(image_source)

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
        "content": [
            image_block,
            {"type": "text", "text": "What do you see in this image? Describe it briefly."},
        ],
    },
]


def run_request(label: str):
    print(f"--- {label} ---")
    stream = client.chat.completions.create(
        model=MODEL,
        messages=MESSAGES,
        max_tokens=100,
        stream=True,
        stream_options={"include_usage": True},
    )

    response_text = ""
    final_usage = None
    for chunk in stream:
        if chunk.choices and chunk.choices[0].delta.content:
            response_text += chunk.choices[0].delta.content
        if chunk.usage:
            final_usage = chunk.usage

    print(f"  Response: {response_text[:120]!r}")
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
run_request("Request 2 (cache read + image reuse expected)")
