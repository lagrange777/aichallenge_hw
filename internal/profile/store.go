// Package profile persists user-selected personalization separately from memory.
package profile

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid profile")
var ErrConflict = errors.New("profile has changed or is unavailable")

type Profile struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Language    string    `json:"language"`
	Level       string    `json:"level"`
	Style       string    `json:"style"`
	Detail      string    `json:"detail"`
	Format      string    `json:"format"`
	Constraints string    `json:"constraints"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type State struct {
	ActiveID string    `json:"activeId"`
	Profiles []Profile `json:"profiles"`
}

func (s State) Active() Profile {
	for _, p := range s.Profiles {
		if p.ID == s.ActiveID {
			return p
		}
	}
	return Profile{}
}

func Default() Profile {
	return Profile{Name: "Основной", Language: "auto", Level: "auto", Style: "neutral", Detail: "normal", Format: "auto"}
}

func Presets() []Profile {
	return []Profile{
		{Name: "Новичок", Description: "Изучаю разработку", Language: "ru", Level: "beginner", Style: "friendly", Detail: "detailed", Format: "steps", Constraints: "Объясняй термины простыми словами. Приводи понятные примеры. Без эмодзи."},
		{Name: "Разработчик", Description: "Разрабатываю серверные приложения на Go", Language: "en", Level: "expert", Style: "neutral", Detail: "brief", Format: "auto", Constraints: "Примеры кода на Go. Минимум вводных пояснений. Без эмодзи."},
		{Name: "Руководитель", Description: "Принимаю решения по разработке продукта", Language: "ru", Level: "intermediate", Style: "business", Detail: "brief", Format: "list", Constraints: "Без кода. Описывай результат, риски и варианты решения. Без эмодзи."},
	}
}

func validID(id string) bool { _, err := hex.DecodeString(id); return len(id) == 32 && err == nil }

func Validate(p Profile) error {
	if strings.TrimSpace(p.Name) == "" || utf8.RuneCountInString(p.Name) > 100 || utf8.RuneCountInString(p.Description) > 1000 || utf8.RuneCountInString(p.Constraints) > 2000 {
		return ErrInvalid
	}
	valid := func(value string, choices ...string) bool {
		for _, c := range choices {
			if c == value {
				return true
			}
		}
		return false
	}
	if !valid(p.Language, "auto", "ru", "en") || !valid(p.Level, "auto", "beginner", "intermediate", "expert") || !valid(p.Style, "neutral", "friendly", "business") || !valid(p.Detail, "brief", "normal", "detailed") || !valid(p.Format, "auto", "list", "steps") {
		return ErrInvalid
	}
	return nil
}

type Store struct {
	root string
	mu   sync.Mutex
}

func NewStore(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, ErrInvalid
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (s *Store) read(owner string) (State, error) {
	if !validID(owner) {
		return State{}, ErrInvalid
	}
	data, err := os.ReadFile(filepath.Join(s.root, owner+".json"))
	if errors.Is(err, os.ErrNotExist) {
		p := Default()
		p.ID = owner
		p.UpdatedAt = time.Now().UTC()
		// Reuse the existing memory/history owner so migration never moves user data.
		return State{ActiveID: owner, Profiles: []Profile{p}}, nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err = json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	if state.Active().ID == "" {
		return State{}, ErrInvalid
	}
	return state, nil
}

func (s *Store) write(owner string, state State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.root, ".profile-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(s.root, owner+".json"))
}

func (s *Store) Get(owner string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.read(owner)
	if err != nil {
		return State{}, err
	}
	if _, err = os.Stat(filepath.Join(s.root, owner+".json")); errors.Is(err, os.ErrNotExist) {
		err = s.write(owner, state)
	}
	return state, err
}

// Save creates an inactive profile or updates an existing profile. Switching is
// committed separately, only after its conversation has loaded successfully.
func (s *Store) Save(owner, expectedID string, p Profile, create bool) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.read(owner)
	if err != nil {
		return State{}, err
	}
	if state.ActiveID != expectedID {
		return State{}, ErrConflict
	}
	p.Name, p.Description, p.Constraints = strings.TrimSpace(p.Name), strings.TrimSpace(p.Description), strings.TrimSpace(p.Constraints)
	if err = Validate(p); err != nil {
		return State{}, err
	}
	if create {
		var id [16]byte
		if _, err = rand.Read(id[:]); err != nil {
			return State{}, err
		}
		p.ID = hex.EncodeToString(id[:])
		p.UpdatedAt = time.Now().UTC()
		state.Profiles = append(state.Profiles, p)
	} else {
		found := false
		for i, old := range state.Profiles {
			if old.ID != p.ID {
				continue
			}
			if !old.UpdatedAt.Equal(p.UpdatedAt) {
				return State{}, ErrConflict
			}
			p.UpdatedAt = time.Now().UTC()
			state.Profiles[i] = p
			found = true
			break
		}
		if !found {
			return State{}, ErrConflict
		}
	}
	if err = s.write(owner, state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (s *Store) Switch(owner, expectedID, targetID string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.read(owner)
	if err != nil {
		return State{}, err
	}
	if state.ActiveID != expectedID {
		return State{}, ErrConflict
	}
	found := false
	for _, p := range state.Profiles {
		if p.ID == targetID {
			found = true
			break
		}
	}
	if !found {
		return State{}, ErrConflict
	}
	state.ActiveID = targetID
	if err = s.write(owner, state); err != nil {
		return State{}, err
	}
	return state, nil
}
