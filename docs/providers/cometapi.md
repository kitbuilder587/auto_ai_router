# Comet API

Comet API is supported as an Anthropic-compatible provider via the dedicated
`cometapi` credential type. Requests are converted from OpenAI Chat Completions
or Responses API format to Anthropic Messages API and sent to `/v1/messages`.

## Configuration

```yaml
credentials:
  - name: "comet_anthropic"
    type: "cometapi"
    api_key: "os.environ/COMET_API_KEY"
    base_url: "https://api.cometapi.com/v1"
    rpm: 60
    tpm: -1
```

## Claude Model Aliases

Use public `name` values for clients, and map them to Comet model IDs with
`model` where the provider names differ:

```yaml
models:
  - name: "anthropic/claude-haiku-4.5"
    model: "claude-haiku-4-5-20251001"
    credential: comet_anthropic
  - name: "claude-haiku-4.5"
    model: "claude-haiku-4-5-20251001"
    credential: comet_anthropic
  - name: "claude-haiku-4-5-20251001"
    credential: comet_anthropic

  - name: "anthropic/claude-opus-4.1"
    model: "claude-opus-4-1-20250805"
    credential: comet_anthropic
  - name: "claude-opus-4.1"
    model: "claude-opus-4-1-20250805"
    credential: comet_anthropic
  - name: "claude-opus-4-1-20250805"
    credential: comet_anthropic

  - name: "anthropic/claude-opus-4.5"
    model: "claude-opus-4-5-20251101"
    credential: comet_anthropic
  - name: "claude-opus-4.5"
    model: "claude-opus-4-5-20251101"
    credential: comet_anthropic
  - name: "claude-opus-4-5-20251101"
    credential: comet_anthropic

  - name: "anthropic/claude-opus-4.6"
    model: "claude-opus-4-6"
    credential: comet_anthropic
  - name: "claude-opus-4-6"
    credential: comet_anthropic

  - name: "anthropic/claude-opus-4.7"
    model: "claude-opus-4-7"
    credential: comet_anthropic
  - name: "claude-opus-4-7"
    credential: comet_anthropic

  - name: "anthropic/claude-sonnet-4"
    model: "claude-sonnet-4-20250514"
    credential: comet_anthropic
  - name: "claude-sonnet-4"
    model: "claude-sonnet-4-20250514"
    credential: comet_anthropic

  - name: "anthropic/claude-sonnet-4.5"
    model: "claude-sonnet-4-5-20250929"
    credential: comet_anthropic
  - name: "claude-sonnet-4.5"
    model: "claude-sonnet-4-5-20250929"
    credential: comet_anthropic
  - name: "claude-sonnet-4-5-20250929"
    credential: comet_anthropic

  - name: "anthropic/claude-sonnet-4.6"
    model: "claude-sonnet-4-6"
    credential: comet_anthropic
  - name: "claude-sonnet-4-6"
    credential: comet_anthropic
```

## Prompt Caching

`cometapi` uses the Anthropic Messages cache-control format:

```json
{"cache_control": {"type": "ephemeral"}}
```

When session-sticky routing is active, the router can automatically inject cache
markers for stable conversation history. Cache accounting is preserved in OpenAI
responses:

| Comet/Anthropic usage field   | OpenAI-compatible response field              |
| ----------------------------- | --------------------------------------------- |
| `cache_read_input_tokens`     | `prompt_tokens_details.cached_tokens`         |
| `cache_creation_input_tokens` | `prompt_tokens_details.cache_creation_tokens` |

For `claude-sonnet-4.5`, prefer the dated Comet model
`claude-sonnet-4-5-20250929` for more stable prompt-cache behavior.

## Error Masking

Comet upstream error bodies are masked before being returned to clients or logged.
The HTTP status is preserved, but the response body is replaced with a neutral
OpenAI-compatible error object.
