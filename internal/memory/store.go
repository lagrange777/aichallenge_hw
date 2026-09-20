// Package memory stores reviewed task and profile memories separately from chat history.
package memory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Layer string

const (
	Working  Layer = "working"
	LongTerm Layer = "long_term"
)

var ErrInvalid = errors.New("invalid memory operation")
var ErrConflict = errors.New("memory has changed; refresh and try again")

type Entry struct {
	MessageID string    `json:"messageId,omitempty"`
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Source    string    `json:"source"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SaveMessage records an explicit user action, without an extraction/model call.
// Existing keys are never silently overwritten by this shortcut.
func (s *Store) SaveMessage(owner, taskID, messageID string, layer Layer, key, value, source string) (State, error) {
	return s.update(owner, func(state *State) error {
		if taskID != state.Task.ID {
			return ErrConflict
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if messageID == "" || !validEntry(layer, key, value) {
			return ErrInvalid
		}
		entries := &state.Working
		if layer == LongTerm {
			entries = &state.LongTerm
		}
		for _, entry := range *entries {
			if entry.MessageID == messageID && entry.Key == key && entry.Value == value {
				return nil
			}
			if entry.Key == key || entry.MessageID == messageID {
				return ErrConflict
			}
		}
		if len(*entries) >= 100 {
			return ErrInvalid
		}
		id, err := newID()
		if err != nil {
			return err
		}
		*entries = append(*entries, Entry{ID: id, MessageID: messageID, Key: key, Value: value, Source: source, UpdatedAt: time.Now().UTC()})
		return nil
	})
}

type Proposal struct {
	ID         string     `json:"id"`
	TaskID     string     `json:"taskId"`
	Layer      Layer      `json:"layer"`
	Key        string     `json:"key"`
	Value      string     `json:"value"`
	Reason     string     `json:"reason"`
	Source     string     `json:"source"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"createdAt"`
	ReviewedAt *time.Time `json:"reviewedAt,omitempty"`
}
type Task struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// TaskMemory contains only task-scoped layers. Profile memory stays shared.
type TaskMemory struct {
	Task      Task       `json:"task"`
	Working   []Entry    `json:"working"`
	Proposals []Proposal `json:"proposals"`
}
type State struct {
	Task      Task         `json:"task"`
	Working   []Entry      `json:"working"`
	LongTerm  []Entry      `json:"longTerm"`
	Proposals []Proposal   `json:"proposals"`
	Archived  []TaskMemory `json:"-"`
}

// Store is single-process. Each revision contains independent layer files; an
// atomic CURRENT.json replacement commits all layers together, including reviews.
type Store struct {
	root string
	mu   sync.Mutex
}

func NewStore(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: empty memory directory", ErrInvalid)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func validID(id string) bool { _, err := hex.DecodeString(id); return len(id) == 32 && err == nil }
func (s *Store) Get(owner string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, revision, err := s.read(owner)
	if err != nil {
		return State{}, err
	}
	if revision == "" {
		if err = s.write(owner, state, revision); err != nil {
			return State{}, err
		}
	}
	return state, nil
}
func (s *Store) update(owner string, change func(*State) error) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, revision, err := s.read(owner)
	if err != nil {
		return State{}, err
	}
	if err = change(&state); err != nil {
		return State{}, err
	}
	if err = s.write(owner, state, revision); err != nil {
		return State{}, err
	}
	return state, nil
}
func (s *Store) read(owner string) (State, string, error) {
	if !validID(owner) {
		return State{}, "", fmt.Errorf("%w: profile ID", ErrInvalid)
	}
	dir := filepath.Join(s.root, owner)
	var revision string
	err := readJSON(filepath.Join(dir, "CURRENT.json"), &revision)
	if errors.Is(err, os.ErrNotExist) {
		return State{Task: Task{ID: owner, Name: "Текущая задача"}, Working: []Entry{}, LongTerm: []Entry{}, Proposals: []Proposal{}}, "", nil
	}
	if err != nil {
		return State{}, "", err
	}
	if !validID(revision) {
		return State{}, "", fmt.Errorf("invalid memory revision")
	}
	dir = filepath.Join(dir, revision)
	var state State
	for name, dst := range map[string]any{"task.json": &state.Task, "working.json": &state.Working, "long_term.json": &state.LongTerm, "proposals.json": &state.Proposals} {
		if err = readJSON(filepath.Join(dir, name), dst); err != nil {
			return State{}, "", err
		}
	}
	// Older revisions contain a single task and have no archive yet.
	if err = readJSON(filepath.Join(dir, "tasks.json"), &state.Archived); err != nil && !errors.Is(err, os.ErrNotExist) {
		return State{}, "", err
	}
	return state, revision, nil
}
func readJSON(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}
func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}
func (s *Store) write(owner string, state State, previous string) error {
	revision, err := newID()
	if err != nil {
		return err
	}
	dir := filepath.Join(s.root, owner)
	next := filepath.Join(dir, revision)
	if err = os.MkdirAll(next, 0700); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(next)
		}
	}()
	for name, value := range map[string]any{"task.json": state.Task, "working.json": state.Working, "long_term.json": state.LongTerm, "proposals.json": state.Proposals, "tasks.json": state.Archived} {
		if err = writeJSON(filepath.Join(next, name), value); err != nil {
			return err
		}
	}
	pointer := filepath.Join(dir, revision+".tmp")
	defer os.Remove(pointer)
	if err = writeJSON(pointer, revision); err != nil {
		return err
	}
	if err = os.Rename(pointer, filepath.Join(dir, "CURRENT.json")); err != nil {
		return err
	}
	committed = true
	if previous != "" {
		_ = os.RemoveAll(filepath.Join(dir, previous))
	}
	return nil
}
func validEntry(layer Layer, key, value string) bool {
	return (layer == Working || layer == LongTerm) && strings.TrimSpace(key) != "" && utf8.RuneCountInString(key) <= 100 && strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= 2000
}
func (s *Store) Propose(owner, taskID, source string, proposals []Proposal) (State, error) {
	return s.update(owner, func(state *State) error {
		if state.Task.ID != taskID {
			return ErrConflict
		}
		if len(proposals) > 5 {
			return fmt.Errorf("%w: at most 5 proposals per turn", ErrInvalid)
		}
		for _, p := range proposals {
			p.Key, p.Value, p.Reason = strings.TrimSpace(p.Key), strings.TrimSpace(p.Value), strings.TrimSpace(p.Reason)
			if !validEntry(p.Layer, p.Key, p.Value) || p.Reason == "" || utf8.RuneCountInString(p.Reason) > 1000 {
				return fmt.Errorf("%w: proposal", ErrInvalid)
			}
			duplicate := false
			entries := state.Working
			if p.Layer == LongTerm {
				entries = state.LongTerm
			}
			for _, e := range entries {
				if e.Key == p.Key && e.Value == p.Value {
					duplicate = true
				}
			}
			for _, old := range state.Proposals {
				if old.TaskID == taskID && old.Layer == p.Layer && old.Key == p.Key && old.Value == p.Value {
					duplicate = true
				}
			}
			if duplicate {
				continue
			}
			if len(state.Proposals) >= 500 {
				return fmt.Errorf("%w: proposal limit reached", ErrInvalid)
			}
			id, err := newID()
			if err != nil {
				return err
			}
			p.ID, p.TaskID, p.Source, p.Status, p.CreatedAt = id, taskID, source, "pending", time.Now().UTC()
			p.ReviewedAt = nil
			state.Proposals = append(state.Proposals, p)
		}
		return nil
	})
}

// Review is the only route from a model proposal into confirmed memory. The
// caller supplies the reviewed (possibly edited) destination, key and value.
func (s *Store) Review(owner, id, taskID, action string, layer Layer, key, value string) (State, error) {
	return s.update(owner, func(state *State) error {
		if taskID != state.Task.ID {
			return ErrConflict
		}
		for i := range state.Proposals {
			p := &state.Proposals[i]
			if p.ID != id {
				continue
			}
			if p.Status != "pending" || p.TaskID != taskID {
				return ErrConflict
			}
			now := time.Now().UTC()
			switch action {
			case "reject":
				p.Status = "rejected"
			case "accept":
				key, value = strings.TrimSpace(key), strings.TrimSpace(value)
				if !validEntry(layer, key, value) {
					return fmt.Errorf("%w: entry", ErrInvalid)
				}
				entries := &state.Working
				if layer == LongTerm {
					entries = &state.LongTerm
				}
				entry := Entry{ID: p.ID, Key: key, Value: value, Source: p.Source, UpdatedAt: now}
				found := false
				for j := range *entries {
					if (*entries)[j].Key == key {
						(*entries)[j] = entry
						found = true
						break
					}
				}
				if !found {
					if len(*entries) >= 100 {
						return fmt.Errorf("%w: at most 100 entries per layer", ErrInvalid)
					}
					*entries = append(*entries, entry)
				}
				p.Status, p.Layer, p.Key, p.Value = "accepted", layer, key, value
			default:
				return fmt.Errorf("%w: review action", ErrInvalid)
			}
			p.ReviewedAt = &now
			return nil
		}
		return ErrConflict
	})
}
func (s *Store) Edit(owner, taskID, id string, layer Layer, key, value string, remove bool) (State, error) {
	return s.update(owner, func(state *State) error {
		if taskID != state.Task.ID {
			return ErrConflict
		}
		if layer != Working && layer != LongTerm {
			return ErrInvalid
		}
		entries := &state.Working
		if layer == LongTerm {
			entries = &state.LongTerm
		}
		for i, e := range *entries {
			if e.ID == id {
				if remove {
					*entries = append((*entries)[:i], (*entries)[i+1:]...)
					return nil
				}
				key, value = strings.TrimSpace(key), strings.TrimSpace(value)
				if !validEntry(layer, key, value) {
					return ErrInvalid
				}
				for j, other := range *entries {
					if j != i && other.Key == key {
						return fmt.Errorf("%w: duplicate key", ErrInvalid)
					}
				}
				(*entries)[i].Key, (*entries)[i].Value, (*entries)[i].UpdatedAt = key, value, time.Now().UTC()
				return nil
			}
		}
		return ErrConflict
	})
}
func (s *Store) NewTask(owner, taskID, name string) (State, error) {
	return s.update(owner, func(state *State) error {
		if state.Task.ID != taskID {
			return ErrConflict
		}
		name = strings.TrimSpace(name)
		if name == "" || utf8.RuneCountInString(name) > 200 {
			return ErrInvalid
		}
		id, err := newID()
		if err != nil {
			return err
		}
		state.Archived = append(state.Archived, TaskMemory{state.Task, state.Working, state.Proposals})
		state.Task = Task{ID: id, Name: name}
		state.Working = []Entry{}
		// The previous task's suggestions remain in its archive.
		state.Proposals = []Proposal{}
		return nil
	})
}

func (s *Store) SwitchTask(owner, currentID, targetID string) (State, error) {
	return s.update(owner, func(state *State) error {
		if state.Task.ID != currentID {
			return ErrConflict
		}
		if currentID == targetID {
			return nil
		}
		for i, target := range state.Archived {
			if target.Task.ID != targetID {
				continue
			}
			state.Archived[i] = TaskMemory{state.Task, state.Working, state.Proposals}
			state.Task, state.Working, state.Proposals = target.Task, target.Working, target.Proposals
			return nil
		}
		return ErrConflict
	})
}
