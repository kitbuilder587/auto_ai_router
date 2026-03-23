# OpenAI Responses API Reference

> **Status:** Verified
>
> **Verified against official sources on:** 2026-03-23
>
> **Scope:** Core Responses API request/response schema, tool definitions, retrieve endpoint, and streaming event model.
>
> **Official sources:**
>
> - [OpenAI Responses API](https://platform.openai.com/docs/api-reference/responses/create)
> - [OpenAI Responses API compact reference](https://platform.openai.com/docs/api-reference/responses/compact?api-mode=responses)
> - [OpenAI tools guide](https://platform.openai.com/docs/guides/tools/file-search)
> - [OpenAI file search guide](https://platform.openai.com/docs/guides/tools-file-search/)
> - [OpenAI computer use guide](https://platform.openai.com/docs/guides/tools-computer-use)
> - [OpenAI image generation guide](https://platform.openai.com/docs/guides/tools-image-generation/)

______________________________________________________________________

## Endpoint

```
POST https://api.openai.com/v1/responses
GET  https://api.openai.com/v1/responses/{response_id}
```

______________________________________________________________________

## Request Parameters

### Required

| Parameter | Type         | Description                                                 |
| --------- | ------------ | ----------------------------------------------------------- |
| `model`   | string       | Model ID (e.g. `gpt-4o`, `o3`, `o4-mini`, `gpt-5`)          |
| `input`   | string/array | Text string or array of input items (messages, files, etc.) |

### Optional

| Parameter              | Type       | Default      | Description                                                                                                                       |
| ---------------------- | ---------- | ------------ | --------------------------------------------------------------------------------------------------------------------------------- |
| `instructions`         | string     | —            | Top-level instructions for the response.                                                                                          |
| `tools`                | array      | —            | Tools the model may invoke.                                                                                                       |
| `tool_choice`          | string/obj | `auto`       | `"none"`, `"auto"`, `"required"`, or a specific tool selection object.                                                            |
| `reasoning`            | object     | —            | Reasoning configuration such as \`{"effort":"low"                                                                                 |
| `text`                 | object     | —            | Text output configuration, including structured output via `text.format`.                                                         |
| `truncation`           | string     | `"disabled"` | Truncation strategy. `auto` truncates long conversations; `disabled` returns a 400 if the input exceeds the model context window. |
| `max_output_tokens`    | integer    | —            | Maximum tokens in the response.                                                                                                   |
| `temperature`          | number     | `1`          | Sampling temperature.                                                                                                             |
| `top_p`                | number     | `1`          | Nucleus sampling parameter.                                                                                                       |
| `stream`               | boolean    | `false`      | Enable SSE streaming.                                                                                                             |
| `previous_response_id` | string     | —            | Reference a previous response to continue a conversation server-side.                                                             |
| `store`                | boolean    | `true`       | Store the response for later retrieval.                                                                                           |
| `metadata`             | object     | —            | Request metadata.                                                                                                                 |
| `include`              | array      | —            | Extra fields to include in the response.                                                                                          |
| `user`                 | string     | —            | End-user identifier. OpenAI is moving toward `safety_identifier` and `prompt_cache_key` for newer usage patterns.                 |

______________________________________________________________________

## Input Item Types

The `input` array accepts items of different types:

### Message Items

```json
{
  "type": "message",
  "role": "user",
  "content": "Hello"
}
```

Content can be a string or array of content parts:

```json
{
  "type": "message",
  "role": "user",
  "content": [
    {"type": "input_text", "text": "What's in this image?"},
    {"type": "input_image", "image_url": "https://..."},
    {"type": "input_image", "file_id": "file-abc123"},
    {"type": "input_audio", "data": "<base64>", "format": "wav"},
    {"type": "input_file", "file_id": "file-abc123"}
  ]
}
```

Roles: `user`, `assistant`, `developer` (system-like).

### Function Call Output

```json
{
  "type": "function_call_output",
  "call_id": "call_abc123",
  "output": "{\"temperature\": 22}"
}
```

### Item Reference (from previous response)

```json
{
  "type": "item_reference",
  "id": "item_abc123"
}
```

______________________________________________________________________

## Tool Types

### Function Tool

```json
{
  "type": "function",
  "name": "get_weather",
  "description": "Get current weather",
  "parameters": {"type": "object", "properties": {...}},
  "strict": true
}
```

### Web Search

```json
{"type": "web_search"}
```

Supported forms include `{"type": "web_search"}`, `{"type": "web_search_2025_08_26"}`, `{"type": "web_search_preview"}`, and `{"type": "web_search_preview_2025_03_11"}`.

Optional `search_context_size`: `"low"`, `"medium"` (default), `"high"`.

### File Search

```json
{
  "type": "file_search",
  "vector_store_ids": ["vs_abc123"],
  "max_num_results": 20,
  "ranking_options": {"ranker": "auto", "score_threshold": 0.0}
}
```

### Code Interpreter

```json
{
  "type": "code_interpreter",
  "container": {"type": "auto"}
}
```

### Computer Use

```json
{
  "type": "computer_use_preview",
  "display_width": 1024,
  "display_height": 768,
  "environment": "browser"
}
```

### Image Generation

```json
{
  "type": "image_generation",
  "background": "auto",
  "input_image_mask": null,
  "output_compression": 100,
  "output_format": "png",
  "quality": "high",
  "size": "1024x1024"
}
```

### MCP (Model Context Protocol)

```json
{
  "type": "mcp",
  "server_label": "my-server",
  "server_url": "https://...",
  "allowed_tools": ["tool1", "tool2"]
}
```

______________________________________________________________________

## Output Item Types

The `output` array in the response contains these item types:

| Type                    | Description                                            |
| ----------------------- | ------------------------------------------------------ |
| `message`               | Text/refusal output from the model                     |
| `function_call`         | Tool invocation with `call_id`, `name`, `arguments`    |
| `function_call_output`  | (In multi-turn) Result of a function call              |
| `file_search_call`      | File search invocation and results                     |
| `web_search_call`       | Web search invocation and results                      |
| `computer_call`         | Computer use action                                    |
| `code_interpreter_call` | Code interpreter execution with input/output           |
| `image_generation_call` | Generated image result                                 |
| `reasoning`             | Internal reasoning (for reasoning models with summary) |
| `mcp_call`              | MCP tool invocation                                    |
| `mcp_call_output`       | MCP tool result                                        |

______________________________________________________________________

## Response Object

```json
{
  "id": "resp_abc123",
  "object": "response",
  "created_at": 1711000000,
  "model": "gpt-4o-2024-08-06",
  "status": "completed",
  "output": [...],
  "usage": {
    "input_tokens": 50,
    "output_tokens": 100,
    "total_tokens": 150,
    "input_tokens_details": {
      "cached_tokens": 0
    },
    "output_tokens_details": {
      "reasoning_tokens": 0
    }
  },
  "metadata": {},
  "temperature": 1.0,
  "top_p": 1.0,
  "max_output_tokens": null,
  "truncation": "disabled",
  "tool_choice": "auto",
  "text": {"format": {"type": "text"}},
  "error": null,
  "incomplete_details": null
}
```

### Status Values

| Status        | Description                              |
| ------------- | ---------------------------------------- |
| `in_progress` | Currently generating                     |
| `completed`   | Finished successfully                    |
| `failed`      | Generation failed (see `error`)          |
| `cancelled`   | Cancelled by user                        |
| `incomplete`  | Stopped early (see `incomplete_details`) |

______________________________________________________________________

## Streaming Events

When `stream: true`, the server emits SSE events:

### Lifecycle Events

| Event                  | Payload         | Description                           |
| ---------------------- | --------------- | ------------------------------------- |
| `response.created`     | Response object | Response created, generation starting |
| `response.in_progress` | Response object | Generation in progress                |
| `response.completed`   | Response object | Generation finished successfully      |
| `response.failed`      | Response object | Generation failed                     |
| `response.cancelled`   | Response object | Generation was cancelled              |
| `response.incomplete`  | Response object | Generation stopped early              |

### Output Item Events

| Event                        | Payload     | Description             |
| ---------------------------- | ----------- | ----------------------- |
| `response.output_item.added` | Output item | New output item started |
| `response.output_item.done`  | Output item | Output item completed   |

### Content Part Events

| Event                         | Payload      | Description              |
| ----------------------------- | ------------ | ------------------------ |
| `response.content_part.added` | Content part | New content part started |
| `response.content_part.done`  | Content part | Content part completed   |

### Text Streaming Events

| Event                                   | Payload           | Description                     |
| --------------------------------------- | ----------------- | ------------------------------- |
| `response.output_text.delta`            | `{delta: "text"}` | Incremental text token          |
| `response.output_text.done`             | `{text: "full"}`  | Complete text                   |
| `response.output_text.annotation.added` | Annotation object | Text annotation (citation, URL) |

### Function Call Events

| Event                                    | Payload              | Description                    |
| ---------------------------------------- | -------------------- | ------------------------------ |
| `response.function_call_arguments.delta` | `{delta: "json"}`    | Incremental function arguments |
| `response.function_call_arguments.done`  | `{arguments: "..."}` | Complete function arguments    |

### Refusal Events

| Event                    | Payload            | Description              |
| ------------------------ | ------------------ | ------------------------ |
| `response.refusal.delta` | `{delta: "text"}`  | Incremental refusal text |
| `response.refusal.done`  | `{refusal: "..."}` | Complete refusal text    |

### Tool-Specific Events

| Event                                          | Description                    |
| ---------------------------------------------- | ------------------------------ |
| `response.file_search_call.in_progress`        | File search started            |
| `response.file_search_call.searching`          | Searching files                |
| `response.file_search_call.completed`          | File search completed          |
| `response.code_interpreter_call.in_progress`   | Code interpreter started       |
| `response.code_interpreter_call.code.delta`    | Incremental code being written |
| `response.code_interpreter_call.code.done`     | Code writing completed         |
| `response.code_interpreter_call.interpreting`  | Code executing                 |
| `response.code_interpreter_call.completed`     | Code interpreter completed     |
| `response.image_generation_call.in_progress`   | Image generation started       |
| `response.image_generation_call.partial_image` | Partial image available        |
| `response.image_generation_call.completed`     | Image generation completed     |
| `response.mcp_call.in_progress`                | MCP tool call started          |
| `response.mcp_call.completed`                  | MCP tool call completed        |
| `response.web_search_call.in_progress`         | Web search started             |
| `response.web_search_call.searching`           | Searching the web              |
| `response.web_search_call.completed`           | Web search completed           |
| `response.reasoning.delta`                     | Incremental reasoning summary  |
| `response.reasoning.done`                      | Reasoning summary completed    |

### Error Event

| Event   | Payload      | Description       |
| ------- | ------------ | ----------------- |
| `error` | Error object | An error occurred |

______________________________________________________________________

## Key Differences from Chat Completions API

| Aspect               | Chat Completions                  | Responses API                                                                  |
| -------------------- | --------------------------------- | ------------------------------------------------------------------------------ |
| **Input format**     | `messages` array (role + content) | `input` array of typed items                                                   |
| **Output format**    | `choices[0].message`              | `output[]` array of typed items                                                |
| **Multi-turn**       | Client manages full history       | `previous_response_id` for server-side context                                 |
| **Instructions**     | System/developer message in array | Top-level `instructions` parameter                                             |
| **Tool results**     | Tool message with `tool_call_id`  | `function_call_output` item with `call_id`                                     |
| **Reasoning**        | `reasoning_effort` parameter      | `reasoning.effort` + `reasoning.summary`                                       |
| **Storage**          | `store: true` opt-in              | `store: true` by default                                                       |
| **Built-in tools**   | Not available                     | file_search, code_interpreter, image_generation, web_search, computer_use, mcp |
| **Streaming events** | `chat.completion.chunk` only      | Semantic typed events per output type                                          |
| **Response format**  | `response_format`                 | `text.format`                                                                  |
| **Token limit**      | `max_completion_tokens`           | `max_output_tokens`                                                            |
