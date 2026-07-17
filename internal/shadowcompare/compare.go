package shadowcompare

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

var selectedMetadataFields = []string{
	"user_api_key",
	"user_api_key_team_id",
	"user_api_key_project_id",
	"user_api_key_org_id",
	"user_api_key_user_id",
	"attempted_retries",
	"max_retries",
}

var selectedUsageFields = []string{
	"prompt_tokens",
	"completion_tokens",
	"total_tokens",
}

var selectedPromptDetailFields = []string{
	"audio_tokens",
	"image_tokens",
	"cached_tokens",
	"cache_creation_tokens",
}

var selectedCompletionDetailFields = []string{
	"audio_tokens",
	"image_tokens",
	"cached_tokens",
	"reasoning_tokens",
	"accepted_prediction_tokens",
	"rejected_prediction_tokens",
}

var selectedCostFields = []string{
	"input_cost",
	"cache_read_cost",
	"cache_creation_cost",
	"output_cost",
	"reasoning_cost",
	"audio_input_cost",
	"audio_output_cost",
	"prediction_cost",
	"input_image_cost",
	"output_image_cost",
	"image_cost",
	"total_cost",
}

func CompareSnapshots(test, reference Snapshot, filter Filter) Report {
	report := Report{
		ContractVersion: ContractVersion,
		GeneratedAt:     time.Now().UTC(),
		Window:          filter.Window,
		RequestID:       filter.RequestID,
		CallID:          filter.CallID,
		Semantics: ReportSemantics{
			RawTimingFieldsCompared:      false,
			RequestIDValuesCompared:      false,
			APIBaseValuesCompared:        false,
			RequesterIPValuesCompared:    false,
			AIRPrivacyContractChecked:    true,
			AIRExtensionMetadataCompared: false,
			MetadataComparison:           "selected_fields_when_available_on_both_sides",
			CounterScope:                 "cumulative_point_in_time",
			DailyScope:                   "all_dimensions_for_raw_entity_api_key_on_full_utc_dates_intersecting_window",
		},
		Raw:      compareRaw(test.Raw, reference.Raw),
		Counters: compareMetricRows(test.Counters, reference.Counters),
		Daily:    compareMetricRows(test.Daily, reference.Daily),
		Warnings: comparisonWarnings(test.Raw, reference.Raw),
	}
	report.Equal = rawEqual(report.Raw) && sectionEqual(report.Counters) && sectionEqual(report.Daily) && len(report.Warnings) == 0
	return report
}

func comparisonWarnings(testRows, referenceRows []RawRow) []string {
	warnings := make([]string, 0, 2)
	if len(testRows) == 0 && len(referenceRows) == 0 {
		warnings = append(warnings, "comparison contains no raw rows in either database")
	}
	testIneligible := countExplicitlyIneligible(testRows)
	referenceIneligible := countExplicitlyIneligible(referenceRows)
	if testIneligible > 0 || referenceIneligible > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"full financial comparison incomplete: comparison_eligible=false rows (test=%d, reference=%d)",
			testIneligible,
			referenceIneligible,
		))
	}
	return warnings
}

func countExplicitlyIneligible(rows []RawRow) int {
	count := 0
	for _, row := range rows {
		extension, ok := row.Metadata["spend_logs_metadata"].(map[string]any)
		if !ok {
			continue
		}
		eligible, ok := extension["comparison_eligible"].(bool)
		if ok && !eligible {
			count++
		}
	}
	return count
}

