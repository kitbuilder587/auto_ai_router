package spendcompare

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCompareSnapshotsRawMissingDuplicateAndDiff(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, 7, 12, 8, 30, 0, 0, time.UTC)
	reference := RawRow{
		RequestID:           "resp-1",
		CallID:              "call-1",
		StartTime:           when,
		EndTime:             when.Add(time.Second),
		CallType:            "acompletion",
		Model:               "backend-model",
		Spend:               10,
		Status:              "success",
		Metadata:            map[string]any{"status": "success"},
		MessagesEmptyObject: true, ResponseEmptyObject: true, ProxyServerRequestEmptyObject: true,
	}
	testRow := reference
	testRow.Status = "failure"
	testDuplicate := testRow
	testDuplicate.RequestID = "resp-duplicate"
	missingReference := reference
	missingReference.RequestID = "resp-2"
	missingReference.CallID = "call-2"

	report := CompareSnapshots(
		Snapshot{Raw: []RawRow{testRow, testDuplicate}},
		Snapshot{Raw: []RawRow{reference, missingReference}},
		Filter{},
	)

	if report.Equal {
		t.Fatal("report.Equal = true, want divergence")
	}
	if len(report.Raw.Duplicates) != 1 || report.Raw.Duplicates[0].Database != "test" {
		t.Fatalf("duplicates = %#v, want one test duplicate", report.Raw.Duplicates)
	}
	if len(report.Raw.MissingInTest) != 1 || report.Raw.MissingInTest[0] != "call:call-2" {
		t.Fatalf("missing_in_test = %#v", report.Raw.MissingInTest)
	}
}

