package chat

import (
	"context"
	"errors"
	"testing"
)

type fakeResponder struct {
	previousIDs []string
	options     []ResponseOptions
	callCount   int
	fail        bool
}

func (f *fakeResponder) Respond(_ context.Context, _ string, previousID string, options ResponseOptions) (Result, error) {
	f.previousIDs = append(f.previousIDs, previousID)
	f.options = append(f.options, options)
	if f.fail {
		return Result{}, errors.New("request failed")
	}
	f.callCount++
	return Result{ResponseID: "resp_" + string(rune('0'+f.callCount)), Output: "answer"}, nil
}

func TestSessionCarriesAndResetsResponseID(t *testing.T) {
	responder := &fakeResponder{}
	session := NewSession(responder)

	if _, err := session.Ask(context.Background(), "first", ResponseOptions{}); err != nil {
		t.Fatalf("first Ask() error = %v", err)
	}
	if _, err := session.Ask(context.Background(), "second", ResponseOptions{}); err != nil {
		t.Fatalf("second Ask() error = %v", err)
	}
	session.Reset()
	if _, err := session.Ask(context.Background(), "third", ResponseOptions{}); err != nil {
		t.Fatalf("third Ask() error = %v", err)
	}

	want := []string{"", "resp_1", ""}
	for i := range want {
		if responder.previousIDs[i] != want[i] {
			t.Fatalf("previousIDs[%d] = %q, want %q", i, responder.previousIDs[i], want[i])
		}
	}
}

func TestSessionKeepsStateAfterError(t *testing.T) {
	responder := &fakeResponder{}
	session := NewSession(responder)

	if _, err := session.Ask(context.Background(), "first", ResponseOptions{}); err != nil {
		t.Fatalf("first Ask() error = %v", err)
	}
	responder.fail = true
	if _, err := session.Ask(context.Background(), "second", ResponseOptions{}); err == nil {
		t.Fatal("second Ask() error = nil, want an error")
	}
	responder.fail = false
	if _, err := session.Ask(context.Background(), "retry", ResponseOptions{}); err != nil {
		t.Fatalf("retry Ask() error = %v", err)
	}

	if got := responder.previousIDs[2]; got != "resp_1" {
		t.Fatalf("previous ID after error = %q, want resp_1", got)
	}
}

func TestSessionPassesTrimmedResponseOptions(t *testing.T) {
	responder := &fakeResponder{}
	session := NewSession(responder)
	options := ResponseOptions{
		Model:               "  gpt-test  ",
		Format:              "  JSON  ",
		LengthLimit:         "  200 words ",
		CompletionCondition: " after summary  ",
	}

	if _, err := session.Ask(context.Background(), "question", options); err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	want := ResponseOptions{Model: "gpt-test", Format: "JSON", LengthLimit: "200 words", CompletionCondition: "after summary"}
	if got := responder.options[0]; got != want {
		t.Fatalf("options = %#v, want %#v", got, want)
	}
}

func TestSessionStartsFreshResponseChainAfterModelChange(t *testing.T) {
	responder := &fakeResponder{}
	session := NewSession(responder)

	if _, err := session.Ask(context.Background(), "first", ResponseOptions{Model: "model-a"}); err != nil {
		t.Fatalf("first Ask() error = %v", err)
	}
	if _, err := session.Ask(context.Background(), "second", ResponseOptions{Model: "model-a"}); err != nil {
		t.Fatalf("second Ask() error = %v", err)
	}
	if _, err := session.Ask(context.Background(), "third", ResponseOptions{Model: "model-b"}); err != nil {
		t.Fatalf("third Ask() error = %v", err)
	}

	want := []string{"", "resp_1", ""}
	for i := range want {
		if responder.previousIDs[i] != want[i] {
			t.Fatalf("previousIDs[%d] = %q, want %q", i, responder.previousIDs[i], want[i])
		}
	}
}
