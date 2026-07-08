# Health Endpoints

## JSON Health — `/health`

Returns detailed status in JSON format:

```bash
curl http://localhost:8080/health
```

When credential scopes are configured, pass the same API key you use for model requests
to see that key's credential view:

```bash
curl -H "Authorization: Bearer sk-your-key" http://localhost:8080/health
```

Response includes:

- Status of visible credentials (RPM/TPM usage, ban status)
- Visible credential scopes (`scopes`, `denied_scopes`) for chained routers
- Status of visible configured models
- Aggregated statistics from connected proxy instances

Example:

```bash
curl http://localhost:8080/health | jq '.credentials'
```

## HTML Dashboard — `/vhealth`

An interactive HTML dashboard showing the same information in a visual format:

```
http://localhost:8080/vhealth
```

![vhealth dashboard](vhealth.png)

## Notes

- Health endpoints do not require authentication, but unauthenticated scoped views only include credentials without `scopes`
- The `/health` path is hardcoded and cannot be reconfigured
- Proxy credential statistics, model metadata, and provider scopes are synced from remote `/health` endpoints every 30 seconds
