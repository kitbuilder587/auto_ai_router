# Security

## Environment Variables

Configuration supports values from environment variables using the `os.environ/VARIABLE_NAME` pattern:

```yaml
server:
  master_key: "os.environ/MASTER_KEY"
  model_prices_link: "os.environ/MODEL_PRICES_URL"

credentials:
  - name: "openai"
    type: "openai"
    api_key: "os.environ/OPENAI_API_KEY"
    base_url: "https://api.openai.com"

  - name: "vertex_ai"
    type: "vertex-ai"
    project_id: "os.environ/GCP_PROJECT_ID"
    location: "us-central1"
    credentials_json: "os.environ/VERTEX_CREDENTIALS"

  - name: "gemini_studio"
    type: "gemini"
    api_key: "os.environ/GEMINI_API_KEY"
    base_url: "https://generativelanguage.googleapis.com"

litellm_db:
  enabled: true
  database_url: "os.environ/LITELLM_DATABASE_URL"
```

Set the variables before starting the router:

```bash
export MASTER_KEY="sk-your-master-key"
export OPENAI_API_KEY="sk-proj-..."
export GCP_PROJECT_ID="my-project"
export GEMINI_API_KEY="AIza..."
export LITELLM_DATABASE_URL="postgresql://user:pass@localhost/litellm"

./auto_ai_router -config config.yaml
```

## Master Key Authentication

All API requests require the `Authorization` header with the master key:

```bash
curl -H "Authorization: Bearer sk-your-master-key" http://localhost:8080/v1/chat/completions ...
```

Health and metrics endpoints (`/health`, `/vhealth`, `/metrics`) do not require authentication.

## LiteLLM API Key Auth

When [LiteLLM DB integration](../litellm-integration/litellm_db.md) is enabled, the router also validates API keys against the LiteLLM verification token table. This allows using LiteLLM-issued API keys alongside the master key.

## Scoped Credential Visibility

Use scopes when one router serves several clients but some credentials should be visible only
to specific clients. Scopes are resolved from the authenticated key, not from user-provided
headers.

```yaml
server:
  master_key: "os.environ/MASTER_KEY"
  api_keys:
    - name: "vsellm"
      key: "os.environ/VSELLM_KEY"
      scopes: [vsellm]
    - name: "avito"
      key: "os.environ/AVITO_KEY"
      scopes: [avito]

credentials:
  - name: "cheapgpt"
    type: "anthropic"
    api_key: "os.environ/CHEAPGPT_KEY"
    base_url: "https://cheapgpt.example.com"
    rpm: 400
    scopes: [vsellm, avito]

  - name: "cometapi"
    type: "anthropic"
    api_key: "os.environ/COMETAPI_KEY"
    base_url: "https://api.cometapi.com"
    rpm: 500
    scopes: [vsellm]

  - name: "shared-grant"
    type: "bedrock"
    api_key: "os.environ/GRANT_KEY"
    base_url: "https://grant.example.com"
    rpm: 1000
    # no scopes: visible to every client
```

In this example, requests authenticated with `VSELLM_KEY` can route through `cheapgpt`,
`cometapi`, and `shared-grant`. Requests authenticated with `AVITO_KEY` can route through
`cheapgpt` and `shared-grant`, but not `cometapi`.

The same visibility filter is applied to `/health`, `/trace`, and `/v1/models` when they are
called with an `Authorization` header. Calls without authorization see only unscoped
credentials. The master key always sees the full router state.

When LiteLLM DB authentication is enabled, the router derives scopes from token metadata
(`scopes`, `scope`, `client`, `client_name`, `tenant`) and from key/team/user aliases or names.
If no scope can be derived from a valid LiteLLM token, the token keeps the previous full-access
behavior for backward compatibility.
