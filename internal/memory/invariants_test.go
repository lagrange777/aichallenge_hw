package memory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyImportInvariantLoadsAsSemanticRule(t *testing.T) {
	root := t.TempDir()
	s, _ := NewStore(root)
	owner := "00112233445566778899aabbccddeeff"
	state, _ := s.Get(owner)
	rule := Invariant{Category: "stack", Title: "Без Gin", Rule: "Не подключать Go-пакет github.com/gin-gonic/gin и его подпакеты."}
	state, err := s.UpdateInvariants(owner, InvariantCommand{TaskID: state.Task.ID, Version: state.Invariants.Version, Action: "create", Rule: rule})
	if err != nil {
		t.Fatal(err)
	}
	var rev string
	if err := readJSON(filepath.Join(root, owner, "CURRENT.json"), &rev); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, owner, rev, "invariants.json")
	var legacy map[string]any
	if err := readJSON(path, &legacy); err != nil {
		t.Fatal(err)
	}
	item := legacy[state.Task.ID].(map[string]any)["items"].([]any)[0].(map[string]any)
	item["kind"] = "forbidden_go_import"
	item["importPath"] = "github.com/gin-gonic/gin"
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	s, _ = NewStore(root)
	state, err = s.Get(owner)
	if err != nil || len(state.Invariants.Active()) != 1 || state.Invariants.Items[0].Rule != rule.Rule {
		t.Fatalf("legacy rule lost: %+v, %v", state.Invariants, err)
	}
	data, _ = json.Marshal(state.Invariants)
	if strings.Contains(string(data), `"kind"`) || strings.Contains(string(data), `"importPath"`) {
		t.Fatal("obsolete check settings exposed")
	}
	rule = state.Invariants.Items[0]
	rule.Rule = "Использовать только стандартную библиотеку Go."
	state, err = s.UpdateInvariants(owner, InvariantCommand{TaskID: state.Task.ID, Version: state.Invariants.Version, Action: "edit", Rule: rule})
	if err != nil || state.Invariants.Items[0].Rule != rule.Rule {
		t.Fatal("legacy rule cannot be edited as text")
	}
}

func TestInvariantStorageLifecycleAndTaskIsolation(t *testing.T) {
	root := t.TempDir()
	s, _ := NewStore(root)
	owner := "00112233445566778899aabbccddeeff"
	state, _ := s.Get(owner)
	rule := Invariant{Category: "stack", Title: "Без Gin", Rule: "Не подключать Go-пакет github.com/gin-gonic/gin и его подпакеты."}
	command := func(action string, r Invariant) {
		t.Helper()
		var err error
		state, err = s.UpdateInvariants(owner, InvariantCommand{TaskID: state.Task.ID, Version: state.Invariants.Version, Action: action, Rule: r})
		if err != nil {
			t.Fatal(err)
		}
	}
	before := state.Task.Workflow.Version
	command("create", rule)
	rule = state.Invariants.Items[0]
	if rule.Status != "active" || rule.Rule == "" || state.Task.Workflow.Version <= before {
		t.Fatal("rule not activated or chat version not invalidated")
	}
	if _, err := s.UpdateInvariants(owner, InvariantCommand{TaskID: state.Task.ID, Version: 1, Action: "disable", Rule: rule}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale update accepted")
	}
	first := state.Task.ID
	state, _ = s.NewTask(owner, first, "Second")
	if len(state.Invariants.Items) != 0 {
		t.Fatal("new task inherited invariants")
	}
	second := state.Task.ID
	state, _ = s.SwitchTask(owner, second, first)
	if len(state.Invariants.Active()) != 1 {
		t.Fatal("switch lost invariant")
	}
	s, _ = NewStore(root)
	state, _ = s.Get(owner)
	command("disable", rule)
	if len(state.Invariants.Active()) != 0 {
		t.Fatal("disabled rule active")
	}
	rule = state.Invariants.Items[0]
	rule.Title = "Без Gin и подпакетов"
	command("edit", rule)
	command("activate", state.Invariants.Items[0])
	if len(state.Invariants.Events) != 4 || state.Invariants.Items[0].Version != 4 {
		t.Fatal("audit/version missing")
	}
	var rev string
	_ = readJSON(filepath.Join(root, owner, "CURRENT.json"), &rev)
	if _, err := os.Stat(filepath.Join(root, owner, rev, "invariants.json")); err != nil {
		t.Fatal("missing separate file")
	}
	other, _ := s.Get("ffeeddccbbaa9988776655443322110099")
	if len(other.Invariants.Items) != 0 {
		t.Fatal("profile leak")
	}
	// Legacy revisions without this file are still readable.
	if err := os.Remove(filepath.Join(root, owner, rev, "invariants.json")); err != nil {
		t.Fatal(err)
	}
	state, err := s.Get(owner)
	if err != nil || state.Invariants.Version != 1 {
		t.Fatal("legacy migration failed")
	}
}
func TestInvariantProposalsNeedConfirmation(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	owner := "00112233445566778899aabbccddeeff"
	state, _ := s.Get(owner)
	rules := []Invariant{{Category: "architecture", Title: "Монолит", Rule: "Один развёртываемый сервис", Status: "active"}}
	state, err := s.ProposeInvariants(owner, state.Task.ID, rules)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Invariants.Active()) != 0 || state.Invariants.Items[0].Status != "proposed" {
		t.Fatal("model activated rule")
	}
	rule := state.Invariants.Items[0]
	state, err = s.UpdateInvariants(owner, InvariantCommand{TaskID: state.Task.ID, Version: state.Invariants.Version, Action: "reject", Rule: rule})
	if err != nil {
		t.Fatal(err)
	}
	state, err = s.ProposeInvariants(owner, state.Task.ID, rules)
	if err != nil || len(state.Invariants.Items) != 1 {
		t.Fatal("rejected rule reproposed")
	}
}
