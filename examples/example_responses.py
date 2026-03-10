#!/usr/bin/env python3
"""
Simple chat example with gpt-oss-120b model
Tests basic question-answer interaction through the proxy
"""

import sys
from openai import OpenAI

# Initialize OpenAI client pointing to local proxy
client = OpenAI(
    api_key="sk-your-master-key-here",
    base_url="http://localhost:8080/v1",
)
model = "gpt-4o-mini"
stream = True
try:
    # Create streaming response
    response = client.responses.create(
        model=model,
        input="Say the word 'stored' once.",
        max_output_tokens=50,
        store=True,
        metadata={"test_tag": "store_retrieve"},
        extra_body={"ttl" : 3600},
        stream=stream,
    )

    print()
    print("-" * 50)

    if not stream:
        print(f"{response.usage=}")
        for item in response.output:
            if item.type == "message":
                for part in item.content:
                    if hasattr(part, "text"):
                        print(part)
    else:
        for event in response:
            event_name = event.__class__.__name__
            to_print = [event_name]
            if event_name == "ResponseTextDeltaEvent":
                to_print.append(f"text: {event.delta}")
            if event_name == "ResponseCompletedEvent":
                to_print.append(f"usage: {event.response.usage}")
            print(*to_print)
    print("-" * 50)


except Exception as e:
    print(f"Error: {e}", file=sys.stderr)
    sys.exit(1)
