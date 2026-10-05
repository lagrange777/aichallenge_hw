package agent

import (
	"codex-chat-cli/internal/memory"
	"context"
	"errors"
	"strings"
	"testing"
)

type dialogueRetriever struct {
	queries []string
	empty   bool
}

func (r *dialogueRetriever) Retrieve(ctx context.Context, q string) (*Retrieval, error) {
	return r.RetrieveConfigured(ctx, q, DefaultRetrievalOptions())
}
func (r *dialogueRetriever) RetrieveConfigured(_ context.Context, q string, o RetrievalOptions) (*Retrieval, error) {
	r.queries = append(r.queries, q)
	sources := []Source{{Ref: "S1", Source: "file.md", ChunkID: "one", Section: "TTL", Text: "History persists.\nTTL is 12 hours."}}
	if r.empty {
		sources = nil
	}
	return &Retrieval{Options: &o, Sources: sources}, nil
}
func dialogueOptions() RetrievalOptions {
	o := DefaultRetrievalOptions()
	o.Mode = "filter"
	o.Grounded = true
	o.Dialogue = true
	return o
}
func TestDialogueUpdatesRequireCurrentUserEvidence(t *testing.T) {
	p := dialoguePlan{Scope: "documents", Query: "TTL?", Updates: []dialogueUpdate{{Kind: "goal", Key: "goal", Quote: "Keep history"}}}
	s, err := applyDialoguePlan(nil, p, "Keep history. Use Docker.")
	if err != nil {
		t.Fatal(err)
	}
	if s.Entries[0].Ref != "U1" || s.Sources[0].Text != "Keep history. Use Docker." {
		t.Fatal(s)
	}
	p.Updates[0].Quote = "unsupported"
	if _, err = applyDialoguePlan(s, p, "Keep history"); err == nil {
		t.Fatal("invented memory accepted")
	}
	p.Updates = []dialogueUpdate{{Kind: "constraint", Key: "deploy", Quote: "Use Docker"}}
	s, err = applyDialoguePlan(s, p, "Use Docker")
	if err != nil {
		t.Fatal(err)
	}
	p.Updates[0].Quote = "Use local process"
	s, err = applyDialoguePlan(s, p, "Use local process")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 2 || s.Entries[1].Text != "Use local process" || len(s.Sources) != 2 {
		t.Fatal("correction retained old source", s)
	}
	p.Updates[0].Delete = true
	p.Updates[0].Quote = "Forget deployment"
	s, err = applyDialoguePlan(s, p, "Forget deployment")
	if err != nil || len(s.Entries) != 1 || len(s.Sources) != 1 {
		t.Fatal("deletion failed", s, err)
	}
}
func TestDialogueResolvesBeforeSearchAndRestores(t *testing.T) {
	hist := &memoryHistory{}
	retriever := &dialogueRetriever{}
	l := &groundingLLM{outputs: []string{`{"scope":"documents","query":"When is the agent removed from RAM?","clarification":"","updates":[{"kind":"goal","key":"goal","quote":"Understand history","delete":false}]}`, goodDraft, goodCheck}}
	a, _ := NewPersistent(l, "test", "dialogue", hist, WithRetriever(retriever))
	o := dialogueOptions()
	r, err := a.Ask(context.Background(), Request{Message: "Understand history. TTL?", RAG: true, RAGOptions: &o})
	if err != nil {
		t.Fatal(err)
	}
	if retriever.queries[0] != "When is the agent removed from RAM?" || r.Retrieval.OriginalQuery != "Understand history. TTL?" || a.DialogueSnapshot().Version != 1 {
		t.Fatal("question not resolved")
	}
	if !strings.Contains(l.calls[1].Input, "Understand history") || !strings.Contains(l.calls[1].Instructions, "never proof of project behavior") {
		t.Fatal("memory boundary missing")
	}
	restored, _ := NewPersistent(l, "test", "dialogue", hist, WithRetriever(retriever))
	if restored.DialogueSnapshot().Entries[0].Text != "Understand history" || len(restored.Messages()) != 2 {
		t.Fatal("restart lost context")
	}
	copy := restored.DialogueSnapshot()
	copy.Entries[0].Text = "changed"
	if restored.DialogueSnapshot().Entries[0].Text == "changed" {
		t.Fatal("snapshot aliases")
	}
}
func TestDialogueWeakContextStillRefusesWithMemory(t *testing.T) {
	l := &groundingLLM{outputs: []string{`{"scope":"documents","query":"SLA?","clarification":"","updates":[{"kind":"goal","key":"goal","quote":"SLA research","delete":false}]}`}}
	a := New(l, "test", WithRetriever(&dialogueRetriever{empty: true}))
	o := dialogueOptions()
	r, err := a.Ask(context.Background(), Request{Message: "SLA research: what percent?", RAG: true, RAGOptions: &o})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.calls) != 1 || !strings.Contains(r.Text, "Источники: подтверждающие источники не найдены") || r.Retrieval.Grounding.Status != "unknown" {
		t.Fatal(r)
	}
}

