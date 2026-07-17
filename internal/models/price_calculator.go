package models

import (
	"github.com/mixaill76/auto_ai_router/internal/converter"
)

const (
	tokenTiering200kThreshold = 200_000
	tokenTiering272kThreshold = 272_000
)

// CalculateTokenCosts preserves the production cost-calculation contract.
// Returns nil if price is nil (model not found in pricing database).
//
// IMPORTANT: Handles two token counting semantics:
//  1. Vertex/OpenAI: Audio/Cached/Reasoning tokens are INCLUDED in PromptTokens/CompletionTokens
//     (Example: PromptTokens=100 includes AudioInputTokens=5 and CachedInputTokens=20)
//  2. Anthropic: Cached tokens are SEPARATE from InputTokens
//     (Example: InputTokens=100 + CachedInputTokens=20 = 120 total)
//
// Solution: Calculate "regular" tokens by subtracting detail breakdowns from totals,
// then add back specialized token costs. This works for both semantics:
// - Vertex/OpenAI: 100 - 5 - 20 = 75 regular, then add audio and cached separately
// - Anthropic: 100 - 0 - 0 = 100 regular (since those tokens are in separate fields)
func CalculateTokenCosts(usage *converter.TokenUsage, price *ModelPrice) *converter.TokenCosts {
	return calculateTokenCosts(usage, price, false)
}

// CalculateShadowTokenCosts calculates the isolated LiteLLM-parity spend. It
// can evolve independently while comparison data is collected, without
// changing the production billing calculation.
func CalculateShadowTokenCosts(usage *converter.TokenUsage, price *ModelPrice) *converter.TokenCosts {
	return calculateTokenCosts(usage, price, true)
}

