# Anthropic Messages API Reference

> **Status:** Draft
>
> **Verified against official sources on:** 2026-03-23
>
> **Confidence:** Low. This file still contains unverified or speculative details and should not yet be treated as a source of truth.
>
> **Official sources:**
>
> - [Anthropic Messages API](https://docs.anthropic.com/en/api/messages)
> - [Anthropic Messages examples](https://docs.anthropic.com/en/api/messages-examples)
> - [Anthropic computer use](https://docs.anthropic.com/en/docs/build-with-claude/computer-use)
> - [Anthropic text editor tool](https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/text-editor-tool)
> - [Anthropic bash tool](https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/bash-tool)
> - [Anthropic web search tool](https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/web-search-tool)

______________________________________________________________________

## Endpoint

```
POST https://api.anthropic.com/v1/messages
```

### Required Headers

| Header              | Value              |
| ------------------- | ------------------ |
| `x-api-key`         | Anthropic API key  |
| `anthropic-version` | `2023-06-01`       |
| `content-type`      | `application/json` |

______________________________________________________________________

## Request Parameters

### Required

| Parameter    | Type    | Description                                                            |
| ------------ | ------- | ---------------------------------------------------------------------- |
| `model`      | string  | Model ID (e.g. `claude-sonnet-4-20250514`, `claude-opus-4-6-20260217`) |
| `max_tokens` | integer | Maximum output tokens (required, no default)                           |
| `messages`   | array   | Conversation messages with alternating `user`/`assistant` roles        |

### Optional

| Parameter        | Type         | Default | Description                                                                |
| ---------------- | ------------ | ------- | -------------------------------------------------------------------------- |
| `system`         | string/array | —       | System prompt. String or array of content blocks (supports cache_control). |
| `temperature`    | number       | 1.0     | Sampling temperature (0.0–1.0). **Must be 1.0 when thinking is enabled.**  |
| `top_p`          | number       | —       | Nucleus sampling (0.0–1.0). Mutually exclusive with `top_k`.               |
| `top_k`          | integer      | —       | Top-K sampling. Only sample from top K tokens.                             |
| `stop_sequences` | array        | —       | Custom stop sequences (up to several).                                     |
| `stream`         | boolean      | false   | Enable SSE streaming.                                                      |
| `tools`          | array        | —       | Tools available to the model.                                              |
| `tool_choice`    | object       | —       | Tool selection policy.                                                     |
| `metadata`       | object       | —       | `{"user_id": "..."}` for abuse tracking.                                   |
| `thinking`       | object       | —       | Extended thinking configuration (see below).                               |

______________________________________________________________________

## Thinking Configuration

### Adaptive (Recommended for Claude Opus 4.6+)

```json
{
  "thinking": {
    "type": "adaptive",
    "effort": "high"
  }
}
```

| Field     | Type   | Values                                 | Description                                 |
| --------- | ------ | -------------------------------------- | ------------------------------------------- |
| `type`    | string | `"adaptive"`                           | Model decides when/how much to think        |
| `effort`  | string | `"low"`, `"medium"`, `"high"`, `"max"` | Controls thinking depth (default: `"high"`) |
| `display` | string | `"summarized"` (default)               | How thinking content is returned            |

### Enabled (Legacy, deprecated for Opus 4.6)

```json
{
  "thinking": {
    "type": "enabled",
    "budget_tokens": 15000
  }
}
```

| Field           | Type    | Description                                      |
| --------------- | ------- | ------------------------------------------------ |
| `type`          | string  | `"enabled"`                                      |
| `budget_tokens` | integer | Max tokens for thinking (must be < `max_tokens`) |

> **Note:** `budget_tokens` is deprecated on Claude Opus 4.6 and will be removed in a future model release. Use adaptive thinking with `effort` instead.

### Disabled

```json
{
  "thinking": {
    "type": "disabled"
  }
}
```

______________________________________________________________________

## Content Block Types

### Text Block

```json
{"type": "text", "text": "Hello, world!"}
```

### Image Block

```json
{
  "type": "image",
  "source": {
    "type": "base64",
    "media_type": "image/jpeg",
    "data": "<base64>"
  }
}
```

Or URL source:

```json
{
  "type": "image",
  "source": {
    "type": "url",
    "url": "https://..."
  }
}
```

Supported: `image/jpeg`, `image/png`, `image/gif`, `image/webp`.

### Document Block

```json
{
  "type": "document",
  "source": {
    "type": "base64",
    "media_type": "application/pdf",
    "data": "<base64>"
  }
}
```

Also supports `text/plain`, `text/html`, `text/csv`, `application/vnd.openxmlformats-officedocument.*`.

### Tool Use Block (in assistant messages)

```json
{
  "type": "tool_use",
  "id": "toolu_abc123",
  "name": "get_weather",
  "input": {"city": "Paris"}
}
```

### Tool Result Block (in user messages)

```json
{
  "type": "tool_result",
  "tool_use_id": "toolu_abc123",
  "content": "22°C, sunny"
}
```

Content can be a string or array of content blocks (text, image).

### Thinking Block (in assistant messages)

```json
{
  "type": "thinking",
  "thinking": "Let me reason through this...",
  "signature": "<opaque_signature>"
}
```

The `signature` field is required when replaying thinking blocks in multi-turn conversations. The thinking content may be summarized when `display: "summarized"` is used with adaptive thinking.

### Server Tool Use Block

```json
{
  "type": "server_tool_use",
  "id": "srvtoolu_abc123",
  "name": "web_search",
  "input": {"query": "latest news"}
}
```

### Server Tool Result Block

```json
{
  "type": "web_search_tool_result",
  "tool_use_id": "srvtoolu_abc123",
  "content": [...]
}
```

______________________________________________________________________

## Tool Types

### Custom Function Tool

```json
{
  "name": "get_weather",
  "description": "Get current weather for a city",
  "input_schema": {
    "type": "object",
    "properties": {"city": {"type": "string"}},
    "required": ["city"]
  }
}
```

### Computer Use Tool

```json
{
  "type": "computer_20251124",
  "name": "computer",
  "display_width_px": 1024,
  "display_height_px": 768,
  "display_number": 1
}
```

**Versions:**

- `computer_20241022` — original version
- `computer_20250124` — added `hold_key`, `left_mouse_down`, `left_mouse_up`, `scroll`, `triple_click`, `wait`
- `computer_20251124` — latest version

### Text Editor Tool

```json
{
  "type": "text_editor_20250728",
  "name": "str_replace_editor"
}
```

**Versions:**

- `text_editor_20241022` — original
- `text_editor_20250124` — updated
- `text_editor_20250429` — deprecated
- `text_editor_20250728` — current recommended

### Bash Tool

```json
{
  "type": "bash_20250124",
  "name": "bash"
}
```

**Versions:**

- `bash_20241022` — original
- `bash_20250124` — current recommended

### Web Search Tool (Server-Side)

```json
{
  "type": "web_search_20250305",
  "name": "web_search",
  "max_uses": 5
}
```

Executes on Anthropic's servers. No beta header required (GA).

______________________________________________________________________

## tool_choice Options

| Value                                 | Description                        |
| ------------------------------------- | ---------------------------------- |
| `{"type": "auto"}`                    | Model decides whether to use tools |
| `{"type": "any"}`                     | Must use at least one tool         |
| `{"type": "none"}`                    | Never use tools                    |
| `{"type": "tool", "name": "fn_name"}` | Must use the specified tool        |

______________________________________________________________________

## Response Object

```json
{
  "id": "msg_abc123",
  "type": "message",
  "role": "assistant",
  "model": "claude-sonnet-4-20250514",
  "content": [
    {"type": "text", "text": "Hello!"}
  ],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {
    "input_tokens": 10,
    "output_tokens": 20,
    "cache_creation_input_tokens": 0,
    "cache_read_input_tokens": 0
  }
}
```

______________________________________________________________________

## stop_reason Values

| Value           | Description                  |
| --------------- | ---------------------------- |
| `end_turn`      | Model finished naturally     |
| `max_tokens`    | Hit `max_tokens` limit       |
| `stop_sequence` | Hit a custom stop sequence   |
| `tool_use`      | Model wants to invoke a tool |

______________________________________________________________________

## Usage Fields

| Field                         | Type    | Description                                     |
| ----------------------------- | ------- | ----------------------------------------------- |
| `input_tokens`                | integer | Total input tokens (includes cached)            |
| `output_tokens`               | integer | Total output tokens (includes thinking)         |
| `cache_creation_input_tokens` | integer | Tokens written to cache this request            |
| `cache_read_input_tokens`     | integer | Tokens read from cache (billed at reduced rate) |

> **Note:** Anthropic's `input_tokens` count INCLUDES cached tokens. The cache fields are supplementary information for billing purposes.

______________________________________________________________________

## Streaming Events

When `stream: true`, the API sends SSE events in this order:

### Event Flow

```
message_start →
  content_block_start → content_block_delta* → content_block_stop →
  (repeat for each content block) →
message_delta → message_stop
```

### Event Types

| Event                 | Data Payload                                                                | Description                         |
| --------------------- | --------------------------------------------------------------------------- | ----------------------------------- |
| `message_start`       | `{"type": "message_start", "message": {...}}`                               | Contains Message with empty content |
| `content_block_start` | `{"type": "content_block_start", "index": 0, "content_block": {...}}`       | New content block starting          |
| `content_block_delta` | `{"type": "content_block_delta", "index": 0, "delta": {...}}`               | Incremental content                 |
| `content_block_stop`  | `{"type": "content_block_stop", "index": 0}`                                | Content block finished              |
| `message_delta`       | `{"type": "message_delta", "delta": {"stop_reason": "..."},"usage": {...}}` | Message-level updates               |
| `message_stop`        | `{"type": "message_stop"}`                                                  | Message complete                    |
| `ping`                | `{"type": "ping"}`                                                          | Keep-alive                          |
| `error`               | `{"type": "error", "error": {...}}`                                         | Error occurred                      |

### Delta Types (in `content_block_delta`)

| Delta Type         | Fields         | Description                    |
| ------------------ | -------------- | ------------------------------ |
| `text_delta`       | `text`         | Incremental text content       |
| `input_json_delta` | `partial_json` | Incremental tool input JSON    |
| `thinking_delta`   | `thinking`     | Incremental thinking content   |
| `signature_delta`  | `signature`    | Incremental thinking signature |

______________________________________________________________________

## Prompt Caching

Add `cache_control` to content blocks:

```json
{
  "type": "text",
  "text": "Long reference document...",
  "cache_control": {"type": "ephemeral"}
}
```

Cache breakpoints can be placed on:

- System prompt blocks
- User message content blocks
- Tool definitions (on the last tool in the array)

Cache TTL: 5 minutes, extended on each hit.

______________________________________________________________________

## Context Window Sizes

| Model             | Context Window |
| ----------------- | -------------- |
| Claude Opus 4.6   | 200K tokens    |
| Claude Sonnet 4.6 | 200K tokens    |
| Claude Sonnet 4.5 | 200K tokens    |
| Claude Sonnet 4   | 200K tokens    |
| Claude Haiku 4.5  | 200K tokens    |
| Claude 3.5 Sonnet | 200K tokens    |
