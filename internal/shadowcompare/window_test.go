package shadowcompare

import (
	"testing"
	"time"
)

func TestParseUTC(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"2026-07-12T08:00:00Z",
		"2026-07-12T08:00:00.123456Z",
		"2026-07-12T08:00:00+00:00",
	} {
		t.Run("accepts_"+value, func(t *testing.T) {
			t.Parallel()
			got, err := ParseUTC(value)
			if err != nil {
				t.Fatalf("ParseUTC(%q): %v", value, err)
			}
			if got.Location() != time.UTC {
				t.Fatalf("location = %v, want UTC", got.Location())
			}
		})
	}

	for _, value := range []string{
		"",
		"2026-07-12",
		"2026-07-12T11:00:00+03:00",
	} {
		t.Run("rejects_"+value, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseUTC(value); err == nil {
				t.Fatalf("ParseUTC(%q) succeeded, want error", value)
			}
		})
	}
}

func TestNewWindow(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		from    time.Time
		to      time.Time
		wantErr bool
	}{
		{name: "one second", from: from, to: from.Add(time.Second)},
		{name: "exactly 24h", from: from, to: from.Add(24 * time.Hour)},
		{name: "zero from", to: from, wantErr: true},
		{name: "zero to", from: from, wantErr: true},
		{name: "empty", from: from, to: from, wantErr: true},
		{name: "reversed", from: from, to: from.Add(-time.Second), wantErr: true},
		{name: "over 24h", from: from, to: from.Add(24*time.Hour + time.Nanosecond), wantErr: true},
		{
			name:    "non UTC from",
			from:    from.In(time.FixedZone("MSK", 3*60*60)),
			to:      from.Add(time.Hour),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			window, err := NewWindow(tc.from, tc.to)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NewWindow(%v, %v) succeeded, want error", tc.from, tc.to)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewWindow(%v, %v): %v", tc.from, tc.to, err)
			}
			if window.From != tc.from || window.To != tc.to {
				t.Fatalf("window = %#v, want from=%v to=%v", window, tc.from, tc.to)
			}
		})
	}
}