func calculateTokenCosts(usage *converter.TokenUsage, price *ModelPrice, isolatedSpend bool) *converter.TokenCosts {
	if usage == nil || price == nil {
		return nil
	}

	costs := &converter.TokenCosts{}
	longContext272k := usage.PromptTokens > tokenTiering272kThreshold
	inputCostPerToken := price.InputCostPerToken
	if longContext272k && price.InputCostPerTokenAbove272k > 0 {
		inputCostPerToken = price.InputCostPerTokenAbove272k
	}
	outputCostPerToken := price.OutputCostPerToken
	if longContext272k && price.OutputCostPerTokenAbove272k > 0 {
		outputCostPerToken = price.OutputCostPerTokenAbove272k
	}

	// Calculate "regular" input tokens by subtracting specialized token types.
	// Vertex/OpenAI: audio/cached tokens are included in PromptTokens; Anthropic: same + cache creation.
	regularInputTokens := usage.PromptTokens - usage.AudioInputTokens - usage.CachedInputTokens -
		usage.CacheCreationTokens - usage.ImageTokens
	if regularInputTokens < 0 {
		regularInputTokens = 0
	}

	// Regular input with 200k tiering
	if longContext272k && price.InputCostPerTokenAbove272k > 0 {
		costs.InputCost = float64(regularInputTokens) * inputCostPerToken
	} else if price.InputCostPerTokenAbove200k > 0 && usage.PromptTokens > tokenTiering200kThreshold {
		above := usage.PromptTokens - tokenTiering200kThreshold
		// Distribute regular tokens proportionally between below/above threshold
		regularAbove := int(int64(regularInputTokens) * int64(above) / int64(usage.PromptTokens))
		regularBelow := regularInputTokens - regularAbove
		costs.InputCost = float64(regularBelow)*price.InputCostPerToken +
			float64(regularAbove)*price.InputCostPerTokenAbove200k
	} else {
		costs.InputCost = float64(regularInputTokens) * inputCostPerToken
	}

	// Calculate "regular" output tokens by subtracting specialized token types
	regularOutputTokens := usage.CompletionTokens - usage.AudioOutputTokens - usage.ReasoningTokens -
		usage.AcceptedPredictionTokens - usage.RejectedPredictionTokens - usage.OutputImageTokens
	if isolatedSpend {
		regularOutputTokens -= usage.CachedOutputTokens
	}
	if regularOutputTokens < 0 {
		regularOutputTokens = 0
	}

	// Regular output with 200k tiering
	if longContext272k && price.OutputCostPerTokenAbove272k > 0 {
		costs.OutputCost = float64(regularOutputTokens) * outputCostPerToken
	} else if price.OutputCostPerTokenAbove200k > 0 && usage.CompletionTokens > tokenTiering200kThreshold {
		above := usage.CompletionTokens - tokenTiering200kThreshold
		// Distribute regular tokens proportionally between below/above threshold
		regularAbove := int(int64(regularOutputTokens) * int64(above) / int64(usage.CompletionTokens))
		regularBelow := regularOutputTokens - regularAbove
		costs.OutputCost = float64(regularBelow)*price.OutputCostPerToken +
			float64(regularAbove)*price.OutputCostPerTokenAbove200k
	} else {
		costs.OutputCost = float64(regularOutputTokens) * outputCostPerToken
	}

	// Audio tokens with fallback to regular tokens
	audioInputCost := price.InputCostPerAudioToken
	if audioInputCost == 0 {
		audioInputCost = inputCostPerToken
	}
	costs.AudioInputCost = float64(usage.AudioInputTokens) * audioInputCost

	audioOutputCost := price.OutputCostPerAudioToken
	if audioOutputCost == 0 {
		audioOutputCost = outputCostPerToken
	}
	costs.AudioOutputCost = float64(usage.AudioOutputTokens) * audioOutputCost

	// Cached read tokens: prefer explicit cached price, fall back to LiteLLM alias,
	// then fall back to regular rate (no discount known).
	cachedInputCost := price.InputCostPerCachedToken
	if cachedInputCost == 0 {
		cachedInputCost = price.CacheReadInputTokenCost
	}
	if longContext272k && price.CacheReadInputTokenCostAbove272k > 0 {
		cachedInputCost = price.CacheReadInputTokenCostAbove272k
	}
	if cachedInputCost == 0 {
		cachedInputCost = inputCostPerToken
	}
	costs.CachedInputCost = float64(usage.CachedInputTokens) * cachedInputCost

	cacheCreationCost := price.CacheCreationInputTokenCost
	if longContext272k && price.CacheCreationInputTokenCostAbove272k > 0 {
		cacheCreationCost = price.CacheCreationInputTokenCostAbove272k
	}
	if cacheCreationCost == 0 {
		cacheCreationCost = inputCostPerToken
	}
	costs.CacheCreationCost = float64(usage.CacheCreationTokens) * cacheCreationCost

	cachedOutputCost := price.OutputCostPerCachedToken
	if cachedOutputCost == 0 {
		cachedOutputCost = outputCostPerToken
	}
	costs.CachedOutputCost = float64(usage.CachedOutputTokens) * cachedOutputCost

	// Reasoning tokens with fallback
	reasoningCost := price.OutputCostPerReasoningToken
	if reasoningCost == 0 {
		reasoningCost = outputCostPerToken
	}
	costs.ReasoningCost = float64(usage.ReasoningTokens) * reasoningCost

	// Prediction tokens with fallback (accepted tokens)
	predictionCost := price.OutputCostPerPredictionToken
	if predictionCost == 0 {
		predictionCost = outputCostPerToken
	}
	costs.PredictionCost = float64(usage.AcceptedPredictionTokens) * predictionCost

	// Rejected prediction tokens count as regular output tokens
	costs.PredictionCost += float64(usage.RejectedPredictionTokens) * outputCostPerToken

	if isolatedSpend {
		// The isolated calculation exposes image directions separately and
		// follows LiteLLM's per-image precedence for image-generation calls.
		inputImagePrice := price.InputCostPerImageToken
		if inputImagePrice == 0 {
			inputImagePrice = price.InputCostPerToken
		}
		costs.InputImageCost = float64(usage.ImageTokens) * inputImagePrice

		if usage.ImageCount > 0 && price.OutputCostPerImage > 0 {
			costs.OutputImageCost = float64(usage.ImageCount) * price.OutputCostPerImage
		} else {
			outputImagePrice := price.OutputCostPerImageToken
			if outputImagePrice == 0 {
				outputImagePrice = price.OutputCostPerToken
			}
			costs.OutputImageCost = float64(usage.OutputImageTokens) * outputImagePrice
		}
		costs.ImageCost = costs.InputImageCost + costs.OutputImageCost
	} else {
		// Preserve the existing production precedence and aggregate image field.
		inputImagePrice := price.InputCostPerImageToken
		if inputImagePrice == 0 {
			inputImagePrice = inputCostPerToken
		}
		costs.ImageCost = float64(usage.ImageTokens) * inputImagePrice

		if usage.OutputImageTokens > 0 {
			outputImagePrice := price.OutputCostPerImageToken
			if outputImagePrice == 0 {
				outputImagePrice = outputCostPerToken
			}
			costs.ImageCost += float64(usage.OutputImageTokens) * outputImagePrice
		} else if usage.ImageCount > 0 && price.OutputCostPerImage > 0 {
			costs.ImageCost += float64(usage.ImageCount) * price.OutputCostPerImage
		}
	}

	// Calculate total
	costs.TotalCost = costs.InputCost +
		costs.OutputCost +
		costs.AudioInputCost +
		costs.AudioOutputCost +
		costs.ReasoningCost +
		costs.CachedInputCost +
		costs.CacheCreationCost +
		costs.CachedOutputCost +
		costs.PredictionCost +
		costs.ImageCost

	return costs
}

// CalculateCost is a convenience method on ModelPrice that calculates total cost
func (p *ModelPrice) CalculateCost(usage *converter.TokenUsage) float64 {
	costs := CalculateTokenCosts(usage, p)
	if costs == nil {
		return 0.0
	}
	return costs.TotalCost
}

// CalculateCosts returns the full cost breakdown for all token types.
func (p *ModelPrice) CalculateCosts(usage *converter.TokenUsage) *converter.TokenCosts {
	return CalculateTokenCosts(usage, p)
}

// CalculateShadowCosts returns the isolated LiteLLM-parity cost breakdown.
func (p *ModelPrice) CalculateShadowCosts(usage *converter.TokenUsage) *converter.TokenCosts {
	return CalculateShadowTokenCosts(usage, p)
}
