// Package models contains the models exposed by the web interface and their
// public token prices. Prices are snapshots and are used only for estimates.
package models

import "strings"

const longContextThreshold = 272000

// Definition describes a selectable OpenAI model and its token prices in USD.
type Definition struct {
	ID                    string
	Label                 string
	InputPerMillion       float64
	CachedInputPerMillion float64
	OutputPerMillion      float64
	CacheWriteMultiplier  float64
	LongContextPricing    bool
}

var catalog = []Definition{
	{ID: "gpt-5.6-sol", Label: "GPT-5.6 Sol", InputPerMillion: 4, CachedInputPerMillion: 0.4, OutputPerMillion: 20, CacheWriteMultiplier: 1.25, LongContextPricing: true},
	{ID: "gpt-5.6-terra", Label: "GPT-5.6 Terra", InputPerMillion: 2, CachedInputPerMillion: 0.2, OutputPerMillion: 12, CacheWriteMultiplier: 1.25, LongContextPricing: true},
	{ID: "gpt-5.3-codex", Label: "GPT-5.3 Codex", InputPerMillion: 1.75, CachedInputPerMillion: 0.175, OutputPerMillion: 14},
	{ID: "gpt-5.4-mini", Label: "GPT-5.4 mini", InputPerMillion: 0.75, CachedInputPerMillion: 0.075, OutputPerMillion: 4.5},
	{ID: "gpt-5.6-luna", Label: "GPT-5.6 Luna", InputPerMillion: 0.2, CachedInputPerMillion: 0.02, OutputPerMillion: 1.2, CacheWriteMultiplier: 1.25, LongContextPricing: true},
}

// Available returns known models from strongest to lightest. A custom
// configured model remains usable at the end, but has no known power rank or
// price estimate.
func Available(defaultID string) []Definition {
	defaultID = strings.TrimSpace(defaultID)
	result := make([]Definition, 0, len(catalog)+1)
	for _, definition := range catalog {
		result = append(result, definition)
	}
	if _, known := Find(defaultID); defaultID != "" && !known {
		result = append(result, Definition{ID: defaultID, Label: defaultID})
	}
	return result
}

// Find looks up a model with known pricing.
func Find(id string) (Definition, bool) {
	for _, definition := range catalog {
		if definition.ID == id {
			return definition, true
		}
	}
	return Definition{}, false
}

// EstimateCost returns the approximate token cost in USD. Tool-specific fees
// and future pricing changes are outside this estimate.
func EstimateCost(id string, inputTokens, cachedInputTokens, cacheWriteTokens, outputTokens int) (float64, bool) {
	definition, ok := Find(id)
	if !ok {
		return 0, false
	}

	inputTokens = nonNegative(inputTokens)
	cachedInputTokens = min(nonNegative(cachedInputTokens), inputTokens)
	cacheWriteTokens = min(nonNegative(cacheWriteTokens), inputTokens-cachedInputTokens)
	uncachedInputTokens := inputTokens - cachedInputTokens - cacheWriteTokens
	outputTokens = nonNegative(outputTokens)

	inputMultiplier := 1.0
	outputMultiplier := 1.0
	if definition.LongContextPricing && inputTokens > longContextThreshold {
		inputMultiplier = 2
		outputMultiplier = 1.5
	}

	cost := (float64(uncachedInputTokens)*definition.InputPerMillion +
		float64(cachedInputTokens)*definition.CachedInputPerMillion +
		float64(cacheWriteTokens)*definition.InputPerMillion*definition.CacheWriteMultiplier) * inputMultiplier
	cost += float64(outputTokens) * definition.OutputPerMillion * outputMultiplier
	return cost / 1_000_000, true
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
