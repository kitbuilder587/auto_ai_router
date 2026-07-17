package shadowcompare

import (
	"fmt"
	"strings"
)

const (
	MaxRawRows                = 100000
	SetTransactionReadOnlySQL = "SET TRANSACTION READ ONLY"
)

const rawSelectSQL = `
SELECT
  request_id,
  call_type,
  api_key,
  spend,
  total_tokens,
  prompt_tokens,
  completion_tokens,
  "startTime",
  "endTime",
  request_duration_ms,
  "completionStartTime",
  COALESCE(model, ''),
  COALESCE(model_id, ''),
  COALESCE(model_group, ''),
  COALESCE(custom_llm_provider, ''),
  COALESCE(api_base, ''),
  COALESCE("user", ''),
  COALESCE(metadata, '{}'::jsonb)::text,
  COALESCE(cache_hit, ''),
  COALESCE(cache_key, ''),
  COALESCE(request_tags, '[]'::jsonb)::text,
  COALESCE(team_id, ''),
  COALESCE(organization_id, ''),
  COALESCE(end_user, ''),
  COALESCE(requester_ip_address, ''),
  COALESCE(session_id, ''),
  COALESCE(status, ''),
  COALESCE(mcp_namespaced_tool_name, ''),
  COALESCE(agent_id, ''),
  COALESCE(messages = '{}'::jsonb, false),
  COALESCE(response = '{}'::jsonb, false),
  COALESCE(proxy_server_request = '{}'::jsonb, false)
FROM "LiteLLM_SpendLogs"
WHERE "startTime" >= $1
  AND "startTime" < $2`

const singleCounterSQL = `
SELECT %s, spend
FROM "%s"
WHERE %s = ANY($1::text[])
ORDER BY %s`

const membershipCounterSQL = `
WITH wanted(user_id, team_id) AS (
  SELECT * FROM unnest($1::text[], $2::text[])
)
SELECT c.user_id, c.team_id, c.spend, c.total_spend
FROM "LiteLLM_TeamMembership" c
JOIN wanted w ON w.user_id = c.user_id AND w.team_id = c.team_id
ORDER BY c.user_id, c.team_id`

const dailySQLFormat = `
WITH wanted(entity_id, date, api_key) AS (
  SELECT DISTINCT * FROM unnest($1::text[], $2::text[], $3::text[])
)
SELECT
  d.%s,
  d.date,
  d.api_key,
  d.model,
  d.model_group,
  d.custom_llm_provider,
  d.mcp_namespaced_tool_name,
  d.endpoint,
  %s,
  d.prompt_tokens,
  d.completion_tokens,
  d.cache_read_input_tokens,
  d.cache_creation_input_tokens,
  d.spend,
  d.api_requests,
  d.successful_requests,
  d.failed_requests
FROM "%s" d
JOIN wanted w
  ON (d.%s IS NOT DISTINCT FROM w.entity_id OR (w.entity_id = '' AND d.%s IS NULL))
 AND d.date = w.date
 AND (d.api_key IS NOT DISTINCT FROM w.api_key OR (w.api_key = '' AND d.api_key IS NULL))
WHERE d.date >= $4
  AND d.date <= $5
ORDER BY d.%s, d.date, d.api_key, d.model, d.custom_llm_provider, d.mcp_namespaced_tool_name, d.endpoint`

var counterTableKeys = map[string]string{
	"LiteLLM_VerificationToken": "token",
	"LiteLLM_UserTable":         "user_id",
	"LiteLLM_TeamTable":         "team_id",
	"LiteLLM_OrganizationTable": "organization_id",
	"LiteLLM_EndUserTable":      "user_id",
	"LiteLLM_TagTable":          "tag_name",
	"LiteLLM_AgentsTable":       "agent_id",
}

var dailyTableEntities = map[string]string{
	"LiteLLM_DailyUserSpend":         "user_id",
	"LiteLLM_DailyTeamSpend":         "team_id",
	"LiteLLM_DailyOrganizationSpend": "organization_id",
	"LiteLLM_DailyEndUserSpend":      "end_user_id",
	"LiteLLM_DailyAgentSpend":        "agent_id",
	"LiteLLM_DailyTagSpend":          "tag",
}

var callTypeEndpoints = map[string]string{
	"acompletion":       "/chat/completions",
	"atext_completion":  "/completions",
	"aembedding":        "/embeddings",
	"aresponses":        "/responses",
	"aimage_generation": "/image/generations",
	"aimage_edit":       "/images/edits",
}

func BuildRawQuery(filter Filter) (string, []any, error) {
	if err := filter.Validate(); err != nil {
		return "", nil, err
	}

	query := rawSelectSQL
	args := []any{filter.Window.From, filter.Window.To}
	if filter.RequestID != "" {
		args = append(args, filter.RequestID)
		query += fmt.Sprintf("\n  AND request_id = $%d", len(args))
	}
	if filter.CallID != "" {
		args = append(args, filter.CallID)
		query += fmt.Sprintf("\n  AND metadata ->> 'litellm_call_id' = $%d", len(args))
	}
	query += fmt.Sprintf("\nORDER BY \"startTime\", request_id\nLIMIT %d", MaxRawRows+1)
	return query, args, nil
}

func counterQuery(table, keyColumn string) string {
	return fmt.Sprintf(singleCounterSQL, keyColumn, table, keyColumn, keyColumn)
}

func dailyQuery(table, entityColumn string) string {
	return fmt.Sprintf(dailySQLFormat, entityColumn, "''::text", table, entityColumn, entityColumn, entityColumn)
}

func ReadOnlySQLTemplates() map[string]string {
	queries := map[string]string{"raw": rawSelectSQL, "team_membership_counter": membershipCounterSQL}
	for table, key := range counterTableKeys {
		queries["counter_"+table] = counterQuery(table, key)
	}
	for name, query := range DailySQLTemplates() {
		queries["daily_"+name] = query
	}
	return queries
}

func DailySQLTemplates() map[string]string {
	queries := make(map[string]string, len(dailyTableEntities))
	for table, entity := range dailyTableEntities {
		queries[table] = dailyQuery(table, entity)
	}
	return queries
}

func endpointForCallType(callType string) (string, bool) {
	endpoint, ok := callTypeEndpoints[strings.TrimSpace(callType)]
	return endpoint, ok
}