func compareRaw(testRows, referenceRows []RawRow) RawReport {
	report := RawReport{
		TestRows:           len(testRows),
		ReferenceRows:      len(referenceRows),
		MissingInTest:      []string{},
		MissingInReference: []string{},
		Duplicates:         []Duplicate{},
		Diffs:              []Difference{},
	}
	testGroups := groupRaw(testRows)
	referenceGroups := groupRaw(referenceRows)
	report.Duplicates = append(report.Duplicates, rawDuplicates("test", testGroups)...)
	report.Duplicates = append(report.Duplicates, rawDuplicates("reference", referenceGroups)...)

	unmatchedTest := make(map[string]RawRow)
	unmatchedReference := make(map[string]RawRow)
	for _, key := range unionKeys(testGroups, referenceGroups) {
		testGroup := testGroups[key]
		referenceGroup := referenceGroups[key]
		switch {
		case len(testGroup) == 1 && len(referenceGroup) == 1:
			report.Diffs = append(report.Diffs, compareRawRow(key, testGroup[0], referenceGroup[0])...)
		case len(testGroup) == 1 && len(referenceGroup) == 0:
			unmatchedTest[key] = testGroup[0]
		case len(testGroup) == 0 && len(referenceGroup) == 1:
			unmatchedReference[key] = referenceGroup[0]
		case len(testGroup) == 0:
			report.MissingInTest = append(report.MissingInTest, key)
		case len(referenceGroup) == 0:
			report.MissingInReference = append(report.MissingInReference, key)
		}
	}

	// Older LiteLLM versions do not always persist metadata.litellm_call_id.
	// In those rows request_id can still contain AIR's call ID, so correlate
	// singleton rows by both identifiers before declaring them missing.
	referenceIndex := indexRawGroupsByIdentifier(unmatchedReference)
	usedReference := make(map[string]struct{})
	for _, testKey := range sortedRawGroupKeys(unmatchedTest) {
		testRow := unmatchedTest[testKey]
		referenceKey, ok := bestRawMatch(testRow, unmatchedReference, referenceIndex, usedReference)
		if !ok {
			report.MissingInReference = append(report.MissingInReference, testKey)
			continue
		}
		usedReference[referenceKey] = struct{}{}
		referenceRow := unmatchedReference[referenceKey]
		key := rawPairKey(testRow, referenceRow)
		report.Diffs = append(report.Diffs, compareRawRow(key, testRow, referenceRow)...)
	}
	for _, referenceKey := range sortedRawGroupKeys(unmatchedReference) {
		if _, used := usedReference[referenceKey]; !used {
			report.MissingInTest = append(report.MissingInTest, referenceKey)
		}
	}
	sort.Strings(report.MissingInTest)
	sort.Strings(report.MissingInReference)
	return report
}

func indexRawGroupsByIdentifier(groups map[string]RawRow) map[string][]string {
	index := make(map[string][]string, len(groups)*2)
	for key, row := range groups {
		for _, identifier := range rawIdentifiers(row) {
			index[identifier] = append(index[identifier], key)
		}
	}
	for identifier := range index {
		sort.Strings(index[identifier])
	}
	return index
}

