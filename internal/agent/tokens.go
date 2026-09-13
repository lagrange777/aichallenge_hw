package agent

import (
	"context"
	"fmt"

	"codex-chat-cli/internal/models"
)

// TokenCounts contains exact counts returned before generating a response.
type TokenCounts struct {
	CurrentRequestTokens int
	ProjectedInputTokens int
}

// TokenMetrics explains token usage for one turn and the whole conversation.
type TokenMetrics struct {
	TokenCountAvailable     bool     `json:"tokenCountAvailable"`
	CurrentRequestTokens    int      `json:"currentRequestTokens"`
	HistoryTokens           int      `json:"historyTokens"`
	ProjectedInputTokens    int      `json:"projectedInputTokens"`
	ContextWindow           int      `json:"contextWindow"`
	MaxInputTokens          int      `json:"maxInputTokens"`
	MaxOutputTokens         int      `json:"maxOutputTokens"`
	ContextUsagePercent     float64  `json:"contextUsagePercent"`
	ContextWarning          string   `json:"contextWarning,omitempty"`
	CumulativeInputTokens   int      `json:"cumulativeInputTokens"`
	CumulativeOutputTokens  int      `json:"cumulativeOutputTokens"`
	CumulativeTotalTokens   int      `json:"cumulativeTotalTokens"`
	CumulativeCostUSD       *float64 `json:"cumulativeCostUsd"`
	CompressionEnabled      bool     `json:"compressionEnabled"`
	CompressionApplied      bool     `json:"compressionApplied"`
	CompressionWarning      string   `json:"compressionWarning,omitempty"`
	SummaryMessages         int      `json:"summaryMessages"`
	RetainedMessages        int      `json:"retainedMessages"`
	CompressionRuns         int      `json:"compressionRuns"`
	SummaryInputTokens      int      `json:"summaryInputTokens"`
	SummaryOutputTokens     int      `json:"summaryOutputTokens"`
	SummaryTotalTokens      int      `json:"summaryTotalTokens"`
	UncompressedInputTokens int      `json:"uncompressedInputTokens"`
	CompressedInputTokens   int      `json:"compressedInputTokens"`
	SavedInputTokens        int      `json:"savedInputTokens"`
	TokenSavingsPercent     float64  `json:"tokenSavingsPercent"`
}

