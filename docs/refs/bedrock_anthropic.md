# AWS Bedrock (Anthropic) API Reference

> **Status:** Draft
>
> **Verified against official sources on:** 2026-03-23
>
> **Confidence:** Low. This file mixes confirmed Bedrock behavior with model and capability claims that still need point-by-point verification.
>
> **Official sources:**
>
> - [Bedrock Anthropic Claude Messages API](https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages.html)
> - [Bedrock InvokeModel](https://docs.aws.amazon.com/bedrock/latest/userguide/bedrock-runtime_example_bedrock-runtime_InvokeModel_AnthropicClaude_section.html)
> - [Bedrock Anthropic request/response details](https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages-request-response.html)
> - [Claude on Amazon Bedrock](https://docs.anthropic.com/en/docs/build-with-claude/claude-on-amazon-bedrock)

______________________________________________________________________

## Endpoints

### Non-Streaming

```
POST https://bedrock-runtime.{REGION}.amazonaws.com/model/{MODEL_ID}/invoke
```

### Streaming

```
POST https://bedrock-runtime.{REGION}.amazonaws.com/model/{MODEL_ID}/invoke-with-response-stream
```

### Common Regions

| Region           | Endpoint Base URL                                      |
| ---------------- | ------------------------------------------------------ |
| `us-east-1`      | `https://bedrock-runtime.us-east-1.amazonaws.com`      |
| `us-west-2`      | `https://bedrock-runtime.us-west-2.amazonaws.com`      |
| `eu-central-1`   | `https://bedrock-runtime.eu-central-1.amazonaws.com`   |
| `eu-west-1`      | `https://bedrock-runtime.eu-west-1.amazonaws.com`      |
| `ap-northeast-1` | `https://bedrock-runtime.ap-northeast-1.amazonaws.com` |

______________________________________________________________________

## Authentication

Bedrock uses AWS Signature V4 authentication (or Bearer token via API gateway). The `Authorization` header follows standard AWS signing.

______________________________________________________________________

## Model IDs

### Direct Model IDs

| Model                | Model ID                                    |
| -------------------- | ------------------------------------------- |
| Claude Opus 4.6      | `anthropic.claude-opus-4-6-20260217-v1:0`   |
| Claude Sonnet 4.6    | `anthropic.claude-sonnet-4-6-20260217-v1:0` |
| Claude Sonnet 4.5    | `anthropic.claude-sonnet-4-5-20250929-v1:0` |
| Claude Sonnet 4      | `anthropic.claude-sonnet-4-20250514-v1:0`   |
| Claude Opus 4        | `anthropic.claude-opus-4-20250514-v1:0`     |
| Claude Haiku 4.5     | `anthropic.claude-haiku-4-5-20251001-v1:0`  |
| Claude 3.5 Sonnet v2 | `anthropic.claude-3-5-sonnet-20241022-v2:0` |
| Claude 3.5 Haiku     | `anthropic.claude-3-5-haiku-20241022-v1:0`  |
| Claude 3 Haiku       | `anthropic.claude-3-haiku-20240307-v1:0`    |

### Cross-Region Inference Profiles

Prefix with region code for cross-region routing:

```
us.anthropic.claude-sonnet-4-20250514-v1:0
us.anthropic.claude-opus-4-6-20260217-v1:0
eu.anthropic.claude-sonnet-4-5-20250929-v1:0
global.anthropic.claude-opus-4-6-v1
```

Prefixes: `us.`, `eu.`, `global.`

______________________________________________________________________

## Request/Response Differences from Direct Anthropic API

### Request Body

The request body is the same as the Anthropic Messages API with these key differences:

| Aspect              | Direct Anthropic API | AWS Bedrock                                                                         |
| ------------------- | -------------------- | ----------------------------------------------------------------------------------- |
| `model` field       | Required in body     | **Not in body** (embedded in endpoint URL)                                          |
| `stream` field      | In body              | **Not in body** (determined by endpoint: `invoke` vs `invoke-with-response-stream`) |
| `anthropic_version` | Header               | **In request body**: `"anthropic_version": "bedrock-2023-05-31"`                    |
| Authentication      | `x-api-key` header   | AWS Signature V4 / Bearer token                                                     |

### Minimal Bedrock Request

```json
{
  "anthropic_version": "bedrock-2023-05-31",
  "max_tokens": 1024,
  "messages": [
    {
      "role": "user",
      "content": "Hello, Claude"
    }
  ]
}
```

### All Supported Parameters

Same as [Anthropic Messages API](./anthropic_messages_api.md) with `anthropic_version` added:

```json
{
  "anthropic_version": "bedrock-2023-05-31",
  "max_tokens": 4096,
  "messages": [...],
  "system": "You are helpful",
  "temperature": 0.7,
  "top_p": 0.9,
  "top_k": 50,
  "stop_sequences": ["\n\nHuman:"],
  "tools": [...],
  "tool_choice": {"type": "auto"},
  "thinking": {"type": "enabled", "budget_tokens": 15000},
  "metadata": {"user_id": "user-123"}
}
```

### Response Body

The response body is identical to the direct Anthropic API:

```json
{
  "id": "msg_abc123",
  "type": "message",
  "role": "assistant",
  "content": [...],
  "model": "claude-sonnet-4-20250514",
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {
    "input_tokens": 10,
    "output_tokens": 50,
    "cache_creation_input_tokens": 0,
    "cache_read_input_tokens": 0
  }
}
```

______________________________________________________________________

## Streaming Differences

### Direct Anthropic API

Uses standard **Server-Sent Events (SSE)**:

```
event: message_start
data: {"type": "message_start", "message": {...}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Hello"}}
```

### AWS Bedrock

Uses **AWS Binary Event Stream** framing protocol:

1. Response is a stream of binary frames
2. Each frame has headers (`:event-type`, `:content-type`, `:message-type`)
3. The payload of each `chunk` event contains a base64-encoded JSON body
4. The JSON body contains the standard Anthropic SSE event data

The binary format requires special decoding (AWS SDK handles this automatically). When proxying, you must decode the binary frames and re-emit as standard SSE events.

### Frame Structure

```
[prelude: 12 bytes]
  total_byte_length (4 bytes)
  headers_byte_length (4 bytes)
  prelude_crc (4 bytes)
[headers: variable]
  :event-type = "chunk"
  :content-type = "application/json"
  :message-type = "event"
[payload: variable]
  {"bytes": "<base64-encoded Anthropic event JSON>"}
[message_crc: 4 bytes]
```

Decoded payload example:

```json
{"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Hello"}}
```

______________________________________________________________________

## Extended Thinking on Bedrock

Same as direct Anthropic API. Supports both legacy `enabled` and adaptive thinking:

```json
{
  "anthropic_version": "bedrock-2023-05-31",
  "max_tokens": 16000,
  "thinking": {
    "type": "enabled",
    "budget_tokens": 10000
  },
  "messages": [...]
}
```

Or adaptive (for Opus 4.6):

```json
{
  "thinking": {
    "type": "adaptive",
    "effort": "high"
  }
}
```

> **Note:** When thinking is enabled, temperature must be 1.0. The API timeout is 60 minutes for Claude 3.7 Sonnet and Claude 4+ models. AWS SDK default timeout is 1 minute, so set `read_timeout` to at least 3600 seconds.

______________________________________________________________________

## 1M Context Window (Beta)

Claude Opus 4.6, Sonnet 4.6, Sonnet 4.5, and Sonnet 4 support 1M-token context on Bedrock. For Sonnet 4.5 and Sonnet 4, this requires the beta header:

```json
{
  "anthropic_version": "bedrock-2023-05-31",
  "anthropic_beta": ["context-1m-2025-08-07"]
}
```

______________________________________________________________________

## Tool Support

All Anthropic tool types are supported on Bedrock:

| Tool Type              | Supported |
| ---------------------- | --------- |
| Custom functions       | Yes       |
| `computer_20251124`    | Yes       |
| `text_editor_20250728` | Yes       |
| `bash_20250124`        | Yes       |
| `web_search_20250305`  | Yes       |

______________________________________________________________________

## Converse API (Alternative)

AWS also offers the **Converse API** as a unified interface across all Bedrock models:

```
POST https://bedrock-runtime.{REGION}.amazonaws.com/model/{MODEL_ID}/converse
POST https://bedrock-runtime.{REGION}.amazonaws.com/model/{MODEL_ID}/converse-stream
```

The Converse API uses a different request/response format that is model-agnostic. AWS recommends it for new applications. However, the auto_ai_router uses the native Anthropic Messages API format via `invoke` / `invoke-with-response-stream` for full feature parity.

______________________________________________________________________

## Parameter Constraints

| Constraint                                    | Applies To                      |
| --------------------------------------------- | ------------------------------- |
| Cannot specify both `temperature` and `top_p` | Claude Sonnet 4.5, Haiku 4.5    |
| Temperature must be 1.0 when thinking enabled | All Claude models with thinking |
| `max_tokens` is required (no default)         | All Claude models               |
| 60-minute API timeout for extended thinking   | Claude 3.7 Sonnet, Claude 4+    |
