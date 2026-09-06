package chat

import (
	"context"
	"strings"
	"sync"
)

// Responder is the API boundary used by a chat session.
type Responder interface {
	Respond(ctx context.Context, input, previousResponseID string, options ResponseOptions) (Result, error)
}

// Usage contains token counts reported by the Responses API.
type Usage struct {
	InputTokens       int
	CachedInputTokens int
	CacheWriteTokens  int
	OutputTokens      int
	ReasoningTokens   int
	TotalTokens       int
}

// Result is a completed model response with its billing metadata.
type Result struct {
	ResponseID string
	Output     string
	Model      string
	Usage      Usage
	CostUSD    *float64
}

// ResponseOptions contains optional requirements for a single model response.
type ResponseOptions struct {
	Model               string
	Format              string
	LengthLimit         string
	CompletionCondition string
	Temperature         *float64
}

// Session holds conversation state for the lifetime of the current process.
type Session struct {
	responder Responder

	mu                 sync.Mutex
	previousResponseID string
	activeModel        string
}

func NewSession(responder Responder) *Session {
	return &Session{responder: responder}
}

// Ask sends a turn and advances session state only after a successful response.
func (s *Session) Ask(ctx context.Context, input string, options ResponseOptions) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	options.Model = strings.TrimSpace(options.Model)
	options.Format = strings.TrimSpace(options.Format)
	options.LengthLimit = strings.TrimSpace(options.LengthLimit)
	options.CompletionCondition = strings.TrimSpace(options.CompletionCondition)
	previousResponseID := s.previousResponseID
	if s.activeModel != "" && options.Model != s.activeModel {
		previousResponseID = ""
	}
	result, err := s.responder.Respond(ctx, strings.TrimSpace(input), previousResponseID, options)
	if err != nil {
		return Result{}, err
	}
	s.previousResponseID = result.ResponseID
	s.activeModel = options.Model
	return result, nil
}

// Reset starts a new conversation within the same browser session.
func (s *Session) Reset() {
	s.mu.Lock()
	s.previousResponseID = ""
	s.activeModel = ""
	s.mu.Unlock()
}