func TestDialogueRecallsUserGoalWithEmptyDocumentSearch(t *testing.T) {
	l := &groundingLLM{outputs: []string{
		`{"scope":"memory","query":"History goal","clarification":"","updates":[]}`,
		`{"status":"answered","claims":[{"text":"Your goal is to understand history.","evidence":[{"ref":"U1","start":1,"end":1}]}]}`,
		`{"claims":[{"index":0,"verdict":"supported","reason":"User explicitly stated this goal"}],"complete":true}`,
	}}
	retriever := &dialogueRetriever{empty: true}
	a := New(l, "test", WithRetriever(retriever))
	a.dialogue = &DialogueState{Enabled: true, Turn: 1, Version: 1,
		Entries: []DialogueEntry{{Kind: "goal", Key: "goal", Text: "Understand history", Ref: "U1"}},
		Sources: []Source{{Ref: "U1", Source: "User message U1", ChunkID: "user-U1", Text: "Understand history"}},
	}
	o := dialogueOptions()
	r, err := a.Ask(context.Background(), Request{Message: "Recall my goal", RAG: true, RAGOptions: &o})
	if err != nil {
		t.Fatal(err)
	}
	if len(retriever.queries) != 1 || r.Retrieval.Grounding.Status != "answered" ||
		!strings.Contains(r.Text, "[U1]") || r.Retrieval.Grounding.Quotes[0].Text != "Understand history" {
		t.Fatalf("missing fresh search or user evidence: %+v", r)
	}
}
func TestDialogueFailureDoesNotCommitState(t *testing.T) {
	l := &groundingLLM{outputs: []string{`{"scope":"documents","query":"TTL?","clarification":"","updates":[{"kind":"goal","key":"goal","quote":"My goal","delete":false}]}`}}
	hist := &memoryHistory{}
	a, _ := NewPersistent(l, "test", "rollback", hist, WithRetriever(&dialogueRetriever{}))
	o := dialogueOptions()
	if _, err := a.Ask(context.Background(), Request{Message: "My goal: TTL?", RAG: true, RAGOptions: &o}); err == nil || a.DialogueSnapshot() != nil || len(a.Messages()) != 0 || hist.saved {
		t.Fatal("failed answer changed durable context")
	}
}
func TestDialogueTaskIsolation(t *testing.T) {
	store, _ := memory.NewStore(t.TempDir())
	hist := &memoryHistory{}
	a, _ := NewPersistent(&groundingLLM{}, "test", profileID, hist, WithMemory(store))
	first, _ := a.Memories()
	a.dialogue = &DialogueState{Enabled: true, Turn: 1, Entries: []DialogueEntry{{Kind: "goal", Key: "goal", Text: "First task"}}}
	second, err := a.NewTask(first.Task.ID, "Second")
	if err != nil {
		t.Fatal(err)
	}
	if a.DialogueSnapshot() != nil {
		t.Fatal("new task inherited dialogue")
	}
	if _, err = a.SwitchTask(second.Task.ID, first.Task.ID); err != nil {
		t.Fatal(err)
	}
	if a.DialogueSnapshot() == nil || a.DialogueSnapshot().Entries[0].Text != "First task" {
		t.Fatal("task state not restored")
	}
	if err = a.Reset(); err != nil {
		t.Fatal(err)
	}
	if a.DialogueSnapshot() != nil {
		t.Fatal("reset left context")
	}
}
func TestDialogueAmbiguousQuestionSearchesButDoesNotInvent(t *testing.T) {
	l := &groundingLLM{outputs: []string{`{"scope":"documents","query":"А она?","clarification":"История или память?","updates":[]}`}}
	retriever := &dialogueRetriever{}
	a := New(l, "test", WithRetriever(retriever))
	o := dialogueOptions()
	r, err := a.Ask(context.Background(), Request{Message: "А она?", RAG: true, RAGOptions: &o})
	if err != nil {
		t.Fatal(err)
	}
	if len(retriever.queries) != 1 || len(l.calls) != 1 || r.Retrieval.Grounding.Reason != "ambiguous_question" {
		t.Fatal(r)
	}
}
func TestDialogueRequiresRAGAndWindow(t *testing.T) {
	o := dialogueOptions()
	a := New(&groundingLLM{}, "test")
	if _, err := a.Ask(context.Background(), Request{Message: "q", RAGOptions: &o}); err == nil {
		t.Fatal("RAG disabled accepted")
	}
	_, err := a.Ask(context.Background(), Request{Message: "q", RAG: true, RAGOptions: &o, ContextStrategy: "branching"})
	if err == nil || errors.Is(err, ErrHistoryNotFound) {
		t.Fatal("branching accepted")
	}
}