func TestCompareSnapshotsJSONUsesEmptyArrays(t *testing.T) {
	t.Parallel()

	report := CompareSnapshots(Snapshot{}, Snapshot{}, Filter{})
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"missing_in_test":[]`, `"missing_in_reference":[]`, `"duplicates":[]`, `"diffs":[]`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("JSON report missing stable empty array %s: %s", field, encoded)
		}
	}
}

func TestCompareSnapshotsAppliesCostToleranceAndExactCategoricals(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, 7, 12, 8, 30, 0, 0, time.UTC)
	reference := RawRow{
		RequestID: "resp-1",
		CallID:    "call-1",
		StartTime: when,
		EndTime:   when.Add(time.Second),
		CallType:  "acompletion",
		Model:     "backend-model",
		Spend:     10,
		Status:    "success",
		Metadata: map[string]any{
			"status":         "success",
			"cost_breakdown": map[string]any{"total_cost": 10.0},
		},
		MessagesEmptyObject: true, ResponseEmptyObject: true, ProxyServerRequestEmptyObject: true,
	}
	testRow := reference
	testRow.Spend = 10.05
	testRow.Metadata = map[string]any{
		"status":         "success",
		"cost_breakdown": map[string]any{"total_cost": 10.05},
	}

	report := CompareSnapshots(Snapshot{Raw: []RawRow{testRow}}, Snapshot{Raw: []RawRow{reference}}, Filter{})
	if !report.Equal {
		t.Fatalf("within-tolerance report diverged: %#v", report.Raw.Diffs)
	}

	testRow.Model = "different-backend"
	report = CompareSnapshots(Snapshot{Raw: []RawRow{testRow}}, Snapshot{Raw: []RawRow{reference}}, Filter{})
	if report.Equal || len(report.Raw.Diffs) == 0 {
		t.Fatalf("categorical difference was not reported: %#v", report)
	}
}

func TestCompareSnapshotsCountersAndDaily(t *testing.T) {
	t.Parallel()

	test := Snapshot{
		Counters: []MetricRow{{Key: "LiteLLM_UserTable|user_id=user-1", Values: map[string]float64{"spend": 1.01}}},
		Daily:    []MetricRow{{Key: "LiteLLM_DailyUserSpend|user_id=user-1|date=2026-07-12", Values: map[string]float64{"spend": 1, "api_requests": 2}}},
	}
	reference := Snapshot{
		Counters: []MetricRow{{Key: "LiteLLM_UserTable|user_id=user-1", Values: map[string]float64{"spend": 1}}},
		Daily:    []MetricRow{{Key: "LiteLLM_DailyUserSpend|user_id=user-1|date=2026-07-12", Values: map[string]float64{"spend": 1, "api_requests": 1}}},
	}

	report := CompareSnapshots(test, reference, Filter{})
	if report.Equal {
		t.Fatal("report.Equal = true, want counter and daily differences")
	}
	if len(report.Counters.Diffs) != 1 {
		t.Fatalf("counter diffs = %#v, want one", report.Counters.Diffs)
	}
	if len(report.Daily.Diffs) != 1 || report.Daily.Diffs[0].Field != "api_requests" {
		t.Fatalf("daily diffs = %#v, want api_requests", report.Daily.Diffs)
	}
}

func TestCompareSnapshotsIgnoresHopTimingAndAIRMetadataExtension(t *testing.T) {
	t.Parallel()

	referenceDuration := int64(120)
	testDuration := int64(80)
	referenceStart := time.Date(2026, 7, 12, 8, 30, 0, 0, time.UTC)
	testStart := referenceStart.Add(25 * time.Millisecond)
	referenceCompletion := referenceStart.Add(50 * time.Millisecond)
	testCompletion := testStart.Add(10 * time.Millisecond)
	reference := RawRow{
		RequestID:                     "resp-1",
		CallID:                        "call-1",
		StartTime:                     referenceStart,
		EndTime:                       referenceStart.Add(120 * time.Millisecond),
		RequestDurationMS:             &referenceDuration,
		CompletionStartTime:           &referenceCompletion,
		CallType:                      "acompletion",
		Spend:                         0.001,
		Status:                        "success",
		Metadata:                      map[string]any{"status": "success"},
		MessagesEmptyObject:           true,
		ResponseEmptyObject:           true,
		ProxyServerRequestEmptyObject: true,
	}
	testRow := reference
	testRow.StartTime = testStart
	testRow.EndTime = testStart.Add(80 * time.Millisecond)
	testRow.RequestDurationMS = &testDuration
	testRow.CompletionStartTime = &testCompletion
	testRow.Metadata = map[string]any{
		"status": "success",
		"spend_logs_metadata": map[string]any{
			"comparison_eligible": true,
			"actual_provider":     "synthetic-provider",
		},
	}

	report := CompareSnapshots(Snapshot{Raw: []RawRow{testRow}}, Snapshot{Raw: []RawRow{reference}}, Filter{})
	if !report.Equal {
		t.Fatalf("hop timing or AIR extension caused false divergence: %#v", report.Raw.Diffs)
	}
	if report.Semantics.RawTimingFieldsCompared {
		t.Fatal("report claims hop timing participates in equality")
	}
}

func TestCompareSnapshotsMarksExplicitlyIneligibleRowsIncomplete(t *testing.T) {
	t.Parallel()

	row := RawRow{
		RequestID: "resp-ineligible",
		CallID:    "call-ineligible",
		Metadata: map[string]any{
			"spend_logs_metadata": map[string]any{"comparison_eligible": false},
		},
		MessagesEmptyObject: true, ResponseEmptyObject: true, ProxyServerRequestEmptyObject: true,
	}
	report := CompareSnapshots(Snapshot{Raw: []RawRow{row}}, Snapshot{Raw: []RawRow{row}}, Filter{})
	if report.Equal {
		t.Fatal("explicitly ineligible rows must not produce a fully equal report")
	}
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "comparison_eligible=false") {
		t.Fatalf("warnings = %#v, want explicit eligibility warning", report.Warnings)
	}
}

func TestCompareSnapshotsDeclaresCumulativeScopes(t *testing.T) {
	t.Parallel()

	report := CompareSnapshots(Snapshot{}, Snapshot{}, Filter{})
	if report.Semantics.CounterScope != "cumulative_point_in_time" {
		t.Fatalf("counter scope = %q", report.Semantics.CounterScope)
	}
	if report.Semantics.DailyScope != "all_dimensions_for_raw_entity_api_key_on_full_utc_dates_intersecting_window" {
		t.Fatalf("daily scope = %q", report.Semantics.DailyScope)
	}
	if report.Semantics.AIRExtensionMetadataCompared {
		t.Fatal("AIR-only extension metadata must not participate in equality")
	}
	if !report.Semantics.AIRPrivacyContractChecked {
		t.Fatal("report must enforce AIR private-column empty-object contract")
	}
	if report.Semantics.RequestIDValuesCompared || report.Semantics.APIBaseValuesCompared || report.Semantics.RequesterIPValuesCompared {
		t.Fatal("intentional identifier/hop contract differences must not participate in equality")
	}
}

func TestCompareSnapshotsDoesNotClaimEqualityWithoutRawEvidence(t *testing.T) {
	t.Parallel()

	report := CompareSnapshots(Snapshot{}, Snapshot{}, Filter{})
	if report.Equal {
		t.Fatal("empty snapshots must not claim a successful comparison")
	}
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "no raw rows") {
		t.Fatalf("warnings = %#v, want no-evidence warning", report.Warnings)
	}
}

func TestCompareSnapshotsCorrelatesCallIDWithLegacyReferenceRequestID(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, 7, 12, 8, 30, 0, 0, time.UTC)
	testRow := RawRow{
		RequestID:           "provider-response-id",
		CallID:              "supplied-call-id",
		StartTime:           when,
		EndTime:             when.Add(time.Second),
		CallType:            "acompletion",
		Status:              "success",
		Metadata:            map[string]any{},
		MessagesEmptyObject: true, ResponseEmptyObject: true, ProxyServerRequestEmptyObject: true,
	}
	referenceRow := testRow
	referenceRow.RequestID = "supplied-call-id"
	referenceRow.CallID = ""

	report := CompareSnapshots(
		Snapshot{Raw: []RawRow{testRow}},
		Snapshot{Raw: []RawRow{referenceRow}},
		Filter{},
	)
	if len(report.Raw.MissingInTest) != 0 || len(report.Raw.MissingInReference) != 0 {
		t.Fatalf("legacy call correlation reported missing rows: %#v", report.Raw)
	}
	if len(report.Raw.Diffs) != 0 {
		t.Fatalf("identifier values are correlation inputs, not equality fields: %#v", report.Raw.Diffs)
	}
}

func TestCompareSnapshotsChecksAIRPrivacyWithoutRequiringReferenceShape(t *testing.T) {
	t.Parallel()

	base := RawRow{
		RequestID: "resp-privacy", CallID: "call-privacy", Status: "success", Metadata: map[string]any{},
		MessagesEmptyObject: true, ResponseEmptyObject: true, ProxyServerRequestEmptyObject: true,
	}
	reference := base
	reference.MessagesEmptyObject = false
	reference.ResponseEmptyObject = false
	reference.ProxyServerRequestEmptyObject = false
	report := CompareSnapshots(Snapshot{Raw: []RawRow{base}}, Snapshot{Raw: []RawRow{reference}}, Filter{})
	if !report.Equal {
		t.Fatalf("reference privacy representation must not fail AIR contract: %#v", report.Raw.Diffs)
	}

	base.ResponseEmptyObject = false
	report = CompareSnapshots(Snapshot{Raw: []RawRow{base}}, Snapshot{Raw: []RawRow{reference}}, Filter{})
	if report.Equal || len(report.Raw.Diffs) != 1 || report.Raw.Diffs[0].Field != "air_contract.response_is_empty_object" {
		t.Fatalf("AIR privacy violation was not reported: %#v", report.Raw.Diffs)
	}
}

func TestCompareSnapshotsNormalizesNoCacheMarkers(t *testing.T) {
	t.Parallel()

	testRow := RawRow{
		RequestID: "resp-cache", CallID: "call-cache", Status: "success", CacheHit: "False", Metadata: map[string]any{},
		MessagesEmptyObject: true, ResponseEmptyObject: true, ProxyServerRequestEmptyObject: true,
	}
	reference := testRow
	reference.CacheHit = "None"
	report := CompareSnapshots(Snapshot{Raw: []RawRow{testRow}}, Snapshot{Raw: []RawRow{reference}}, Filter{})
	if !report.Equal {
		t.Fatalf("False and None both mean no cache hit: %#v", report.Raw.Diffs)
	}
	reference.CacheHit = "True"
	report = CompareSnapshots(Snapshot{Raw: []RawRow{testRow}}, Snapshot{Raw: []RawRow{reference}}, Filter{})
	if report.Equal || len(report.Raw.Diffs) != 1 || report.Raw.Diffs[0].Field != "cache_hit" {
		t.Fatalf("real cache hit difference was not reported: %#v", report.Raw.Diffs)
	}
}