func sortedRawGroupKeys(groups map[string]RawRow) []string {
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func rawIdentifiers(row RawRow) []string {
	identifiers := make([]string, 0, 2)
	if row.CallID != "" {
		identifiers = append(identifiers, row.CallID)
	}
	if row.RequestID != "" && row.RequestID != row.CallID {
		identifiers = append(identifiers, row.RequestID)
	}
	return identifiers
}

func bestRawMatch(test RawRow, references map[string]RawRow, index map[string][]string, used map[string]struct{}) (string, bool) {
	candidates := make(map[string]struct{})
	for _, identifier := range rawIdentifiers(test) {
		for _, key := range index[identifier] {
			if _, alreadyUsed := used[key]; !alreadyUsed {
				candidates[key] = struct{}{}
			}
		}
	}
	bestKey := ""
	bestScore := 0
	for key := range candidates {
		score := rawMatchScore(test, references[key])
		if score > bestScore || (score == bestScore && score > 0 && (bestKey == "" || key < bestKey)) {
			bestKey = key
			bestScore = score
		}
	}
	return bestKey, bestScore > 0
}

func rawMatchScore(test, reference RawRow) int {
	switch {
	case test.CallID != "" && test.CallID == reference.CallID:
		return 4
	case test.RequestID != "" && test.RequestID == reference.RequestID:
		return 3
	case test.CallID != "" && test.CallID == reference.RequestID:
		return 2
	case test.RequestID != "" && test.RequestID == reference.CallID:
		return 2
	default:
		return 0
	}
}

func rawPairKey(test, reference RawRow) string {
	switch {
	case test.CallID != "" && test.CallID == reference.CallID:
		return "call:" + test.CallID
	case test.CallID != "" && test.CallID == reference.RequestID:
		return "call:" + test.CallID
	case reference.CallID != "" && reference.CallID == test.RequestID:
		return "call:" + reference.CallID
	case test.RequestID != "" && test.RequestID == reference.RequestID:
		return "request:" + test.RequestID
	default:
		return "request:" + test.RequestID
	}
}

func groupRaw(rows []RawRow) map[string][]RawRow {
	grouped := make(map[string][]RawRow, len(rows))
	for _, row := range rows {
		key := "request:" + row.RequestID
		if row.CallID != "" {
			key = "call:" + row.CallID
		}
		grouped[key] = append(grouped[key], row)
	}
	return grouped
}

func rawDuplicates(database string, groups map[string][]RawRow) []Duplicate {
	duplicates := make([]Duplicate, 0)
	for key, rows := range groups {
		if len(rows) < 2 {
			continue
		}
		requestIDs := make([]string, 0, len(rows))
		for _, row := range rows {
			requestIDs = append(requestIDs, row.RequestID)
		}
		sort.Strings(requestIDs)
		duplicates = append(duplicates, Duplicate{Database: database, Key: key, Count: len(rows), RequestIDs: requestIDs})
	}
	sort.Slice(duplicates, func(i, j int) bool {
		if duplicates[i].Database != duplicates[j].Database {
			return duplicates[i].Database < duplicates[j].Database
		}
		return duplicates[i].Key < duplicates[j].Key
	})
	return duplicates
}

func compareRawRow(key string, test, reference RawRow) []Difference {
	testFields := comparableRawFields(test)
	referenceFields := comparableRawFields(reference)
	addSelectedMetadataFields(testFields, referenceFields, test.Metadata, reference.Metadata)
	diffs := compareFields(key, testFields, referenceFields)
	for _, contract := range []struct {
		field         string
		isEmptyObject bool
	}{
		{field: "messages_is_empty_object", isEmptyObject: test.MessagesEmptyObject},
		{field: "response_is_empty_object", isEmptyObject: test.ResponseEmptyObject},
		{field: "proxy_server_request_is_empty_object", isEmptyObject: test.ProxyServerRequestEmptyObject},
	} {
		if !contract.isEmptyObject {
			diffs = append(diffs, Difference{
				Key:       key,
				Field:     "air_contract." + contract.field,
				Test:      false,
				Reference: true,
			})
		}
	}
	return diffs
}

func comparableRawFields(row RawRow) map[string]any {
	fields := map[string]any{
		"call_type":                row.CallType,
		"api_key":                  row.APIKey,
		"spend":                    row.Spend,
		"total_tokens":             row.TotalTokens,
		"prompt_tokens":            row.PromptTokens,
		"completion_tokens":        row.CompletionTokens,
		"model_id":                 row.ModelID,
		"model_group":              row.ModelGroup,
		"user":                     row.User,
		"cache_hit":                cacheHitState(row.CacheHit),
		"cache_key":                row.CacheKey,
		"request_tags":             normalizedTags(row.RequestTags),
		"team_id":                  row.TeamID,
		"organization_id":          row.OrganizationID,
		"end_user":                 row.EndUser,
		"status":                   row.Status,
		"mcp_namespaced_tool_name": row.MCPNamespacedToolName,
		"agent_id":                 row.AgentID,
	}
	// LiteLLM failure rows do not retain reliable provider/model dimensions.
	// AIR retains them in its contract, but they are not pairwise-comparable.
	if row.Status != "failure" {
		fields["model"] = row.Model
		fields["custom_llm_provider"] = row.CustomLLMProvider
	}
	return fields
}

func cacheHitState(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "true")
}

