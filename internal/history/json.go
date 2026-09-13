// Package history provides durable implementations of the agent history boundary.
package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"codex-chat-cli/internal/agent"
)

const (
	fileVersion    = 1
	maxHistorySize = 16 << 20
)

type fileData struct {
	Version       int                                `json:"version"`
	Conversations map[string]agent.ConversationState `json:"conversations"`
}

// JSONStore keeps all browser conversations in one atomically replaced JSON file.
type JSONStore struct {
	path string
	mu   sync.Mutex
}

var _ agent.History = (*JSONStore)(nil)

// NewJSONStore prepares and validates a JSON history file.
func NewJSONStore(path string) (*JSONStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("history path is required")
	}
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve history path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o700); err != nil {
		return nil, fmt.Errorf("create history directory: %w", err)
	}

	store := &JSONStore{path: cleanPath}
	if _, err := store.read(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("validate history file: %w", err)
	}
	return store, nil
}

// Load retrieves one conversation from disk.
func (s *JSONStore) Load(conversationID string) (agent.ConversationState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.read()
	if errors.Is(err, os.ErrNotExist) {
		return agent.ConversationState{}, agent.ErrHistoryNotFound
	}
	if err != nil {
		return agent.ConversationState{}, err
	}
	state, ok := data.Conversations[conversationID]
	if !ok {
		return agent.ConversationState{}, agent.ErrHistoryNotFound
	}
	return state, nil
}

// Save inserts or replaces one conversation and atomically writes the file.
func (s *JSONStore) Save(conversationID string, state agent.ConversationState) error {
	if strings.TrimSpace(conversationID) == "" {
		return errors.New("conversation ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.read()
	if errors.Is(err, os.ErrNotExist) {
		data = fileData{Version: fileVersion, Conversations: make(map[string]agent.ConversationState)}
	} else if err != nil {
		return err
	}
	if data.Conversations == nil {
		data.Conversations = make(map[string]agent.ConversationState)
	}
	data.Conversations[conversationID] = state
	return s.write(data)
}

// Delete removes one conversation while leaving all other histories intact.
func (s *JSONStore) Delete(conversationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.read()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, ok := data.Conversations[conversationID]; !ok {
		return nil
	}
	delete(data.Conversations, conversationID)
	return s.write(data)
}

func (s *JSONStore) read() (fileData, error) {
	file, err := os.Open(s.path)
	if err != nil {
		return fileData{}, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fileData{}, fmt.Errorf("inspect history file: %w", err)
	}
	if info.Size() > maxHistorySize {
		return fileData{}, fmt.Errorf("history file exceeds %d bytes", maxHistorySize)
	}

	var data fileData
	decoder := json.NewDecoder(io.LimitReader(file, maxHistorySize+1))
	if err := decoder.Decode(&data); err != nil {
		return fileData{}, fmt.Errorf("decode history file: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fileData{}, errors.New("history file must contain one JSON object")
	}
	if data.Version != fileVersion {
		return fileData{}, fmt.Errorf("unsupported history version %d", data.Version)
	}
	if data.Conversations == nil {
		data.Conversations = make(map[string]agent.ConversationState)
	}
	return data, nil
}

func (s *JSONStore) write(data fileData) error {
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".history-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary history file: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("set history permissions: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		return fmt.Errorf("encode history file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync history file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close history file: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace history file: %w", err)
	}
	committed = true
	return nil
}
