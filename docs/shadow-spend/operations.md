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

## Comparison semantics (what shadow-compare can and cannot prove)

The comparison against the primary LiteLLM accounting is **aggregate-only**.
Interpret its output with the following known, deliberate asymmetries:

- **No row-by-row matching.** LiteLLM's `request_id` is the provider response
  id (fallback `litellm_call_id`); AIR writes its own router UUID. The two
  systems cannot be joined per request — only totals over a UTC window are
  comparable.
- **No per-model breakdown.** LiteLLM records the backend model name and the
  deployment id in `model`/`model_id`; AIR records the public alias. Totals
  match, per-model daily buckets never will. Compare spend/tokens/request
  counts without the `model` dimension.
- **Tables LiteLLM does not maintain.** AIR additionally increments
  `LiteLLM_ProjectTable.spend`, `LiteLLM_OrganizationMembership.spend` and the
  per-entity `model_spend` JSON columns. The primary writer never touches
  them; they are excluded from comparison and must not be treated as drift.
- **Empty dimensions.** AIR stores `''` where LiteLLM (via Prisma) stores SQL
  `NULL` in nullable dimension columns (`model_group`, `custom_llm_provider`,
  `endpoint`, ...). The comparator normalizes `'' = NULL`; ad-hoc SQL against
  the two databases must do the same.
- **Expected drift direction under retries.** LiteLLM deduplicates the raw
  spend row (`skip_duplicates`) but enqueues entity and daily increments
  regardless, so a redelivered event can double-count on the primary side.
  AIR applies raw row, counters, and daily projections in one transaction
  keyed by the inserted row, so replays add nothing. During incidents expect
  `primary >= shadow`, not the reverse.
- **Failure rows.** Both sides record failed requests (`failed_requests`
  counters, spend usually 0), but LiteLLM writes `call_type=''` for them
  while AIR keeps the resolved call type; daily failure buckets can differ in
  the `endpoint`/`custom_llm_provider` dimensions.

## Triage

1. Check sink health, queue depth, pending entries, and DLQ size.
2. Identify which terminal counter increased and inspect logs from that time.
3. Restore database connectivity or capacity and allow the DLQ to drain.
4. Start a new comparison interval only after the live gauges are healthy; a
   process restart may define that new interval but does not repair lost data.
