package spendcompare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

var ErrTooManyRawRows = errors.New("comparison returned more than 100000 raw rows; narrow the window or add an ID filter")

type membershipKey struct {
	UserID string
	TeamID string
}

type dailyAnchor struct {
	Entity string
	Date   string
	APIKey string
}

type queryScope struct {
	CounterValues map[string][]string
	Memberships   []membershipKey
	Daily         map[string][]dailyAnchor
	Warnings      []string
}

// CompareDatabases opens two PostgreSQL connections with read-only defaults,
// reads bounded snapshots, and returns a deterministic comparison report.
func CompareDatabases(ctx context.Context, testDSN, referenceDSN string, filter Filter) (Report, error) {
	if err := filter.Validate(); err != nil {
		return Report{}, err
	}

	testConn, err := openReadOnlyConnection(ctx, testDSN, "test")
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = testConn.Close(ctx) }()

	referenceConn, err := openReadOnlyConnection(ctx, referenceDSN, "reference")
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = referenceConn.Close(ctx) }()

	return compareConnections(ctx, testConn, referenceConn, filter)
}

func openReadOnlyConnection(ctx context.Context, dsn, label string) (*pgx.Conn, error) {
	config, err := readOnlyConnectionConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("%s database configuration is invalid", label)
	}
	connection, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s database connection failed: %w", label, err)
		}
		return nil, fmt.Errorf("%s database connection failed", label)
	}
	return connection, nil
}

func readOnlyConnectionConfig(dsn string) (*pgx.ConnConfig, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if config.RuntimeParams == nil {
		config.RuntimeParams = make(map[string]string)
	}
	config.RuntimeParams["default_transaction_read_only"] = "on"
	config.RuntimeParams["application_name"] = "air-spend-compare"
	return config, nil
}

func compareConnections(ctx context.Context, testConn, referenceConn *pgx.Conn, filter Filter) (Report, error) {
	testTx, err := beginReadOnlyTransaction(ctx, testConn, "test")
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = testTx.Rollback(ctx) }()

	referenceTx, err := beginReadOnlyTransaction(ctx, referenceConn, "reference")
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = referenceTx.Rollback(ctx) }()

	testRaw, err := loadRaw(ctx, testTx, filter)
	if err != nil {
		return Report{}, fmt.Errorf("read test spend logs: %w", err)
	}
	referenceRaw, err := loadRaw(ctx, referenceTx, filter)
	if err != nil {
		return Report{}, fmt.Errorf("read reference spend logs: %w", err)
	}

	scope := buildQueryScope(testRaw, referenceRaw)
	testSnapshot, err := loadScopedSnapshot(ctx, testTx, testRaw, scope, filter.Window)
	if err != nil {
		return Report{}, fmt.Errorf("read test aggregates: %w", err)
	}
	referenceSnapshot, err := loadScopedSnapshot(ctx, referenceTx, referenceRaw, scope, filter.Window)
	if err != nil {
		return Report{}, fmt.Errorf("read reference aggregates: %w", err)
	}

	if err := testTx.Commit(ctx); err != nil {
		return Report{}, fmt.Errorf("finish test read-only transaction: %w", err)
	}
	if err := referenceTx.Commit(ctx); err != nil {
		return Report{}, fmt.Errorf("finish reference read-only transaction: %w", err)
	}

	report := CompareSnapshots(testSnapshot, referenceSnapshot, filter)
	report.Warnings = mergeWarnings(report.Warnings, scope.Warnings)
	if len(report.Warnings) > 0 {
		report.Equal = false
	}
	return report, nil
}

func beginReadOnlyTransaction(ctx context.Context, conn *pgx.Conn, label string) (pgx.Tx, error) {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, fmt.Errorf("begin %s read-only transaction: %w", label, err)
	}
	if err := setTransactionReadOnly(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("protect %s transaction as read-only: %w", label, err)
	}
	return tx, nil
}

type commandExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func setTransactionReadOnly(ctx context.Context, executor commandExecutor) error {
	_, err := executor.Exec(ctx, SetTransactionReadOnlySQL)
	return err
}

