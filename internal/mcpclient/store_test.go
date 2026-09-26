package mcpclient

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreSecretsRestartAndEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Save(Connection{Name: "Neurly", URL: "https://neurly.ru/v1/mcp"}, "private-key", false)
	if err != nil {
		t.Fatal(err)
	}
	s, err = NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(list)
	if len(list) != 1 || !list[0].HasToken || strings.Contains(string(encoded), "private-key") {
		t.Fatalf("unsafe list %s", encoded)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions %v", info.Mode())
	}
	c.Name = "Renamed"
	updated, err := s.Save(c, "", false)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := s.Get(updated.ID, updated.Version)
	if err != nil || token != "private-key" {
		t.Fatal("key was not preserved")
	}
	if _, err = s.Save(c, "", false); !errors.Is(err, ErrConflict) {
		t.Fatal("stale edit accepted")
	}
	updated.URL = "https://other.example/mcp"
	updated, err = s.Save(updated, "", false)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err = s.Get(updated.ID, updated.Version)
	if err != nil || token != "" || updated.HasToken {
		t.Fatal("secret retained for another endpoint")
	}
	updated, err = s.Save(updated, "replacement", false)
	if err != nil {
		t.Fatal(err)
	}
	updated, err = s.Save(updated, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if updated.HasToken {
		t.Fatal("clear token failed")
	}
	if err = s.Delete(updated.ID, updated.Version); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Get(updated.ID, updated.Version); !errors.Is(err, ErrConflict) {
		t.Fatal("deleted connection exists")
	}
}