func (a *Agent) measureTokens(ctx context.Context, request CompletionRequest, summary *ConversationSummary) TokenMetrics {
	metrics := TokenMetrics{CompressionEnabled: a.compression.Enabled}
	if definition, ok := models.Find(request.Model); ok {
		metrics.ContextWindow = definition.ContextWindow
		metrics.MaxInputTokens = definition.MaxInputTokens
		metrics.MaxOutputTokens = definition.MaxOutputTokens
	}
	metrics.CumulativeInputTokens, metrics.CumulativeOutputTokens, metrics.CumulativeTotalTokens, metrics.CumulativeCostUSD = cumulativeMetrics(a.messages)
	if summary != nil && summary.MessageCount > 0 {
		metrics.SummaryMessages = summary.MessageCount
		metrics.CompressionRuns = summary.Runs
		metrics.SummaryInputTokens = summary.InputTokens
		metrics.SummaryOutputTokens = summary.OutputTokens
		metrics.SummaryTotalTokens = summary.TotalTokens
		metrics.CumulativeInputTokens += max(0, summary.InputTokens)
		metrics.CumulativeOutputTokens += max(0, summary.OutputTokens)
		metrics.CumulativeTotalTokens += max(0, summary.TotalTokens)
		metrics.CumulativeCostUSD = addKnownCosts(metrics.CumulativeCostUSD, summary.CostUSD)
	}
	metrics.RetainedMessages = max(0, len(a.messages)-metrics.SummaryMessages)
	if a.tokenCounter == nil {
		return metrics
	}

	counts, err := a.tokenCounter.CountTokens(ctx, request)
	if err != nil {
		return metrics
	}
	metrics.TokenCountAvailable = true
	metrics.CurrentRequestTokens = max(0, counts.CurrentRequestTokens)
	metrics.ProjectedInputTokens = max(metrics.CurrentRequestTokens, counts.ProjectedInputTokens)
	metrics.HistoryTokens = max(0, metrics.ProjectedInputTokens-metrics.CurrentRequestTokens)
	if summary != nil && summary.MessageCount > 0 {
		metrics.CompressedInputTokens = metrics.ProjectedInputTokens
		fullRequest := request
		fullRequest.PreviousResponseID = ""
		fullRequest.History = contextMessages(a.messages)
		if fullCounts, err := a.tokenCounter.CountTokens(ctx, fullRequest); err == nil {
			metrics.UncompressedInputTokens = max(0, fullCounts.ProjectedInputTokens)
			metrics.SavedInputTokens = max(0, metrics.UncompressedInputTokens-metrics.CompressedInputTokens)
			if metrics.UncompressedInputTokens > 0 {
				metrics.TokenSavingsPercent = float64(metrics.SavedInputTokens) / float64(metrics.UncompressedInputTokens) * 100
			}
			summary.LastUncompressedTokens = metrics.UncompressedInputTokens
			summary.LastCompressedTokens = metrics.CompressedInputTokens
			summary.LastSavedTokens = metrics.SavedInputTokens
			summary.LastTokenSavingsPercent = metrics.TokenSavingsPercent
		} else {
			metrics.UncompressedInputTokens = summary.LastUncompressedTokens
			metrics.CompressedInputTokens = summary.LastCompressedTokens
			metrics.SavedInputTokens = summary.LastSavedTokens
			metrics.TokenSavingsPercent = summary.LastTokenSavingsPercent
		}
	}
	if metrics.MaxInputTokens > 0 {
		metrics.ContextUsagePercent = float64(metrics.ProjectedInputTokens) / float64(metrics.MaxInputTokens) * 100
		if metrics.ProjectedInputTokens > metrics.MaxInputTokens {
			metrics.ContextWarning = fmt.Sprintf(
				"Контекст превышает входной лимит модели: %d из %d токенов. Запрос всё равно отправлен; API может отклонить его или вернуть неполный ответ.",
				metrics.ProjectedInputTokens,
				metrics.MaxInputTokens,
			)
		}
	}
	return metrics
}

func addKnownCosts(first, second *float64) *float64 {
	if first == nil || second == nil {
		return nil
	}
	total := *first + *second
	return &total
}

func (a *Agent) completeTokenMetrics(metrics TokenMetrics, usage Usage, cost *float64) TokenMetrics {
	metrics.CumulativeInputTokens += max(0, usage.InputTokens)
	metrics.CumulativeOutputTokens += max(0, usage.OutputTokens)
	metrics.CumulativeTotalTokens += max(0, usage.TotalTokens)
	if metrics.CumulativeCostUSD != nil && cost != nil {
		total := *metrics.CumulativeCostUSD + *cost
		metrics.CumulativeCostUSD = &total
	} else if len(a.messages) == 0 && cost != nil {
		total := *cost
		metrics.CumulativeCostUSD = &total
	} else {
		metrics.CumulativeCostUSD = nil
	}
	return metrics
}

func cumulativeMetrics(messages []Message) (input, output, total int, cost *float64) {
	costValue := 0.0
	costKnown := true
	hasAssistantMessage := false
	for _, message := range messages {
		if message.Role != "assistant" || message.Metrics == nil {
			continue
		}
		hasAssistantMessage = true
		input += max(0, message.Metrics.InputTokens)
		output += max(0, message.Metrics.OutputTokens)
		turnTotal := message.Metrics.TotalTokens
		if turnTotal <= 0 {
			turnTotal = message.Metrics.InputTokens + message.Metrics.OutputTokens
		}
		total += max(0, turnTotal)
		if message.Metrics.CostUSD == nil {
			costKnown = false
		} else {
			costValue += *message.Metrics.CostUSD
		}
	}
	if costKnown && hasAssistantMessage {
		cost = &costValue
	}
	return input, output, total, cost
}
