// Package agent encapsulates the complete request/response flow of the coding
// assistant independently from HTTP delivery and the OpenAI transport.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"codex-chat-cli/internal/models"
)

// LLM is the transport boundary used by Agent to call a language model.
type LLM interface {
	Complete(ctx context.Context, request CompletionRequest) (CompletionResponse, error)
}

// TokenCounter returns exact input-token counts before an LLM request.
type TokenCounter interface {
	CountTokens(ctx context.Context, request CompletionRequest) (TokenCounts, error)
}

// Request is a user request accepted by the agent.
type Request struct {
	Message             string
	Model               string
	Format              string
	LengthLimit         string
	CompletionCondition string
	Temperature         *float64
}

// CompletionRequest is a normalized request sent from the agent to an LLM.
type CompletionRequest struct {
	Input               string
	History             []ContextMessage
	Model               string
	PreviousResponseID  string
	Format              string
	LengthLimit         string
	CompletionCondition string
	Temperature         *float64
}

// ContextMessage is one prior conversational message replayed when an API
// response chain cannot be continued, for example after switching models.
type ContextMessage struct {
	Role    string
	Content string
}

// CompletionResponse is the transport-neutral result returned by an LLM.
type CompletionResponse struct {
	ResponseID string
	Output     string
	Model      string
	Usage      Usage
}

// Usage contains token counts reported by the LLM API.
type Usage struct {
	InputTokens       int
	CachedInputTokens int
	CacheWriteTokens  int
	OutputTokens      int
	ReasoningTokens   int
	TotalTokens       int
}

// Response is the final result prepared by the agent for the interface layer.
type Response struct {
	Text         string
	Model        string
	Usage        Usage
	Duration     time.Duration
	CostUSD      *float64
	TokenMetrics TokenMetrics
}

// MessageMetrics contains metadata saved together with an assistant message.
type MessageMetrics struct {
	DurationMS        int64    `json:"durationMs"`
	InputTokens       int      `json:"inputTokens"`
	CachedInputTokens int      `json:"cachedInputTokens"`
	CacheWriteTokens  int      `json:"cacheWriteTokens"`
	OutputTokens      int      `json:"outputTokens"`
	ReasoningTokens   int      `json:"reasoningTokens"`
	TotalTokens       int      `json:"totalTokens"`
	CostUSD           *float64 `json:"costUsd"`
	TokenMetrics
}

// Message is one durable item in a conversation transcript.
type Message struct {
	Role    string          `json:"role"`
	Text    string          `json:"text"`
	Time    int64           `json:"time"`
	Model   string          `json:"model,omitempty"`
	Metrics *MessageMetrics `json:"metrics,omitempty"`
}

