# Isolated spend writer operations

The isolated writer stores spend rows and derived aggregates in the configured
`spend_log.database_url`. It is disabled when that URL is empty. Startup also
checks `expected_database_name` so a configuration mistake cannot silently
write to an unintended database.

## Comparison-window validity

`auto_ai_router_shadow_spend_comparison_window_valid` is a conservative,
process-local latch. It is `1` only after the queue and DLQ are empty and the
current process has observed no terminal loss, DLQ overflow, commit ambiguity,
or aggregation error. Once one of those events occurs, the gauge remains `0`
for the rest of that process lifetime even if the writer later recovers.

A restart creates a new observation window and therefore resets the latch. Do
not interpret a persistent `0` as proof that an incident is still active, and
do not restart solely to make the gauge green. Alert on increases in the
associated error and loss counters, then use queue depth, DLQ size, sink health,
and recent logs to determine whether the condition is ongoing. A financial
comparison is valid only for a time range whose AIR processes all kept this
gauge at `1` throughout the range.

## Triage

1. Check sink health, queue depth, pending entries, and DLQ size.
2. Identify which terminal counter increased and inspect logs from that time.
3. Restore database connectivity or capacity and allow the DLQ to drain.
4. Start a new comparison interval only after the live gauges are healthy; a
   process restart may define that new interval but does not repair lost data.
