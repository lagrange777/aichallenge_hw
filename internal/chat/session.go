package chat

import (
	"context"
	"strings"
	"sync"
)

// Responder is the API boundary used by a chat session.
type Responder interface {
	Respond(ctx context.Context, input, previousResponseID string) (responseID, output string, err error)
}

// Session holds conversation state for the lifetime of the current process.
type Session struct {
	responder Responder

	mu                 sync.Mutex
	previousResponseID string
}

func NewSession(responder Responder) *Session {
	return &Session{responder: responder}
}

// Ask sends a turn and advances session state only after a successful response.
func (s *Session) Ask(ctx context.Context, input string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	responseID, output, err := s.responder.Respond(ctx, strings.TrimSpace(input), s.previousResponseID)
	if err != nil {
		return "", err
	}
	s.previousResponseID = responseID
	return output, nil
}

// Reset starts a new conversation within the same CLI process.
func (s *Session) Reset() {
	s.mu.Lock()
	s.previousResponseID = ""
	s.mu.Unlock()
}
