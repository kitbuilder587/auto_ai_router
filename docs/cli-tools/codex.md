# OpenAI Codex CLI

[Codex CLI](https://github.com/openai/codex) is an open-source terminal coding assistant from OpenAI. It uses the OpenAI API to execute tasks, write and edit code, run shell commands, and browse files — all from the terminal.

Because Auto AI Router exposes an OpenAI-compatible API, Codex CLI can be pointed at the router and transparently use any configured backend (Vertex AI, Anthropic, Gemini, OpenAI, etc.).

## Prerequisites

- Codex CLI installed: `npm install -g @openai/codex`
- Auto AI Router running and reachable (e.g. `http://localhost:8080`)
- A master key configured in your router (`server.master_key`)

## Configuration

Codex reads its configuration from `~/.codex/config.toml`.

### Step 1 — Set the API key environment variable

```bash
export OPENAI_API_KEY="sk-your-master-key-here"
```

Add this line to your shell profile (`.bashrc`, `.zshrc`) to make it permanent.

### Step 2 — Add a router provider and profile

Edit (or create) `~/.codex/config.toml`:

```toml
[model_providers.router]
name             = "router"
base_url         = "http://localhost:8080/v1"
env_key          = "OPENAI_API_KEY"
stream_idle_timeout_ms = 10000000

[profiles.router]
model_provider       = "router"
model                = "gpt-4o"
model_context_window = 32000
web_search           = "disabled"
```

| Field                    | Description                                                                                    |
| ------------------------ | ---------------------------------------------------------------------------------------------- |
| `base_url`               | URL of your running Auto AI Router instance                                                    |
| `env_key`                | Environment variable that holds the master key                                                 |
| `stream_idle_timeout_ms` | Streaming idle timeout in milliseconds. Set high for slow providers (Vertex AI, Anthropic).    |
| `model`                  | Default model sent in every request. Must match a model name in the router config.             |
| `model_context_window`   | Context window hint for Codex (tokens). Does not affect the backend — set to match your model. |
| `web_search`             | Set to `"disabled"` — the router does not implement the Codex web search protocol.             |

## Running Codex

```bash
codex -p router
```

The `-p router` flag selects the `[profiles.router]` profile defined above.

To use a different model for a single session:

```bash
codex -p router --model gemini-2.5-flash
```

The model name must match one of the models registered in the router's `models` section or available through a configured credential.

## Using Different Backends

Because Auto AI Router handles credential routing, you can switch backends simply by changing the `model` field — no Codex reconfiguration needed.

Example router config with multiple backends:

```yaml
credentials:
  - name: openai_main
    type: openai
    api_key: "sk-proj-xxxxx"

  - name: vertex_gemini
    type: vertex-ai
    project_id: "my-project"
    location: "us-central1"
    credentials_file: "path/to/sa.json"

  - name: anthropic_main
    type: anthropic
    api_key: "sk-ant-xxxxx"

models:
  - name: "gpt-4o"
    credential: openai_main
  - name: "gemini-2.5-flash"
    credential: vertex_gemini
  - name: "claude-sonnet-4-6"
    credential: anthropic_main
```

Switch models in Codex profiles:

```toml
[profiles.gemini]
model_provider       = "router"
model                = "gemini-2.5-flash"
model_context_window = 1000000

[profiles.claude]
model_provider       = "router"
model                = "claude-sonnet-4-6"
model_context_window = 200000
```

Run with the desired backend:

```bash
codex -p gemini
codex -p claude
```

## Model Aliases

If your models in the router use aliases (see [Model Aliases](../advanced/model_alias.md)), you can use the alias name directly in Codex:

```yaml
# router config
model_aliases:
  - alias: "fast"
    target: "gemini-2.5-flash"
```

```toml
# Codex profile
[profiles.fast]
model_provider       = "router"
model                = "fast"
model_context_window = 1000000
```

## Troubleshooting

### Connection refused

Verify the router is running and listening on the correct port:

```bash
curl http://localhost:8080/health
```

### 401 Unauthorized

Check that `OPENAI_API_KEY` is set in your environment and matches `server.master_key` in the router config:

```bash
echo $OPENAI_API_KEY
```

### Stream timeout / partial responses

Increase `stream_idle_timeout_ms` in the provider config. Providers like Vertex AI or Anthropic can have longer gaps between stream chunks than OpenAI:

```toml
[model_providers.router]
stream_idle_timeout_ms = 30000000
```

### Model not found

Make sure the model name in `[profiles.router]` exactly matches either a `models[].name` entry or a credential that supports the model in your router config.

### web_search errors

Always set `web_search = "disabled"` in Codex profiles when pointing at the router. The router does not implement the Codex-specific web search extension.