func addSelectedMetadataFields(testFields, referenceFields, testMetadata, referenceMetadata map[string]any) {
	addSharedMetadataGroup(testFields, referenceFields, testMetadata, referenceMetadata, "", selectedMetadataFields)
	addSharedMetadataGroup(testFields, referenceFields, testMetadata, referenceMetadata, "usage_object", selectedUsageFields)
	addSharedMetadataGroup(testFields, referenceFields, testMetadata, referenceMetadata, "usage_object.prompt_tokens_details", selectedPromptDetailFields)
	addSharedMetadataGroup(testFields, referenceFields, testMetadata, referenceMetadata, "usage_object.completion_tokens_details", selectedCompletionDetailFields)
	addSharedMetadataGroup(testFields, referenceFields, testMetadata, referenceMetadata, "cost_breakdown", selectedCostFields)
}

func addSharedMetadataGroup(testFields, referenceFields, testMetadata, referenceMetadata map[string]any, parent string, children []string) {
	testParent, testOK := metadataObject(testMetadata, parent)
	referenceParent, referenceOK := metadataObject(referenceMetadata, parent)
	if !testOK || !referenceOK {
		return
	}
	for _, child := range children {
		testValue, testExists := testParent[child]
		referenceValue, referenceExists := referenceParent[child]
		if !testExists && !referenceExists {
			continue
		}
		field := "metadata."
		if parent != "" {
			field += parent + "."
		}
		field += child
		testFields[field], referenceFields[field] = normalizeOptionalPair(testValue, testExists, referenceValue, referenceExists)
	}
}

