package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const maxInputBytes = 1024 * 1024

type Session interface {
	Ask(ctx context.Context, input string) (string, error)
	Reset()
}

type App struct {
	in          io.Reader
	out         io.Writer
	errOut      io.Writer
	session     Session
	model       string
	cwd         string
	turns       int
	interactive bool
	theme       theme
}

func New(in io.Reader, out, errOut io.Writer, session Session, model string) *App {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	interactive := isTerminal(out)
	return &App{
		in:          in,
		out:         out,
		errOut:      errOut,
		session:     session,
		model:       model,
		cwd:         cwd,
		interactive: interactive,
		theme:       newTheme(interactive),
	}
}

// Run starts the interactive read-evaluate-print loop.
func (a *App) Run(ctx context.Context) error {
	a.printWelcome()

	scanner := bufio.NewScanner(a.in)
	scanner.Buffer(make([]byte, 1024), maxInputBytes)

	for {
		a.printPrompt()
		input, ok, err := a.readInput(scanner)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(a.out)
			return nil
		}
		if input == "" {
			continue
		}

		handled, exit := a.handleCommand(input)
		if exit {
			return nil
		}
		if handled {
			continue
		}

		answer, err := a.askWithActivity(ctx, input)
		if err != nil {
			if errors.Is(err, context.Canceled) ||
				(errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil) {
				return err
			}
			a.printError(err)
			continue
		}

		a.turns++
		a.printAnswer(answer)
	}
}

func (a *App) readInput(scanner *bufio.Scanner) (string, bool, error) {
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", false, fmt.Errorf("прочитать ввод: %w", err)
		}
		return "", false, nil
	}

	lines := make([]string, 0, 2)
	line := scanner.Text()
	for {
		part, continued := trimContinuation(line)
		lines = append(lines, part)
		if totalInputBytes(lines) > maxInputBytes {
			return "", false, fmt.Errorf("ввод превышает предел %d байт", maxInputBytes)
		}
		if !continued {
			break
		}

		a.printContinuationPrompt()
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", false, fmt.Errorf("прочитать продолжение ввода: %w", err)
			}
			break
		}
		line = scanner.Text()
	}

	return strings.TrimSpace(strings.Join(lines, "\n")), true, nil
}

func trimContinuation(line string) (string, bool) {
	trimmed := strings.TrimRight(line, " \t")
	backslashes := 0
	for i := len(trimmed) - 1; i >= 0 && trimmed[i] == '\\'; i-- {
		backslashes++
	}
	if backslashes%2 == 0 {
		return line, false
	}
	return trimmed[:len(trimmed)-1], true
}

func totalInputBytes(lines []string) int {
	total := len(lines) - 1
	for _, line := range lines {
		total += len(line)
	}
	return total
}

func (a *App) handleCommand(input string) (handled, exit bool) {
	command := strings.ToLower(strings.TrimSpace(input))
	switch command {
	case "/exit", "/quit":
		return true, true
	case "/help":
		a.printHelp()
		return true, false
	case "/reset", "/new":
		a.session.Reset()
		a.turns = 0
		a.printNotice("Новый диалог начат")
		return true, false
	case "/status":
		a.printStatus()
		return true, false
	case "/clear":
		a.clearScreen()
		return true, false
	}

	if strings.HasPrefix(command, "/") {
		a.printUnknownCommand(command)
		return true, false
	}
	return false, false
}

type askResult struct {
	answer string
	err    error
}

func (a *App) askWithActivity(ctx context.Context, input string) (string, error) {
	if !a.interactive {
		return a.session.Ask(ctx, input)
	}

	result := make(chan askResult, 1)
	go func() {
		answer, err := a.session.Ask(ctx, input)
		result <- askResult{answer: answer, err: err}
	}()

	frames := []string{"◐", "◓", "◑", "◒"}
	frame := 0
	a.printActivity(frames[frame])
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()
	defer a.clearActivity()

	for {
		select {
		case response := <-result:
			return response.answer, response.err
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			frame = (frame + 1) % len(frames)
			a.printActivity(frames[frame])
		}
	}
}
