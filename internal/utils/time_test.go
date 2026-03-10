package utils

import (
	"testing"
	"time"
)

func TestNowUTC(t *testing.T) {
	// Test that NowUTC returns a time in UTC
	result := NowUTC()

	if result.Location() != time.UTC {
		t.Errorf("expected UTC location, got %v", result.Location())
	}

	// Test that the time is recent (within last minute)
	now := time.Now().UTC()
	diff := now.Sub(result)
	if diff > time.Minute || diff < -time.Minute {
		t.Errorf("expected recent time, got %v", result)
	}
}

func TestNowUTC_Consistency(t *testing.T) {
	// Test that multiple calls return consistent results
	// (they should be very close together in time)
	result1 := NowUTC()
	result2 := NowUTC()

	// Both should be in UTC
	if result1.Location() != time.UTC {
		t.Errorf("result1 not in UTC: %v", result1.Location())
	}
	if result2.Location() != time.UTC {
		t.Errorf("result2 not in UTC: %v", result2.Location())
	}

	// The difference should be minimal
	diff := result2.Sub(result1)
	if diff < 0 {
		diff = -diff
	}
	if diff > time.Second {
		t.Errorf("too large difference between calls: %v", diff)
	}
}
