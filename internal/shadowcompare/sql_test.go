package shadowcompare

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestBuildRawQueryAlwaysUsesIndexedBounds(t *testing.T) {
	t.Parallel()

	window, err := NewWindow(
		time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		filter Filter
		want   []string
		args   int
	}{
		{name: "bounds only", filter: Filter{Window: window}, args: 2},
		{
			name:   "request id",
			filter: Filter{Window: window, RequestID: "resp-1"},
			want:   []string{"request_id = $3"},
			args:   3,
		},
		{
			name:   "call id",
			filter: Filter{Window: window, CallID: "call-1"},
			want:   []string{"metadata ->> 'litellm_call_id' = $3"},
			args:   3,
		},
		{
			name:   "both ids",
			filter: Filter{Window: window, RequestID: "resp-1", CallID: "call-1"},
			want:   []string{"request_id = $3", "metadata ->> 'litellm_call_id' = $4"},
			args:   4,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			query, args, err := BuildRawQuery(tc.filter)
			if err != nil {
				t.Fatalf("BuildRawQuery: %v", err)
			}
			for _, fragment := range []string{
				`"startTime" >= $1`,
				`"startTime" < $2`,
				`ORDER BY "startTime", request_id`,
				`LIMIT 100001`,
			} {
				if !strings.Contains(query, fragment) {
					t.Errorf("query missing %q:\n%s", fragment, query)
				}
			}
			for _, fragment := range tc.want {
				if !strings.Contains(query, fragment) {
					t.Errorf("query missing %q:\n%s", fragment, query)
				}
			}
			if len(args) != tc.args {
				t.Fatalf("len(args) = %d, want %d", len(args), tc.args)
			}
		})
	}
}

func TestReadOnlySQLSurface(t *testing.T) {
	t.Parallel()

	if SetTransactionReadOnlySQL != "SET TRANSACTION READ ONLY" {
		t.Fatalf("SetTransactionReadOnlySQL = %q", SetTransactionReadOnlySQL)
	}

	for name, query := range ReadOnlySQLTemplates() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			upper := strings.ToUpper(strings.TrimSpace(query))
			if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
				t.Fatalf("read query must start with SELECT or WITH:\n%s", query)
			}
			forbidden := regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|MERGE|UPSERT|CREATE|ALTER|DROP|TRUNCATE|COPY|CALL|DO|GRANT|REVOKE|LOCK|VACUUM|REINDEX|REFRESH)\b|\bFOR\s+(UPDATE|SHARE)\b`)
			if forbidden.MatchString(query) {
				t.Fatalf("query contains write-capable SQL:\n%s", query)
			}
		})
	}
}

func TestSetTransactionReadOnlyExecutesExactSQL(t *testing.T) {
	t.Parallel()

	recorder := &recordingExecutor{}
	if err := setTransactionReadOnly(context.Background(), recorder); err != nil {
		t.Fatalf("setTransactionReadOnly: %v", err)
	}
	if recorder.query != SetTransactionReadOnlySQL {
		t.Fatalf("executed %q, want %q", recorder.query, SetTransactionReadOnlySQL)
	}
	if len(recorder.args) != 0 {
		t.Fatalf("read-only command args = %#v, want none", recorder.args)
	}
}

func TestReadOnlyConnectionConfig(t *testing.T) {
	t.Parallel()

	config, err := readOnlyConnectionConfig("postgres://user:secret@example.invalid/test-db")
	if err != nil {
		t.Fatalf("readOnlyConnectionConfig: %v", err)
	}
	if got := config.RuntimeParams["default_transaction_read_only"]; got != "on" {
		t.Fatalf("default_transaction_read_only = %q, want on", got)
	}
	if got := config.RuntimeParams["application_name"]; got != "air-shadow-compare" {
		t.Fatalf("application_name = %q", got)
	}
}

func TestCounterQueriesAreIdentityScoped(t *testing.T) {
	t.Parallel()

	for name, query := range ReadOnlySQLTemplates() {
		if !strings.HasPrefix(name, "counter_") && name != "team_membership_counter" {
			continue
		}
		if !strings.Contains(query, "ANY(") && !strings.Contains(query, "unnest(") {
			t.Fatalf("counter query %s is not identity scoped:\n%s", name, query)
		}
	}
}

func TestDailyQueriesAreBoundedAndScoped(t *testing.T) {
	t.Parallel()

	for name, query := range DailySQLTemplates() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, fragment := range []string{"date >=", "date <=", "unnest(", "entity_id", "api_key"} {
				if !strings.Contains(query, fragment) {
					t.Fatalf("daily query missing %q:\n%s", fragment, query)
				}
			}
			for _, forbidden := range []string{"w.model", "w.provider", "w.mcp_tool", "w.endpoint"} {
				if strings.Contains(query, forbidden) {
					t.Fatalf("daily query still hides arbitrary surplus behind %q:\n%s", forbidden, query)
				}
			}
			for _, nullable := range []string{"COALESCE(d.model", "COALESCE(d.model_group", "COALESCE(d.custom_llm_provider", "COALESCE(d.mcp_namespaced_tool_name", "COALESCE(d.endpoint"} {
				if strings.Contains(query, nullable) {
					t.Fatalf("daily query collapses SQL NULL and empty through %q:\n%s", nullable, query)
				}
			}
		})
	}
}

func TestDailyTagQueryIgnoresRepresentativeRequestID(t *testing.T) {
	t.Parallel()

	query := dailyQuery("LiteLLM_DailyTagSpend", "tag")
	if strings.Contains(query, "d.request_id") {
		t.Fatalf("daily tag request_id is not an aggregate dimension:\n%s", query)
	}
}

type recordingExecutor struct {
	query string
	args  []any
}

func (r *recordingExecutor) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	r.query = query
	r.args = args
	return pgconn.CommandTag{}, nil
}
