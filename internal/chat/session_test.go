package chat

import (
	"context"
	"errors"
	"testing"
)

type fakeResponder struct {
	previousIDs []string
	callCount   int
	fail        bool
}

func (f *fakeResponder) Respond(_ context.Context, _ string, previousID string) (string, string, error) {
	f.previousIDs = append(f.previousIDs, previousID)
	if f.fail {
		return "", "", errors.New("request failed")
	}
	f.callCount++
	return "resp_" + string(rune('0'+f.callCount)), "answer", nil
}

func TestSessionCarriesAndResetsResponseID(t *testing.T) {
	responder := &fakeResponder{}
	session := NewSession(responder)

	if _, err := session.Ask(context.Background(), "first"); err != nil {
		t.Fatalf("first Ask() error = %v", err)
	}
	if _, err := session.Ask(context.Background(), "second"); err != nil {
		t.Fatalf("second Ask() error = %v", err)
	}
	session.Reset()
	if _, err := session.Ask(context.Background(), "third"); err != nil {
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

	if _, err := session.Ask(context.Background(), "first"); err != nil {
		t.Fatalf("first Ask() error = %v", err)
	}
	responder.fail = true
	if _, err := session.Ask(context.Background(), "second"); err == nil {
		t.Fatal("second Ask() error = nil, want an error")
	}
	responder.fail = false
	if _, err := session.Ask(context.Background(), "retry"); err != nil {
		t.Fatalf("retry Ask() error = %v", err)
	}

	if got := responder.previousIDs[2]; got != "resp_1" {
		t.Fatalf("previous ID after error = %q, want resp_1", got)
	}
}
