# Load Balancing

## Round-Robin

Auto AI Router distributes requests across credentials using round-robin balancing. When multiple credentials support the same model, each request goes to the next available credential in rotation.

### Example

With 4 Vertex AI credentials configured for `gemini-2.5-flash`:

```
Request 1 → vertex_cred_1
Request 2 → vertex_cred_2
Request 3 → vertex_cred_3
Request 4 → vertex_cred_4
Request 5 → vertex_cred_1  (cycle repeats)
```

Credentials that are rate-limited or banned are skipped automatically.

## Multiple Credentials per Model

Configure multiple credentials for the same model to multiply your effective rate limits:

```yaml
credentials:
  - name: "openai_1"
    type: "openai"
    api_key: "os.environ/OPENAI_KEY_1"
    base_url: "https://api.openai.com"
    rpm: 100
    tpm: 50000

  - name: "openai_2"
    type: "openai"
    api_key: "os.environ/OPENAI_KEY_2"
    base_url: "https://api.openai.com"
    rpm: 100
    tpm: 50000

models:
  - name: "gpt-4o"
    credential: openai_1
    rpm: 100
    tpm: 50000
  - name: "gpt-4o"
    credential: openai_2
    rpm: 100
    tpm: 50000
```

This gives you an effective 200 RPM for `gpt-4o`.

## Fallback Priority

Primary credentials (non-fallback) are always tried first. Fallback credentials (`is_fallback: true`) are used only when all primary credentials are exhausted. See [Proxy — Fallback Behavior](../providers/proxy.md#fallback-behavior) for details.

## Proxy Chain Fallback

When using chained routers (e.g. router01 → router02 as primary, router03 as fallback), fallback works across the chain:

```
router01 receives request
  └─► router02 (primary proxy) → router02 returns 429/5xx
      └─► router01 detects retryable error
          └─► router03 (fallback proxy) → success
```

router01 marks router02 as "tried" immediately on the first attempt, so same-type retries
never re-select router02. After all primary proxies are exhausted, `TryFallbackProxy`
selects the next `is_fallback: true` credential (router03).

```yaml
credentials:
  - name: "router02"
    type: "proxy"
    base_url: "https://router02.example.com"
    is_fallback: false

  - name: "router03"
    type: "proxy"
    base_url: "https://router03.example.com"
    is_fallback: true
```
