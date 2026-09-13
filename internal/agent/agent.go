// Package agent encapsulates the complete request/response flow of the coding
// assistant independently from HTTP delivery and the OpenAI transport.
package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"codex-chat-cli/internal/models"
)

// LLM is the transport boundary used by Agent to call a language model.
type LLM interface {
	Complete(ctx context.Context, request CompletionRequest) (CompletionResponse, error)
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
	Model               string
	PreviousResponseID  string
	Format              string
	LengthLimit         string
	CompletionCondition string
	Temperature         *float64
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
	Text     string
	Model    string
	Usage    Usage
	Duration time.Duration
	CostUSD  *float64
}

// Agent owns one conversation and encapsulates its LLM request/response flow.
type Agent struct {
	llm          LLM
	defaultModel string

	mu                 sync.Mutex
	previousResponseID string
	activeModel        string
}

// New creates an independent agent conversation.
func New(llm LLM, defaultModel string) *Agent {
	return &Agent{llm: llm, defaultModel: strings.TrimSpace(defaultModel)}
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
	if a.activeModel != "" && request.Model != a.activeModel {
		previousResponseID = ""
	}
	completion, err := a.llm.Complete(ctx, CompletionRequest{
		Input:               request.Message,
		Model:               request.Model,
		PreviousResponseID:  previousResponseID,
		Format:              request.Format,
		LengthLimit:         request.LengthLimit,
		CompletionCondition: request.CompletionCondition,
		Temperature:         request.Temperature,
	})
	if err != nil {
		return Response{}, err
	}

	a.previousResponseID = completion.ResponseID
	a.activeModel = request.Model
	if completion.Model == "" {
		completion.Model = request.Model
	}
	if completion.Usage.TotalTokens == 0 {
		completion.Usage.TotalTokens = completion.Usage.InputTokens + completion.Usage.OutputTokens
	}

	response := Response{
		Text:     completion.Output,
		Model:    completion.Model,
		Usage:    completion.Usage,
		Duration: time.Since(started),
	}
	if cost, ok := models.EstimateCost(request.Model, completion.Usage.InputTokens, completion.Usage.CachedInputTokens, completion.Usage.CacheWriteTokens, completion.Usage.OutputTokens); ok {
		response.CostUSD = &cost
	}
	return response, nil
}

// Reset clears the conversation context without replacing the agent.
func (a *Agent) Reset() {
	a.mu.Lock()
	a.previousResponseID = ""
	a.activeModel = ""
	a.mu.Unlock()
}
