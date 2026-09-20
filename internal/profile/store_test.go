package profile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProfilesPersistValidateAndIsolate(t *testing.T) {
	root := t.TempDir()
	s, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	const owner = "00112233445566778899aabbccddeeff"
	initial, err := s.Get(owner)
	if err != nil {
		t.Fatal(err)
	}
	if initial.ActiveID != owner || initial.Active().Language != "auto" {
		t.Fatal("legacy identity/default changed")
	}
	state, err := s.Save(owner, owner, Presets()[0], true)
	if err != nil {
		t.Fatal(err)
	}
	id := state.Profiles[1].ID
	if state.ActiveID != owner {
		t.Fatal("creation silently switched identity")
	}
	if _, err = s.Switch(owner, owner, id); err != nil {
		t.Fatal(err)
	}
	s, _ = NewStore(root)
	state, err = s.Get(owner)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveID != id || state.Active().Name != "Новичок" {
		t.Fatal("restart lost profile")
	}
	old := state.Active()
	updated := old
	updated.Language = "en"
	if _, err = s.Save(owner, id, updated, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(owner, id, old, false); !errors.Is(err, ErrConflict) {
		t.Fatal("stale edit accepted")
	}
	updated.Language = "unknown"
	if _, err = s.Save(owner, id, updated, true); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad enum accepted")
	}
	if _, err = s.Switch(owner, owner, id); !errors.Is(err, ErrConflict) {
		t.Fatal("stale switch accepted")
	}
	if _, err = s.Switch("11112233445566778899aabbccddeeff", "11112233445566778899aabbccddeeff", id); !errors.Is(err, ErrConflict) {
		t.Fatal("other browser accessed profile")
	}
	if _, err = s.Get("../escape"); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid identity accepted")
	}
	info, err := os.Stat(filepath.Join(root, owner+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("profile file not private")
	}
}
