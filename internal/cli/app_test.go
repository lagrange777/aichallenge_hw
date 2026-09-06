package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeSession struct {
	inputs []string
	resets int
	err    error
}

func (f *fakeSession) Ask(_ context.Context, input string) (string, error) {
	f.inputs = append(f.inputs, input)
	if f.err != nil {
		return "", f.err
	}
	return "reply to " + input, nil
}

func (f *fakeSession) Reset() {
	f.resets++
}

func TestRunChatAndCommands(t *testing.T) {
	in := strings.NewReader("hello\n/reset\nworld\n/exit\n")
	var out bytes.Buffer
	var errOut bytes.Buffer
	session := &fakeSession{}

	app := New(in, &out, &errOut, session, "test-model")
	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if session.resets != 1 {
		t.Fatalf("resets = %d", session.resets)
	}
	if len(session.inputs) != 2 || session.inputs[0] != "hello" || session.inputs[1] != "world" {
		t.Fatalf("inputs = %#v", session.inputs)
	}
	if !strings.Contains(out.String(), ">_ OpenAI Codex Chat") ||
		!strings.Contains(out.String(), "• codex\n  reply to hello") ||
		!strings.Contains(out.String(), "Новый диалог начат") {
		t.Fatalf("output = %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestRunStatusHelpAndUnknownCommand(t *testing.T) {
	in := strings.NewReader("/status\n/help\n/nope\n/exit\n")
	var out bytes.Buffer
	var errOut bytes.Buffer
	session := &fakeSession{}

	app := New(in, &out, &errOut, session, "test-model")
	app.cwd = "/tmp/example"
	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(session.inputs) != 0 {
		t.Fatalf("inputs = %#v", session.inputs)
	}
	for _, expected := range []string{"Сессия", "test-model", "/tmp/example", "Команды", "/new, /reset"} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("output does not contain %q: %q", expected, out.String())
		}
	}
	if !strings.Contains(errOut.String(), "Неизвестная команда /nope") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestRunMultilineInput(t *testing.T) {
	in := strings.NewReader("first line\\\nsecond line\n/exit\n")
	var out bytes.Buffer
	var errOut bytes.Buffer
	session := &fakeSession{}

	app := New(in, &out, &errOut, session, "test-model")
	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(session.inputs) != 1 || session.inputs[0] != "first line\nsecond line" {
		t.Fatalf("inputs = %#v", session.inputs)
	}
	if !strings.Contains(out.String(), "· ") {
		t.Fatalf("continuation prompt missing: %q", out.String())
	}
}

func TestRunPrintsRequestErrorAndContinues(t *testing.T) {
	in := strings.NewReader("hello\n/exit\n")
	var out bytes.Buffer
	var errOut bytes.Buffer
	session := &fakeSession{err: errors.New("temporary failure")}

	app := New(in, &out, &errOut, session, "test-model")
	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.Contains(errOut.String(), "Запрос не выполнен") ||
		!strings.Contains(errOut.String(), "temporary failure") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestSanitizeTerminalText(t *testing.T) {
	input := "safe\x1b[2J\rtext\x00\nnext"
	if got, want := sanitizeTerminalText(input), "safe[2Jtext\nnext"; got != want {
		t.Fatalf("sanitizeTerminalText() = %q, want %q", got, want)
	}
}

func TestNonInteractiveOutputHasNoANSISequences(t *testing.T) {
	in := strings.NewReader("/exit\n")
	var out bytes.Buffer
	app := New(in, &out, &out, &fakeSession{}, "test-model")

	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("output contains ANSI controls: %q", out.String())
	}
}
