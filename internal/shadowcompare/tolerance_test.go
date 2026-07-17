package shadowcompare

import (
	"math"
	"testing"
)

func TestCostTolerance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		reference float64
		want      float64
	}{
		{name: "absolute floor at zero", reference: 0, want: 0.000001},
		{name: "absolute floor for small cost", reference: 0.0001, want: 0.000001},
		{name: "half percent", reference: 10, want: 0.05},
		{name: "negative reference uses magnitude", reference: -10, want: 0.05},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := CostTolerance(tc.reference); math.Abs(got-tc.want) > 1e-12 {
				t.Fatalf("CostTolerance(%v) = %v, want %v", tc.reference, got, tc.want)
			}
		})
	}
}

func TestCostsEqual(t *testing.T) {
	t.Parallel()

	if !CostsEqual(10.05, 10) {
		t.Fatal("difference at 0.5% boundary must be accepted")
	}
	if CostsEqual(10.050001, 10) {
		t.Fatal("difference above 0.5% boundary must be rejected")
	}
	if !CostsEqual(0.000001, 0) {
		t.Fatal("difference at absolute floor must be accepted")
	}
	if CostsEqual(0.0000011, 0) {
		t.Fatal("difference above absolute floor must be rejected")
	}
}