// ConversationState is the complete state needed to restore an Agent.
type ConversationState struct {
	PreviousResponseID string    `json:"previousResponseId,omitempty"`
	ActiveModel        string    `json:"activeModel,omitempty"`
	Messages           []Message `json:"messages"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

// ErrHistoryNotFound indicates that a conversation has not been saved yet.
var ErrHistoryNotFound = errors.New("conversation history not found")

// History is the persistence boundary owned by Agent.
type History interface {
	Load(conversationID string) (ConversationState, error)
	Save(conversationID string, state ConversationState) error
	Delete(conversationID string) error
}

// Agent owns one conversation and encapsulates its LLM request/response flow.
type Agent struct {
	llm          LLM
	tokenCounter TokenCounter
	defaultModel string

	mu                 sync.Mutex
	previousResponseID string
	activeModel        string
	messages           []Message
	history            History
	conversationID     string
}

// New creates an independent agent conversation.
func New(llm LLM, defaultModel string) *Agent {
	a := &Agent{llm: llm, defaultModel: strings.TrimSpace(defaultModel)}
	if counter, ok := llm.(TokenCounter); ok {
		a.tokenCounter = counter
	}
	return a
}

// NewPersistent creates an agent and restores its conversation when it exists.
func NewPersistent(llm LLM, defaultModel, conversationID string, history History) (*Agent, error) {
	conversationID = strings.TrimSpace(conversationID)
	if history == nil {
		return New(llm, defaultModel), nil
	}
	if conversationID == "" {
		return nil, errors.New("conversation ID is required for persistent history")
	}

	a := New(llm, defaultModel)
	a.history = history
	a.conversationID = conversationID
	state, err := history.Load(conversationID)
	if errors.Is(err, ErrHistoryNotFound) {
		return a, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load conversation history: %w", err)
	}
	a.previousResponseID = strings.TrimSpace(state.PreviousResponseID)
	a.activeModel = strings.TrimSpace(state.ActiveModel)
	a.messages = cloneMessages(state.Messages)
	return a, nil
}

// Ask normalizes a user request, calls the LLM and prepares the final response.
// Conversation state advances only after a successful completion.
func (a *Agent) Ask(ctx context.Context, request Request) (Response, error) {
	started := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()

	request.Message = strings.TrimSpace(request.Message)
	if request.Message == "" {
		return Response{}, errors.New("message is required")
	}
	request.Model = strings.TrimSpace(request.Model)
	if request.Model == "" {
		request.Model = a.defaultModel
	}
	if request.Model == "" {
		return Response{}, errors.New("model is required")
	}
	request.Format = strings.TrimSpace(request.Format)
	request.LengthLimit = strings.TrimSpace(request.LengthLimit)
	request.CompletionCondition = strings.TrimSpace(request.CompletionCondition)

	previousResponseID := a.previousResponseID
	var replayHistory []ContextMessage
	if len(a.messages) > 0 && (previousResponseID == "" || request.Model != a.activeModel) {
		previousResponseID = ""
		replayHistory = contextMessages(a.messages)
	}
	completionRequest := CompletionRequest{
		Input:               request.Message,
		History:             replayHistory,
		Model:               request.Model,
		PreviousResponseID:  previousResponseID,
		Format:              request.Format,
		LengthLimit:         request.LengthLimit,
		CompletionCondition: request.CompletionCondition,
		Temperature:         request.Temperature,
	}
	response := Response{
		Model:        request.Model,
		TokenMetrics: a.measureTokens(ctx, completionRequest),
	}
	completion, err := a.llm.Complete(ctx, completionRequest)
	if err != nil {
		response.Duration = time.Since(started)
		return response, err
	}

	if completion.Model == "" {
		completion.Model = request.Model
	}
	if completion.Usage.TotalTokens == 0 {
		completion.Usage.TotalTokens = completion.Usage.InputTokens + completion.Usage.OutputTokens
	}

	response.Text = completion.Output
	response.Model = completion.Model
	response.Usage = completion.Usage
	response.Duration = time.Since(started)
	if cost, ok := models.EstimateCost(request.Model, completion.Usage.InputTokens, completion.Usage.CachedInputTokens, completion.Usage.CacheWriteTokens, completion.Usage.OutputTokens); ok {
		response.CostUSD = &cost
	}
	response.TokenMetrics = a.completeTokenMetrics(response.TokenMetrics, response.Usage, response.CostUSD)

	durationMS := response.Duration.Milliseconds()
	if durationMS < 1 {
		durationMS = 1
	}
	nextMessages := append(cloneMessages(a.messages),
		Message{Role: "user", Text: request.Message, Time: started.UnixMilli()},
		Message{
			Role:  "assistant",
			Text:  response.Text,
			Time:  time.Now().UnixMilli(),
			Model: response.Model,
			Metrics: &MessageMetrics{
				DurationMS:        durationMS,
				InputTokens:       response.Usage.InputTokens,
				CachedInputTokens: response.Usage.CachedInputTokens,
				CacheWriteTokens:  response.Usage.CacheWriteTokens,
				OutputTokens:      response.Usage.OutputTokens,
				ReasoningTokens:   response.Usage.ReasoningTokens,
				TotalTokens:       response.Usage.TotalTokens,
				CostUSD:           response.CostUSD,
				TokenMetrics:      response.TokenMetrics,
			},
		},
	)
	if a.history != nil {
		state := ConversationState{
			PreviousResponseID: completion.ResponseID,
			ActiveModel:        request.Model,
			Messages:           nextMessages,
			UpdatedAt:          time.Now().UTC(),
		}
		if err := a.history.Save(a.conversationID, state); err != nil {
			return Response{}, fmt.Errorf("save conversation history: %w", err)
		}
	}
	a.previousResponseID = completion.ResponseID
	a.activeModel = request.Model
	a.messages = nextMessages
	return response, nil
}

func contextMessages(messages []Message) []ContextMessage {
	context := make([]ContextMessage, 0, len(messages))
	for _, message := range messages {
		role := strings.TrimSpace(message.Role)
		content := message.Text
		if strings.TrimSpace(content) == "" || (role != "user" && role != "assistant") {
			continue
		}
		context = append(context, ContextMessage{Role: role, Content: content})
	}
	return context
}

// Reset clears the conversation context without replacing the agent.
func (a *Agent) Reset() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.history != nil {
		if err := a.history.Delete(a.conversationID); err != nil {
			return fmt.Errorf("delete conversation history: %w", err)
		}
	}
	a.previousResponseID = ""
	a.activeModel = ""
	a.messages = nil
	return nil
}

// Messages returns a snapshot of the conversation transcript.
func (a *Agent) Messages() []Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneMessages(a.messages)
}

func cloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]Message, len(messages))
	for index, message := range messages {
		cloned[index] = message
		if message.Metrics != nil {
			metrics := *message.Metrics
			if message.Metrics.CostUSD != nil {
				cost := *message.Metrics.CostUSD
				metrics.CostUSD = &cost
			}
			if message.Metrics.CumulativeCostUSD != nil {
				cost := *message.Metrics.CumulativeCostUSD
				metrics.CumulativeCostUSD = &cost
			}
			cloned[index].Metrics = &metrics
		}
	}
	return cloned
}
