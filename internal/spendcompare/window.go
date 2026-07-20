package spendcompare

import (
	"errors"
	"fmt"
	"time"
)

const MaxWindow = 24 * time.Hour

var (
	ErrWindowRequired = errors.New("comparison window requires --from and --to")
	ErrWindowUTC      = errors.New("comparison bounds must be UTC")
	ErrWindowOrder    = errors.New("comparison --to must be after --from")
	ErrWindowTooWide  = errors.New("comparison window must not exceed 24 hours")
)

// ParseUTC parses RFC3339/RFC3339Nano and rejects non-zero UTC offsets.
func ParseUTC(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, ErrWindowRequired
	}
	timestamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse UTC timestamp: %w", err)
	}
	_, offset := timestamp.Zone()
	if offset != 0 {
		return time.Time{}, ErrWindowUTC
	}
	return timestamp.UTC(), nil
}

// NewWindow validates a half-open UTC interval with a hard 24-hour maximum.
func NewWindow(from, to time.Time) (Window, error) {
	if from.IsZero() || to.IsZero() {
		return Window{}, ErrWindowRequired
	}
	if from.Location() != time.UTC || to.Location() != time.UTC {
		return Window{}, ErrWindowUTC
	}
	if !to.After(from) {
		return Window{}, ErrWindowOrder
	}
	if to.Sub(from) > MaxWindow {
		return Window{}, ErrWindowTooWide
	}
	return Window{From: from, To: to}, nil
}

func (f Filter) Validate() error {
	_, err := NewWindow(f.Window.From, f.Window.To)
	return err
}
