package shadowcompare

import "time"

const ContractVersion = "air-shadow-spend/v1"

// Window is a required half-open UTC interval [From, To).
type Window struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Filter narrows the comparison. Window is always required for database reads.
type Filter struct {
	Window    Window `json:"window"`
	RequestID string `json:"request_id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
}

// RawRow is the normalized subset of LiteLLM_SpendLogs used by the comparer.
type RawRow struct {
	RequestID                     string
	CallID                        string
	CallType                      string
	APIKey                        string
	Spend                         float64
	TotalTokens                   int64
	PromptTokens                  int64
	CompletionTokens              int64
	StartTime                     time.Time
	EndTime                       time.Time
	RequestDurationMS             *int64
	CompletionStartTime           *time.Time
	Model                         string
	ModelID                       string
	ModelGroup                    string
	CustomLLMProvider             string
	APIBase                       string
	User                          string
	Metadata                      map[string]any
	CacheHit                      string
	CacheKey                      string
	RequestTags                   []string
	TeamID                        string
	OrganizationID                string
	EndUser                       string
	RequesterIP                   string
	SessionID                     string
	Status                        string
	MCPNamespacedToolName         string
	AgentID                       string
	MessagesEmptyObject           bool
	ResponseEmptyObject           bool
	ProxyServerRequestEmptyObject bool
}

// MetricRow represents one targeted counter or daily aggregate row.
type MetricRow struct {
	Key    string
	Labels map[string]any
	Values map[string]float64
}

// Snapshot is the complete read-only result for one database.
type Snapshot struct {
	Raw      []RawRow
	Counters []MetricRow
	Daily    []MetricRow
}

type Difference struct {
	Key       string   `json:"key"`
	Field     string   `json:"field"`
	Test      any      `json:"test"`
	Reference any      `json:"reference"`
	Tolerance *float64 `json:"tolerance,omitempty"`
}

type Duplicate struct {
	Database   string   `json:"database"`
	Key        string   `json:"key"`
	Count      int      `json:"count"`
	RequestIDs []string `json:"request_ids,omitempty"`
}

type RawReport struct {
	TestRows           int          `json:"test_rows"`
	ReferenceRows      int          `json:"reference_rows"`
	MissingInTest      []string     `json:"missing_in_test"`
	MissingInReference []string     `json:"missing_in_reference"`
	Duplicates         []Duplicate  `json:"duplicates"`
	Diffs              []Difference `json:"diffs"`
}

type SectionReport struct {
	TestRows           int          `json:"test_rows"`
	ReferenceRows      int          `json:"reference_rows"`
	MissingInTest      []string     `json:"missing_in_test"`
	MissingInReference []string     `json:"missing_in_reference"`
	Duplicates         []Duplicate  `json:"duplicates"`
	Diffs              []Difference `json:"diffs"`
}

// ReportSemantics makes the scope behind Equal machine-readable. AIR and
// LiteLLM observe different network hops, while counters and daily tables do
// not contain window-local deltas.
type ReportSemantics struct {
	RawTimingFieldsCompared      bool   `json:"raw_timing_fields_compared"`
	RequestIDValuesCompared      bool   `json:"request_id_values_compared"`
	APIBaseValuesCompared        bool   `json:"api_base_values_compared"`
	RequesterIPValuesCompared    bool   `json:"requester_ip_values_compared"`
	AIRPrivacyContractChecked    bool   `json:"air_privacy_contract_checked"`
	AIRExtensionMetadataCompared bool   `json:"air_extension_metadata_compared"`
	MetadataComparison           string `json:"metadata_comparison"`
	CounterScope                 string `json:"counter_scope"`
	DailyScope                   string `json:"daily_scope"`
}

// Report is emitted as JSON by cmd/shadow-compare.
type Report struct {
	ContractVersion string          `json:"contract_version,omitempty"`
	GeneratedAt     time.Time       `json:"generated_at,omitempty"`
	Window          Window          `json:"window"`
	RequestID       string          `json:"request_id,omitempty"`
	CallID          string          `json:"call_id,omitempty"`
	Semantics       ReportSemantics `json:"semantics"`
	Equal           bool            `json:"equal"`
	Raw             RawReport       `json:"raw"`
	Counters        SectionReport   `json:"counters"`
	Daily           SectionReport   `json:"daily"`
	Warnings        []string        `json:"warnings,omitempty"`
}
