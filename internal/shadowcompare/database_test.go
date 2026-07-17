package shadowcompare

import (
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestBuildQueryScopeUsesRawIdentityDateAndAPIKeyAnchors(t *testing.T) {
	t.Parallel()

	row := RawRow{
		CallType:              "acompletion",
		APIKey:                "key-hash",
		User:                  "user-1",
		TeamID:                "team-1",
		OrganizationID:        "org-1",
		EndUser:               "end-user-1",
		AgentID:               "agent-1",
		RequestTags:           []string{"tag-b", "tag-a"},
		StartTime:             time.Date(2026, 7, 12, 8, 30, 0, 0, time.UTC),
		Model:                 "backend-model",
		CustomLLMProvider:     "openai",
		MCPNamespacedToolName: "",
	}

	scope := buildQueryScope([]RawRow{row})
	wantCounters := map[string][]string{
		"LiteLLM_VerificationToken": {"key-hash"},
		"LiteLLM_UserTable":         {"user-1"},
		"LiteLLM_TeamTable":         {"team-1"},
		"LiteLLM_OrganizationTable": {"org-1"},
		"LiteLLM_EndUserTable":      {"end-user-1"},
		"LiteLLM_TagTable":          {"tag-a", "tag-b"},
		"LiteLLM_AgentsTable":       {"agent-1"},
	}
	if !reflect.DeepEqual(scope.CounterValues, wantCounters) {
		t.Fatalf("counter scope = %#v, want %#v", scope.CounterValues, wantCounters)
	}
	if !reflect.DeepEqual(scope.Memberships, []membershipKey{{UserID: "user-1", TeamID: "team-1"}}) {
		t.Fatalf("memberships = %#v", scope.Memberships)
	}
	for _, table := range []string{
		"LiteLLM_DailyUserSpend",
		"LiteLLM_DailyTeamSpend",
		"LiteLLM_DailyOrganizationSpend",
		"LiteLLM_DailyEndUserSpend",
		"LiteLLM_DailyAgentSpend",
	} {
		if len(scope.Daily[table]) != 1 {
			t.Fatalf("daily scope %s = %#v, want one row", table, scope.Daily[table])
		}
		want := dailyAnchor{Entity: map[string]string{
			"LiteLLM_DailyUserSpend":         "user-1",
			"LiteLLM_DailyTeamSpend":         "team-1",
			"LiteLLM_DailyOrganizationSpend": "org-1",
			"LiteLLM_DailyEndUserSpend":      "end-user-1",
			"LiteLLM_DailyAgentSpend":        "agent-1",
		}[table], Date: "2026-07-12", APIKey: "key-hash"}
		if got := scope.Daily[table][0]; got != want {
			t.Fatalf("daily anchor for %s = %#v, want %#v", table, got, want)
		}
	}
	if len(scope.Daily["LiteLLM_DailyTagSpend"]) != 2 {
		t.Fatalf("tag daily scope = %#v, want two rows", scope.Daily["LiteLLM_DailyTagSpend"])
	}
	if len(scope.Warnings) != 0 {
		t.Fatalf("warnings = %#v", scope.Warnings)
	}
}

func TestBuildQueryScopeMarksUnsupportedCallTypeIncomplete(t *testing.T) {
	t.Parallel()

	scope := buildQueryScope([]RawRow{{
		CallType:  "unknown-route",
		APIKey:    "key-hash",
		User:      "user-1",
		StartTime: time.Date(2026, 7, 12, 8, 30, 0, 0, time.UTC),
	}})
	if len(scope.Warnings) != 1 {
		t.Fatalf("warnings = %#v, want one", scope.Warnings)
	}
	if len(scope.Daily["LiteLLM_DailyUserSpend"]) != 0 {
		t.Fatalf("unsupported route produced daily scope: %#v", scope.Daily)
	}
}

func TestBuildQueryScopeDeduplicatesFailureDimensionVariantsIntoOneAnchor(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, 7, 12, 8, 30, 0, 0, time.UTC)
	air := RawRow{
		CallType: "", Status: "failure", APIKey: "key-hash", User: "user-1", StartTime: when,
		Model: "backend-model", ModelGroup: "openai/public-model", CustomLLMProvider: "openai",
		MCPNamespacedToolName: "server/tool",
	}
	liteLLM := air
	liteLLM.Model = "openai/public-model"
	liteLLM.CustomLLMProvider = ""

	scope := buildQueryScope([]RawRow{air}, []RawRow{liteLLM})
	if len(scope.Warnings) != 0 {
		t.Fatalf("warnings = %#v, want none for LiteLLM failure semantics", scope.Warnings)
	}
	wanted := scope.Daily["LiteLLM_DailyUserSpend"]
	if len(wanted) != 1 {
		t.Fatalf("failure daily scope = %#v, want one exhaustive anchor", wanted)
	}
	want := dailyAnchor{Entity: "user-1", Date: "2026-07-12", APIKey: "key-hash"}
	if wanted[0] != want {
		t.Fatalf("failure daily anchor = %#v, want %#v", wanted[0], want)
	}
}