func loadRaw(ctx context.Context, tx pgx.Tx, filter Filter) ([]RawRow, error) {
	query, args, err := BuildRawQuery(filter)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]RawRow, 0)
	for rows.Next() {
		row, err := scanRawRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
		if len(result) > MaxRawRows {
			return nil, ErrTooManyRawRows
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRawRow(scanner rowScanner) (RawRow, error) {
	var (
		row                 RawRow
		requestDuration     pgtype.Int8
		completionStartTime pgtype.Timestamp
		metadataJSON        string
		requestTagsJSON     string
	)
	err := scanner.Scan(
		&row.RequestID,
		&row.CallType,
		&row.APIKey,
		&row.Spend,
		&row.TotalTokens,
		&row.PromptTokens,
		&row.CompletionTokens,
		&row.StartTime,
		&row.EndTime,
		&requestDuration,
		&completionStartTime,
		&row.Model,
		&row.ModelID,
		&row.ModelGroup,
		&row.CustomLLMProvider,
		&row.APIBase,
		&row.User,
		&metadataJSON,
		&row.CacheHit,
		&row.CacheKey,
		&requestTagsJSON,
		&row.TeamID,
		&row.OrganizationID,
		&row.EndUser,
		&row.RequesterIP,
		&row.SessionID,
		&row.Status,
		&row.MCPNamespacedToolName,
		&row.AgentID,
		&row.MessagesEmptyObject,
		&row.ResponseEmptyObject,
		&row.ProxyServerRequestEmptyObject,
	)
	if err != nil {
		return RawRow{}, err
	}
	if requestDuration.Valid {
		value := requestDuration.Int64
		row.RequestDurationMS = &value
	}
	if completionStartTime.Valid {
		value := completionStartTime.Time.UTC()
		row.CompletionStartTime = &value
	}
	if err := json.Unmarshal([]byte(metadataJSON), &row.Metadata); err != nil {
		return RawRow{}, fmt.Errorf("decode metadata for request %q: %w", row.RequestID, err)
	}
	if row.Metadata == nil {
		row.Metadata = make(map[string]any)
	}
	if err := json.Unmarshal([]byte(requestTagsJSON), &row.RequestTags); err != nil {
		return RawRow{}, fmt.Errorf("decode request_tags for request %q: %w", row.RequestID, err)
	}
	if callID, ok := row.Metadata["litellm_call_id"].(string); ok {
		row.CallID = callID
	}
	return row, nil
}

func loadScopedSnapshot(ctx context.Context, tx pgx.Tx, raw []RawRow, scope queryScope, window Window) (Snapshot, error) {
	counters, err := loadCounters(ctx, tx, scope)
	if err != nil {
		return Snapshot{}, err
	}
	daily, err := loadDaily(ctx, tx, scope, window)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Raw: raw, Counters: counters, Daily: daily}, nil
}

func buildQueryScope(snapshots ...[]RawRow) queryScope {
	counterSets := make(map[string]map[string]struct{}, len(counterTableKeys))
	for table := range counterTableKeys {
		counterSets[table] = make(map[string]struct{})
	}
	membershipSet := make(map[membershipKey]struct{})
	dailySets := make(map[string]map[string]dailyAnchor, len(dailyTableEntities))
	for table := range dailyTableEntities {
		dailySets[table] = make(map[string]dailyAnchor)
	}
	warningSet := make(map[string]struct{})

	for _, rows := range snapshots {
		for _, row := range rows {
			addNonEmpty(counterSets["LiteLLM_VerificationToken"], row.APIKey)
			addNonEmpty(counterSets["LiteLLM_UserTable"], row.User)
			addNonEmpty(counterSets["LiteLLM_TeamTable"], row.TeamID)
			addNonEmpty(counterSets["LiteLLM_OrganizationTable"], row.OrganizationID)
			addNonEmpty(counterSets["LiteLLM_EndUserTable"], row.EndUser)
			addNonEmpty(counterSets["LiteLLM_AgentsTable"], row.AgentID)
			for _, tag := range row.RequestTags {
				addNonEmpty(counterSets["LiteLLM_TagTable"], tag)
			}
			if row.User != "" && row.TeamID != "" {
				membershipSet[membershipKey{UserID: row.User, TeamID: row.TeamID}] = struct{}{}
			}

			if !dailyEligibleForRawRow(row) {
				warningSet[unsupportedCallTypeWarning(row.CallType)] = struct{}{}
				continue
			}
			base := dailyAnchor{
				Date:   row.StartTime.UTC().Format(time.DateOnly),
				APIKey: row.APIKey,
			}
			addDailyAnchor(dailySets["LiteLLM_DailyUserSpend"], base, row.User)
			addDailyAnchor(dailySets["LiteLLM_DailyTeamSpend"], base, row.TeamID)
			addDailyAnchor(dailySets["LiteLLM_DailyOrganizationSpend"], base, row.OrganizationID)
			addDailyAnchor(dailySets["LiteLLM_DailyEndUserSpend"], base, row.EndUser)
			addDailyAnchor(dailySets["LiteLLM_DailyAgentSpend"], base, row.AgentID)
			if len(row.RequestTags) == 0 {
				addDailyAnchor(dailySets["LiteLLM_DailyTagSpend"], base, "")
			}
			for _, tag := range row.RequestTags {
				addDailyAnchor(dailySets["LiteLLM_DailyTagSpend"], base, tag)
			}
		}
	}

	scope := queryScope{
		CounterValues: make(map[string][]string, len(counterSets)),
		Daily:         make(map[string][]dailyAnchor, len(dailySets)),
	}
	for table, values := range counterSets {
		scope.CounterValues[table] = sortedSet(values)
	}
	for key := range membershipSet {
		scope.Memberships = append(scope.Memberships, key)
	}
	sort.Slice(scope.Memberships, func(i, j int) bool {
		if scope.Memberships[i].TeamID != scope.Memberships[j].TeamID {
			return scope.Memberships[i].TeamID < scope.Memberships[j].TeamID
		}
		return scope.Memberships[i].UserID < scope.Memberships[j].UserID
	})
	for table, wanted := range dailySets {
		keys := make([]string, 0, len(wanted))
		for key := range wanted {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			scope.Daily[table] = append(scope.Daily[table], wanted[key])
		}
	}
	scope.Warnings = sortedSet(warningSet)
	return scope
}

func dailyEligibleForRawRow(row RawRow) bool {
	if _, ok := endpointForCallType(row.CallType); ok {
		return true
	}
	return strings.TrimSpace(row.CallType) == "" && row.Status == "failure"
}

func addNonEmpty(set map[string]struct{}, value string) {
	if value != "" {
		set[value] = struct{}{}
	}
}

func addDailyAnchor(set map[string]dailyAnchor, base dailyAnchor, entity string) {
	base.Entity = entity
	key := strings.Join([]string{base.Entity, base.Date, base.APIKey}, "\x00")
	set[key] = base
}

func sortedSet(set map[string]struct{}) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func loadCounters(ctx context.Context, tx pgx.Tx, scope queryScope) ([]MetricRow, error) {
	tables := make([]string, 0, len(counterTableKeys))
	for table := range counterTableKeys {
		tables = append(tables, table)
	}
	sort.Strings(tables)

	result := make([]MetricRow, 0)
	for _, table := range tables {
		values := scope.CounterValues[table]
		if len(values) == 0 {
			continue
		}
		keyColumn := counterTableKeys[table]
		rows, err := tx.Query(ctx, counterQuery(table, keyColumn), values)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", table, err)
		}
		for rows.Next() {
			var key string
			var spend float64
			if err := rows.Scan(&key, &spend); err != nil {
				rows.Close()
				return nil, err
			}
			result = append(result, MetricRow{
				Key:    metricKey(table, keyColumn, key),
				Values: map[string]float64{"spend": spend},
			})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	if len(scope.Memberships) > 0 {
		users := make([]string, 0, len(scope.Memberships))
		teams := make([]string, 0, len(scope.Memberships))
		for _, membership := range scope.Memberships {
			users = append(users, membership.UserID)
			teams = append(teams, membership.TeamID)
		}
		rows, err := tx.Query(ctx, membershipCounterSQL, users, teams)
		if err != nil {
			return nil, fmt.Errorf("query LiteLLM_TeamMembership: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var userID, teamID string
			var spend, totalSpend float64
			if err := rows.Scan(&userID, &teamID, &spend, &totalSpend); err != nil {
				return nil, err
			}
			result = append(result, MetricRow{
				Key: metricKey("LiteLLM_TeamMembership", "team_id", teamID, "user_id", userID),
				Values: map[string]float64{
					"spend":       spend,
					"total_spend": totalSpend,
				},
			})
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func loadDaily(ctx context.Context, tx pgx.Tx, scope queryScope, window Window) ([]MetricRow, error) {
	tables := make([]string, 0, len(dailyTableEntities))
	for table := range dailyTableEntities {
		tables = append(tables, table)
	}
	sort.Strings(tables)

	fromDate := window.From.UTC().Format(time.DateOnly)
	toDate := window.To.Add(-time.Nanosecond).UTC().Format(time.DateOnly)
	result := make([]MetricRow, 0)
	for _, table := range tables {
		wanted := scope.Daily[table]
		if len(wanted) == 0 {
			continue
		}
		entities, dates, apiKeys := dailyArrays(wanted)
		rows, err := tx.Query(
			ctx,
			dailyQuery(table, dailyTableEntities[table]),
			entities,
			dates,
			apiKeys,
			fromDate,
			toDate,
		)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", table, err)
		}
		for rows.Next() {
			row, err := scanDailyRow(rows, table, dailyTableEntities[table])
			if err != nil {
				rows.Close()
				return nil, err
			}
			result = append(result, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return result, nil
}

func dailyArrays(wanted []dailyAnchor) (entities, dates, apiKeys []string) {
	for _, row := range wanted {
		entities = append(entities, row.Entity)
		dates = append(dates, row.Date)
		apiKeys = append(apiKeys, row.APIKey)
	}
	return
}

type nullableMetricText struct {
	Value string
	Valid bool
}

func nullableMetricTextFromPG(value pgtype.Text) nullableMetricText {
	return nullableMetricText{Value: value.String, Valid: value.Valid}
}

func (value nullableMetricText) KeyValue() string {
	if !value.Valid {
		return "NULL"
	}
	return strconv.Quote(value.Value)
}

func (value nullableMetricText) LabelValue() any {
	if !value.Valid {
		return nil
	}
	return value.Value
}

func scanDailyRow(scanner rowScanner, table, entityColumn string) (MetricRow, error) {
	var (
		entity, apiKey, model, modelGroup, provider, mcpTool, endpoint       pgtype.Text
		date, requestID                                                      string
		promptTokens, completionTokens, cacheReadTokens, cacheCreationTokens int64
		spend                                                                float64
		apiRequests, successfulRequests, failedRequests                      int64
	)
	if err := scanner.Scan(
		&entity,
		&date,
		&apiKey,
		&model,
		&modelGroup,
		&provider,
		&mcpTool,
		&endpoint,
		&requestID,
		&promptTokens,
		&completionTokens,
		&cacheReadTokens,
		&cacheCreationTokens,
		&spend,
		&apiRequests,
		&successfulRequests,
		&failedRequests,
	); err != nil {
		return MetricRow{}, err
	}

	// LiteLLM_DailyTagSpend.request_id is a representative source row, not
	// part of the aggregate key. Concurrent aggregation can select a different
	// representative without changing the aggregate, so it is deliberately
	// excluded from equality.
	_ = requestID
	entityValue := nullableMetricTextFromPG(entity)
	apiKeyValue := nullableMetricTextFromPG(apiKey)
	modelValue := nullableMetricTextFromPG(model)
	modelGroupValue := nullableMetricTextFromPG(modelGroup)
	providerValue := nullableMetricTextFromPG(provider)
	mcpToolValue := nullableMetricTextFromPG(mcpTool)
	endpointValue := nullableMetricTextFromPG(endpoint)
	labels := map[string]any{"model_group": modelGroupValue.LabelValue()}
	return MetricRow{
		Key: nullableMetricKey(
			table,
			nullableMetricPair{Name: entityColumn, Value: entityValue},
			nullableMetricPair{Name: "date", Value: nullableMetricText{Value: date, Valid: true}},
			nullableMetricPair{Name: "api_key", Value: apiKeyValue},
			nullableMetricPair{Name: "model", Value: modelValue},
			nullableMetricPair{Name: "custom_llm_provider", Value: providerValue},
			nullableMetricPair{Name: "mcp_namespaced_tool_name", Value: mcpToolValue},
			nullableMetricPair{Name: "endpoint", Value: endpointValue},
		),
		Labels: labels,
		Values: map[string]float64{
			"prompt_tokens":               float64(promptTokens),
			"completion_tokens":           float64(completionTokens),
			"cache_read_input_tokens":     float64(cacheReadTokens),
			"cache_creation_input_tokens": float64(cacheCreationTokens),
			"spend":                       spend,
			"api_requests":                float64(apiRequests),
			"successful_requests":         float64(successfulRequests),
			"failed_requests":             float64(failedRequests),
		},
	}, nil
}
