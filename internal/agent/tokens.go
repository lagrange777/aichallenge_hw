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
	TokenCountAvailable    bool     `json:"tokenCountAvailable"`
	CurrentRequestTokens   int      `json:"currentRequestTokens"`
	HistoryTokens          int      `json:"historyTokens"`
	ProjectedInputTokens   int      `json:"projectedInputTokens"`
	ContextWindow          int      `json:"contextWindow"`
	MaxInputTokens         int      `json:"maxInputTokens"`
	MaxOutputTokens        int      `json:"maxOutputTokens"`
	ContextUsagePercent    float64  `json:"contextUsagePercent"`
	ContextWarning         string   `json:"contextWarning,omitempty"`
	CumulativeInputTokens  int      `json:"cumulativeInputTokens"`
	CumulativeOutputTokens int      `json:"cumulativeOutputTokens"`
	CumulativeTotalTokens  int      `json:"cumulativeTotalTokens"`
	CumulativeCostUSD      *float64 `json:"cumulativeCostUsd"`
}

func (a *Agent) measureTokens(ctx context.Context, request CompletionRequest) TokenMetrics {
	metrics := TokenMetrics{}
	if definition, ok := models.Find(request.Model); ok {
		metrics.ContextWindow = definition.ContextWindow
		metrics.MaxInputTokens = definition.MaxInputTokens
		metrics.MaxOutputTokens = definition.MaxOutputTokens
	}
	metrics.CumulativeInputTokens, metrics.CumulativeOutputTokens, metrics.CumulativeTotalTokens, metrics.CumulativeCostUSD = cumulativeMetrics(a.messages)
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
