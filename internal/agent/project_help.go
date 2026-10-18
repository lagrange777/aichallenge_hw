package agent

import (
	"codex-chat-cli/internal/models"
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
)

type ProjectContext struct {
	Documents int       `json:"documents"`
	Chunks    int       `json:"chunks"`
	IndexHash string    `json:"indexHash"`
	Runs      []ToolRun `json:"runs"`
}

func WithProjectRetriever(r Retriever) Option { return func(a *Agent) { a.projectRetriever = r } }
func ProjectHelpQuestion(message string) (string, bool) {
	message = strings.TrimSpace(message)
	if message == "/help" {
		return "", true
	}
	if strings.HasPrefix(message, "/help") {
		tail := []rune(strings.TrimPrefix(message, "/help"))
		if len(tail) > 0 && unicode.IsSpace(tail[0]) {
			return strings.TrimSpace(string(tail)), true
		}
	}
	return "", false
}

const ProjectHelpMenu = `Ассистент проекта codex_chat_cli. Задайте вопрос после команды /help:

- /help Как устроен проект и его каталоги?
- /help Как запустить приложение в Docker?
- /help Как вызвать API чата?
- /help Где реализованы RAG и память?
- /help На какой ветке мы сейчас?

Ответы опираются на README, docs и актуальные результаты MCP. Источники и точные цитаты доступны под ответом. Старый учебный корпус и память других задач для /help не используются.`

// askProjectHelpLocked uses an isolated evidence-only request. It cannot invoke
// trading/scheduler tools, update task memory, or reuse stale hidden API chains.
// The visible turn is persisted through the same history/branch mechanism.
func (a *Agent) askProjectHelpLocked(ctx context.Context, request Request, question string, started time.Time) (Response, error) {
	if a.projectRetriever == nil {
		return Response{}, fmt.Errorf("Ассистент проекта не подключён")
	}
	if len([]rune(question)) > 4000 {
		return Response{}, fmt.Errorf("Вопрос /help ограничен 4000 символами")
	}
	zeroCost := 0.0
	response := Response{Text: ProjectHelpMenu, Model: request.Model, CostUSD: &zeroCost}
	var runs []ToolRun
	if question != "" {
		r, err := a.projectRetriever.Retrieve(ctx, question)
		if err != nil {
			return Response{}, fmt.Errorf("Документация проекта: %w", err)
		}
		if r == nil {
			return Response{}, fmt.Errorf("Пустой результат поиска по проекту")
		}
		r = cloneRetrieval(r)
		r.Mode = "rag"
		r.Empty = len(r.Sources) == 0
		response.Retrieval = r
		if r.Project != nil {
			runs = r.Project.Runs
		}
		if r.Empty {
			r.Grounding = unknownGrounding("project_evidence_unavailable", 0)
			response.Text = UnknownRAGAnswer
		} else {
			completion, _, err := a.completeGrounded(ctx, CompletionRequest{Model: request.Model, Input: question}, r, invariantPlan{})
			if err != nil {
				return Response{}, err
			}
			response.Text = completion.Output
			response.Usage = sumUsage(completion.Usage, r.AuxiliaryUsage())
		}
		r.RecordCitations(response.Text)
		if r.Warning != "" {
			response.Text += "\n\n" + r.Warning
		}
		if cost, ok := models.EstimateCost(request.Model, response.Usage.InputTokens, response.Usage.CachedInputTokens, response.Usage.CacheWriteTokens, response.Usage.OutputTokens); ok {
			response.CostUSD = &cost
		}
	}
	response.Duration = time.Since(started)
	metrics := TokenMetrics{ContextStrategy: string(a.strategy.Type)}
	metrics.CumulativeInputTokens, metrics.CumulativeOutputTokens, metrics.CumulativeTotalTokens, metrics.CumulativeCostUSD = cumulativeMetrics(a.messages)
	if definition, ok := models.Find(request.Model); ok {
		metrics.ContextWindow, metrics.MaxInputTokens, metrics.MaxOutputTokens = definition.ContextWindow, definition.MaxInputTokens, definition.MaxOutputTokens
	}
	response.TokenMetrics = a.completeTokenMetrics(metrics, response.Usage, response.CostUSD)
	response.TokenMetrics.RetainedMessages = len(a.messages) + 2
	messages := append(cloneMessages(a.messages), Message{Role: "user", Text: request.Message, Time: started.UnixMilli()}, Message{Role: "assistant", Text: response.Text, Time: time.Now().UnixMilli(), Model: request.Model, Retrieval: cloneRetrieval(response.Retrieval), ToolRuns: append([]ToolRun(nil), runs...), Metrics: &MessageMetrics{DurationMS: max(1, response.Duration.Milliseconds()), InputTokens: response.Usage.InputTokens, CachedInputTokens: response.Usage.CachedInputTokens, OutputTokens: response.Usage.OutputTokens, ReasoningTokens: response.Usage.ReasoningTokens, TotalTokens: response.Usage.TotalTokens, CostUSD: response.CostUSD, TokenMetrics: response.TokenMetrics}})
	branches := cloneBranches(a.branches)
	if a.strategy.Type == StrategyBranching && a.activeBranchID != "" {
		b := branches[a.activeBranchID]
		b.PreviousResponseID = ""
		b.ActiveModel = request.Model
		b.Messages = cloneMessages(messages)
		b.UpdatedAt = time.Now().UTC()
		branches[a.activeBranchID] = b
	}
	if err := a.saveStateLocked("", request.Model, messages, a.summary, a.facts, a.memory, branches, a.activeBranchID, a.checkpoint); err != nil {
		return Response{}, err
	}
	a.messages = messages
	a.branches = branches
	a.previousResponseID = ""
	a.activeModel = request.Model
	return response, nil
}

func cloneProject(p *ProjectContext) *ProjectContext {
	if p == nil {
		return nil
	}
	copy := *p
	copy.Runs = append([]ToolRun(nil), p.Runs...)
	return &copy
}