func metadataObject(metadata map[string]any, path string) (map[string]any, bool) {
	current := metadata
	if path == "" {
		return current, current != nil
	}
	for _, part := range strings.Split(path, ".") {
		value, ok := current[part]
		if !ok || value == nil {
			return nil, false
		}
		current, ok = value.(map[string]any)
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func normalizeOptionalPair(test any, testExists bool, reference any, referenceExists bool) (any, any) {
	if !testExists || test == nil {
		test = emptyValueLike(reference)
	}
	if !referenceExists || reference == nil {
		reference = emptyValueLike(test)
	}
	return test, reference
}

func emptyValueLike(value any) any {
	if _, ok := number(value); ok {
		return float64(0)
	}
	switch value.(type) {
	case string:
		return ""
	case bool:
		return false
	case []any:
		return []any{}
	default:
		return nil
	}
}

func compareMetricRows(testRows, referenceRows []MetricRow) SectionReport {
	report := SectionReport{
		TestRows:           len(testRows),
		ReferenceRows:      len(referenceRows),
		MissingInTest:      []string{},
		MissingInReference: []string{},
		Duplicates:         []Duplicate{},
		Diffs:              []Difference{},
	}
	testGroups := groupMetrics(testRows)
	referenceGroups := groupMetrics(referenceRows)
	report.Duplicates = append(report.Duplicates, metricDuplicates("test", testGroups)...)
	report.Duplicates = append(report.Duplicates, metricDuplicates("reference", referenceGroups)...)

	for _, key := range unionKeys(testGroups, referenceGroups) {
		testGroup := testGroups[key]
		referenceGroup := referenceGroups[key]
		switch {
		case len(testGroup) == 0:
			report.MissingInTest = append(report.MissingInTest, key)
		case len(referenceGroup) == 0:
			report.MissingInReference = append(report.MissingInReference, key)
		case len(testGroup) == 1 && len(referenceGroup) == 1:
			testFields := metricFields(testGroup[0])
			referenceFields := metricFields(referenceGroup[0])
			report.Diffs = append(report.Diffs, compareFields(key, testFields, referenceFields)...)
		}
	}
	return report
}

func groupMetrics(rows []MetricRow) map[string][]MetricRow {
	grouped := make(map[string][]MetricRow, len(rows))
	for _, row := range rows {
		grouped[row.Key] = append(grouped[row.Key], row)
	}
	return grouped
}

func metricDuplicates(database string, groups map[string][]MetricRow) []Duplicate {
	duplicates := make([]Duplicate, 0)
	for key, rows := range groups {
		if len(rows) > 1 {
			duplicates = append(duplicates, Duplicate{Database: database, Key: key, Count: len(rows)})
		}
	}
	sort.Slice(duplicates, func(i, j int) bool {
		if duplicates[i].Database != duplicates[j].Database {
			return duplicates[i].Database < duplicates[j].Database
		}
		return duplicates[i].Key < duplicates[j].Key
	})
	return duplicates
}

func metricFields(row MetricRow) map[string]any {
	fields := make(map[string]any, len(row.Labels)+len(row.Values))
	for key, value := range row.Labels {
		fields[key] = value
	}
	for key, value := range row.Values {
		fields[key] = value
	}
	return fields
}

func compareFields(key string, testFields, referenceFields map[string]any) []Difference {
	diffs := make([]Difference, 0)
	for _, field := range unionKeys(testFields, referenceFields) {
		testValue, testOK := testFields[field]
		referenceValue, referenceOK := referenceFields[field]
		if testOK && referenceOK && valuesEqual(field, testValue, referenceValue) {
			continue
		}
		diff := Difference{Key: key, Field: field, Test: valueOrNil(testValue, testOK), Reference: valueOrNil(referenceValue, referenceOK)}
		if isCostField(field) {
			if referenceNumber, ok := number(referenceValue); ok {
				tolerance := CostTolerance(referenceNumber)
				diff.Tolerance = &tolerance
			}
		}
		diffs = append(diffs, diff)
	}
	return diffs
}

func valuesEqual(field string, test, reference any) bool {
	if isCostField(field) {
		testNumber, testOK := number(test)
		referenceNumber, referenceOK := number(reference)
		if testOK && referenceOK {
			return CostsEqual(testNumber, referenceNumber)
		}
	}
	if testNumber, testOK := number(test); testOK {
		if referenceNumber, referenceOK := number(reference); referenceOK {
			return math.Abs(testNumber-referenceNumber) <= 1e-12
		}
	}
	return reflect.DeepEqual(test, reference)
}

func number(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	default:
		return 0, false
	}
}

func isCostField(field string) bool {
	leaf := field
	if index := strings.LastIndexByte(field, '.'); index >= 0 {
		leaf = field[index+1:]
	}
	return leaf == "spend" || leaf == "total_spend" || strings.Contains(leaf, "cost")
}

func normalizedTags(tags []string) []string {
	result := append([]string{}, tags...)
	sort.Strings(result)
	return slices.Compact(result)
}

func valueOrNil(value any, ok bool) any {
	if !ok {
		return nil
	}
	return value
}

func rawEqual(report RawReport) bool {
	return len(report.MissingInTest) == 0 && len(report.MissingInReference) == 0 && len(report.Duplicates) == 0 && len(report.Diffs) == 0
}

func sectionEqual(report SectionReport) bool {
	return len(report.MissingInTest) == 0 && len(report.MissingInReference) == 0 && len(report.Duplicates) == 0 && len(report.Diffs) == 0
}

func unionKeys[V any](left, right map[string]V) []string {
	keys := make([]string, 0, len(left)+len(right))
	seen := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range right {
		if _, ok := seen[key]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func metricKey(table string, pairs ...string) string {
	var builder strings.Builder
	builder.WriteString(table)
	for index := 0; index+1 < len(pairs); index += 2 {
		builder.WriteByte('|')
		builder.WriteString(pairs[index])
		builder.WriteByte('=')
		builder.WriteString(strconv.Quote(pairs[index+1]))
	}
	return builder.String()
}

type nullableMetricPair struct {
	Name  string
	Value nullableMetricText
}

func nullableMetricKey(table string, pairs ...nullableMetricPair) string {
	var builder strings.Builder
	builder.WriteString(table)
	for _, pair := range pairs {
		builder.WriteByte('|')
		builder.WriteString(pair.Name)
		builder.WriteByte('=')
		builder.WriteString(pair.Value.KeyValue())
	}
	return builder.String()
}

func unsupportedCallTypeWarning(callType string) string {
	return "daily comparison skipped unsupported call_type " + strconv.Quote(callType)
}

func mergeWarnings(groups ...[]string) []string {
	set := make(map[string]struct{})
	for _, group := range groups {
		for _, warning := range group {
			set[warning] = struct{}{}
		}
	}
	return sortedSet(set)
}
