# Prometheus Metrics

Enable Prometheus metrics in the config:

```yaml
monitoring:
  prometheus_enabled: true
```

Metrics are available at `/metrics`.

The same metrics can also be **pushed** to an OTLP collector instead of (or in addition to) being scraped — see [OpenTelemetry](otel.md).

## Available Metrics

| Metric                                     | Type      | Description                                                |
| ------------------------------------------ | --------- | ---------------------------------------------------------- |
| `auto_ai_router_credential_rpm_current`    | Gauge     | Current RPM usage per credential                           |
| `auto_ai_router_credential_tpm_current`    | Gauge     | Current TPM usage per credential                           |
| `auto_ai_router_credential_banned`         | Gauge     | Ban status per credential (1 = banned)                     |
| `auto_ai_router_requests_total`            | Counter   | Total requests processed                                   |
| `auto_ai_router_requests_duration_seconds` | Histogram | Request latency distribution                               |
| `auto_ai_router_aborted_requests_total`    | Counter   | Client-aborted requests by credential, model, and endpoint |

## Proxy Credential Exclusion

Proxy credentials are **not** included in Prometheus metrics. Their statistics are available through the `/health` endpoint and are synchronized from the remote `/health` endpoint every 30 seconds.

## Scrape Configuration

Example Prometheus scrape config:

```yaml
scrape_configs:
  - job_name: 'auto-ai-router'
    scrape_interval: 15s
    static_configs:
      - targets: ['localhost:8080']
```