func TestBuildQueryScopeDoesNotUseKnownDimensionsToLimitDailyRows(t *testing.T) {
	t.Parallel()

	when := time.Date(2026, 7, 12, 8, 30, 0, 0, time.UTC)
	rows := []RawRow{
		{CallType: "acompletion", APIKey: "key-hash", User: "user-1", StartTime: when,
			Model: "model-a", CustomLLMProvider: "openai"},
		{CallType: "acompletion", APIKey: "key-hash", User: "user-1", StartTime: when,
			Model: "model-b", CustomLLMProvider: "anthropic", MCPNamespacedToolName: "server/tool"},
	}

	scope := buildQueryScope(rows)
	wanted := scope.Daily["LiteLLM_DailyUserSpend"]
	if len(wanted) != 1 {
		t.Fatalf("daily scope = %#v, want one entity/date/key anchor", wanted)
	}
	if got, want := wanted[0], (dailyAnchor{Entity: "user-1", Date: "2026-07-12", APIKey: "key-hash"}); got != want {
		t.Fatalf("daily anchor = %#v, want %#v", got, want)
	}
}

func TestNullableMetricTextDistinguishesSQLNullFromEmpty(t *testing.T) {
	t.Parallel()

	nullText := nullableMetricText{}
	emptyText := nullableMetricText{Value: "", Valid: true}
	if nullText.KeyValue() == emptyText.KeyValue() {
		t.Fatalf("SQL NULL and empty string collapsed to %q", nullText.KeyValue())
	}
	if nullText.LabelValue() != nil {
		t.Fatalf("SQL NULL label = %#v, want nil", nullText.LabelValue())
	}
	if got := emptyText.LabelValue(); got != "" {
		t.Fatalf("empty label = %#v, want empty string", got)
	}
}

func TestScanDailyRowPreservesNullableDimensions(t *testing.T) {
	t.Parallel()

	nullRow, err := scanDailyRow(dailyScanner{
		endpoint:   pgtype.Text{},
		modelGroup: pgtype.Text{},
	}, "LiteLLM_DailyUserSpend", "user_id")
	if err != nil {
		t.Fatal(err)
	}
	emptyRow, err := scanDailyRow(dailyScanner{
		endpoint:   pgtype.Text{String: "", Valid: true},
		modelGroup: pgtype.Text{String: "", Valid: true},
	}, "LiteLLM_DailyUserSpend", "user_id")
	if err != nil {
		t.Fatal(err)
	}

	if nullRow.Key == emptyRow.Key {
		t.Fatalf("NULL and empty endpoint collapsed to %q", nullRow.Key)
	}
	if nullRow.Labels["model_group"] != nil {
		t.Fatalf("NULL model_group = %#v, want nil", nullRow.Labels["model_group"])
	}
	if got := emptyRow.Labels["model_group"]; got != "" {
		t.Fatalf("empty model_group = %#v, want empty string", got)
	}

	report := compareMetricRows([]MetricRow{nullRow, emptyRow}, []MetricRow{nullRow})
	if len(report.MissingInReference) != 1 || report.MissingInReference[0] != emptyRow.Key {
		t.Fatalf("nullable coexistence report = %#v, want empty endpoint as surplus", report)
	}
}

type dailyScanner struct {
	endpoint   pgtype.Text
	modelGroup pgtype.Text
}

func (scanner dailyScanner) Scan(dest ...any) error {
	*dest[0].(*pgtype.Text) = pgtype.Text{String: "user-1", Valid: true}
	*dest[1].(*string) = "2026-07-12"
	*dest[2].(*pgtype.Text) = pgtype.Text{String: "key-hash", Valid: true}
	*dest[3].(*pgtype.Text) = pgtype.Text{String: "model-1", Valid: true}
	*dest[4].(*pgtype.Text) = scanner.modelGroup
	*dest[5].(*pgtype.Text) = pgtype.Text{String: "openai", Valid: true}
	*dest[6].(*pgtype.Text) = pgtype.Text{String: "", Valid: true}
	*dest[7].(*pgtype.Text) = scanner.endpoint
	*dest[8].(*string) = ""
	for _, index := range []int{9, 10, 11, 12, 14, 15, 16} {
		*dest[index].(*int64) = 0
	}
	*dest[13].(*float64) = 0
	return nil
}

func TestBuildQueryScopeWarnsForEmptyNonFailureCallType(t *testing.T) {
	t.Parallel()

	scope := buildQueryScope([]RawRow{{
		CallType: "", Status: "success", APIKey: "key-hash", User: "user-1",
		StartTime: time.Date(2026, 7, 12, 8, 30, 0, 0, time.UTC),
	}})
	if len(scope.Warnings) != 1 {
		t.Fatalf("warnings = %#v, want incomplete-scope warning", scope.Warnings)
	}
	if len(scope.Daily["LiteLLM_DailyUserSpend"]) != 0 {
		t.Fatalf("invalid success row produced daily scope: %#v", scope.Daily)
	}
}
