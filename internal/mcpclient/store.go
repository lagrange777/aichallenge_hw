package mcpclient

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

var ErrConflict = errors.New("Подключение изменилось или удалено. Обновите список")

// Connection is a public view. Secrets are stored separately and never returned.
type Connection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	HasToken bool   `json:"hasToken"`
	Version  int    `json:"version"`
}
type savedConnection struct {
	Connection
	Token string `json:"token"`
}
type Store struct {
	mu   sync.Mutex
	path string
}

func NewStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	s := &Store{path: path}
	_, err := s.read()
	return s, err
}
func (s *Store) read() ([]savedConnection, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []savedConnection{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records []savedConnection
	err = json.Unmarshal(data, &records)
	return records, err
}
func (s *Store) write(records []savedConnection) error {
	file, err := os.CreateTemp(filepath.Dir(s.path), ".mcp-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err = json.NewEncoder(file).Encode(records); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), s.path)
}
func (s *Store) List() ([]Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.read()
	if err != nil {
		return nil, err
	}
	list := make([]Connection, 0, len(records))
	for _, record := range records {
		list = append(list, record.Connection)
	}
	return list, nil
}
func (s *Store) Save(c Connection, token string, clearToken bool) (Connection, error) {
	c.Name, c.URL = strings.TrimSpace(c.Name), strings.TrimSpace(c.URL)
	token = strings.TrimSpace(token)
	if c.Name == "" || utf8.RuneCountInString(c.Name) > 100 || len(token) > 8192 || strings.ContainsAny(token, "\r\n") {
		return Connection{}, errors.New("Укажите имя до 100 символов и корректный ключ до 8192 байт")
	}
	if err := ValidateEndpoint(c.URL); err != nil {
		return Connection{}, err
	}
	if clearToken && token != "" {
		return Connection{}, errors.New("Нельзя одновременно заменить и удалить ключ")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.read()
	if err != nil {
		return Connection{}, err
	}
	index := -1
	for i, record := range records {
		if record.ID == c.ID {
			index = i
			break
		}
	}
	if c.ID != "" {
		if index < 0 || records[index].Version != c.Version {
			return Connection{}, ErrConflict
		}
		old := records[index]
		// Never send an existing secret to a newly edited endpoint without re-entry.
		if token == "" && !clearToken && c.URL == old.URL {
			token = old.Token
		}
	} else {
		if len(records) >= 20 {
			return Connection{}, errors.New("Можно сохранить не более 20 MCP-серверов")
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return Connection{}, err
		}
		c.ID, c.Version = hex.EncodeToString(id[:]), 0
	}
	c.HasToken, c.Version = token != "", c.Version+1
	record := savedConnection{c, token}
	if index < 0 {
		records = append(records, record)
	} else {
		records[index] = record
	}
	return c, s.write(records)
}
func (s *Store) Get(id string, version int) (Connection, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.read()
	if err != nil {
		return Connection{}, "", err
	}
	for _, record := range records {
		if record.ID == id && record.Version == version {
			return record.Connection, record.Token, nil
		}
	}
	return Connection{}, "", ErrConflict
}
func (s *Store) Delete(id string, version int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.read()
	if err != nil {
		return err
	}
	for i, record := range records {
		if record.ID == id && record.Version == version {
			return s.write(append(records[:i], records[i+1:]...))
		}
	}
	return ErrConflict
}
