# Redis / Valkey Integration

Auto AI Router supports an optional Redis (or [Valkey](https://valkey.io/)) backend that enables two features when running multiple replicas:

| Feature                              | Without Redis                                                 | With Redis                                                 |
| ------------------------------------ | ------------------------------------------------------------- | ---------------------------------------------------------- |
| **Rate limiting** (RPM/TPM)          | Per-pod counters — each replica enforces limits independently | Global counters — limits enforced across the whole cluster |
| **Response storage** (`store: true`) | Local bbolt file — not accessible from other pods             | Shared Redis — any replica can retrieve stored responses   |

If Redis is not configured, both features fall back to their original in-process implementations automatically.

## When to use Redis

Enable Redis if you run **two or more replicas** of auto-ai-router. Without it:

- A credential with `rpm: 100` effectively allows `100 × N` requests per minute (where N is the number of pods).
- Responses stored with `store: true` are only accessible from the pod that created them.

Single-replica deployments do not need Redis.

## Configuration

Add a `redis` section to `config.yaml`:

```yaml
redis:
  enabled: true
  addresses:
    - "redis:6379"            # host:port of your Redis/Valkey instance
  password: "os.environ/REDIS_PASSWORD"   # optional; supports env variable syntax
  key_prefix: "rl:"           # namespace prefix for all keys (default: "rl:")
  force_single_client: true   # set false only for Redis Cluster
  connect_timeout: 5s
  conn_write_timeout: 10s
```

### All Parameters

| Parameter             | Type     | Default | Description                                  |
| --------------------- | -------- | ------- | -------------------------------------------- |
| `enabled`             | bool     | `false` | Enable Redis backend                         |
| `addresses`           | []string | —       | One or more `host:port` addresses            |
| `username`            | string   | —       | Redis ACL username (optional)                |
| `password`            | string   | —       | Redis AUTH password (optional)               |
| `select_db`           | int      | `0`     | Redis database index                         |
| `key_prefix`          | string   | `"rl:"` | Prefix prepended to every key                |
| `tls_enabled`         | bool     | `false` | Enable TLS                                   |
| `connect_timeout`     | duration | `5s`    | TCP dial timeout                             |
| `conn_write_timeout`  | duration | `10s`   | Per-connection write/pipeline timeout        |
| `force_single_client` | bool     | `false` | Skip cluster detection (use for single-node) |

All string values support the `os.environ/VAR_NAME` syntax for environment variable substitution.

### Minimal config for single-node Valkey

```yaml
redis:
  enabled: true
  addresses:
    - "valkey:6379"
  force_single_client: true
```

### Config with password via environment variable

```yaml
redis:
  enabled: true
  addresses:
    - "os.environ/REDIS_ADDRESS"
  password: "os.environ/REDIS_PASSWORD"
  force_single_client: true
```

## Key Layout

All keys are namespaced under `key_prefix` (default `rl:`):

| Key pattern            | Used for                                |
| ---------------------- | --------------------------------------- |
| `rl:rpm:{type}:{name}` | Request count ZSET (sliding 60s window) |
| `rl:tpm:{type}:{name}` | Token count ZSET (sliding 60s window)   |
| `rl:response:{id}`     | Stored Responses API entry              |

Rate-limit keys are automatically removed after **120 seconds** of inactivity (via Redis `EXPIRE`). Response keys are set with the TTL from the `ttl` field of the request, or persist indefinitely when `ttl: 0`.

## How Rate Limiting Works in Redis

Each request is recorded atomically using a **Lua script** that runs entirely on the Redis server:

1. Remove entries older than 60 seconds from the sorted set (`ZREMRANGEBYSCORE`)
2. Count remaining entries (`ZCARD`)
3. If count ≥ limit → reject (return 0)
4. Add new entry with current timestamp as score and a UUID as member (`ZADD`)
5. Reset key TTL (`EXPIRE 120`)

The `TryAllowAll` check (credential RPM + credential TPM + model RPM + model TPM) is a single Lua script that validates all four counters atomically before recording anything — no TOCTOU race conditions across replicas.

Token consumption (`ConsumeTokens`) stores entries as `uuid:count` members so the TPM check can sum token counts with `ZRANGE` inside a Lua script.

## Memory Sizing

A rough guide for the rate-limit keyspace:

- Each request adds one entry to the RPM sorted set (UUID string ≈ 50 bytes + overhead ≈ ~100 bytes per entry).
- At 1000 RPM sustained across 10 credentials × 20 models = 200 keys, each holding up to 60 000 entries ≈ **~1.2 GB** in the worst case. In practice, entries expire within 60 seconds so live memory is much lower.

For the response store, size depends on average response payload. A typical 2 KB response at 10 000 stored responses ≈ **~20 MB**.

Start with `--maxmemory 256mb` and adjust based on observed usage.
