package spendcompare

import "math"

const (
	absoluteCostTolerance = 0.000001
	relativeCostTolerance = 0.005
)

func CostTolerance(reference float64) float64 {
	return math.Max(absoluteCostTolerance, math.Abs(reference)*relativeCostTolerance)
}

func CostsEqual(actual, reference float64) bool {
	tolerance := CostTolerance(reference)
	epsilon := 1e-12 * math.Max(1, math.Abs(reference))
	return math.Abs(actual-reference) <= tolerance+epsilon
}
