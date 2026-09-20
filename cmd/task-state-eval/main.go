// task-state-eval checks continuation from synthetic checkpoints using the
// configured model. All task data is isolated in a temporary directory.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/config"
	"codex-chat-cli/internal/history"
	"codex-chat-cli/internal/memory"
	"codex-chat-cli/internal/openai"
)

func main() {
	output := flag.String("output", "task-state-evaluation.json", "JSON report path")
	flag.Parse()
	if err := run(*output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(output string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	client, err := openai.NewClient(cfg.APIKey, cfg.BaseURL, "You are a helpful coding assistant. Answer in Russian.", &http.Client{Timeout: cfg.Timeout})
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "task-state-eval-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	store, err := memory.NewStore(filepath.Join(root, "memory"))
	if err != nil {
		return err
	}
	hist, err := history.NewJSONStore(filepath.Join(root, "history.json"))
	if err != nil {
		return err
	}
	owner := "00112233445566778899aabbccddeeff"
	type result struct {
		Stage        string               `json:"stage"`
		Checkpoint   memory.Progress      `json:"checkpoint"`
		PauseBlocked bool                 `json:"pauseBlocked"`
		Question     string               `json:"question"`
		Answer       string               `json:"answer"`
		Proposal     *memory.TaskProposal `json:"proposal"`
		Warning      string               `json:"warning,omitempty"`
	}
	report := struct {
		Model   string    `json:"model"`
		Date    time.Time `json:"date"`
		Results []result  `json:"results"`
	}{Model: cfg.Model, Date: time.Now().UTC()}
	for _, stage := range []string{"planning", "execution", "validation"} {
		fmt.Fprintln(os.Stderr, "Проверка продолжения:", stage)
		a, err := agent.NewPersistent(client, cfg.Model, owner, hist, agent.WithMemory(store))
		if err != nil {
			return err
		}
		view, err := a.Memories()
		if err != nil {
			return err
		}
		view, err = a.NewTask(view.Task.ID, "Валидатор имени пользователя")
		if err != nil {
			return err
		}
		p := view.Task.Workflow.Progress
		p.Goal = "Подготовить Go-функцию validUsername: от 3 до 16 ASCII-символов, только латинские буквы, цифры и подчёркивание. Без сторонних библиотек."
		p.CurrentStep = "Составить короткий план реализации и проверки"
		p.ExpectedActor = "agent"
		p.ExpectedAction = "Предложить план по известным требованиям, без повторного запроса цели"
		update := func(action string) error {
			var e error
			view, e = a.UpdateWorkflow(memory.WorkflowCommand{TaskID: view.Task.ID, Version: view.Task.Workflow.Version, Action: action, Progress: p})
			return e
		}
		if err = update("save"); err != nil {
			return err
		}
		if stage != "planning" {
			p.Stage = "execution"
			p.CurrentStep = "Написать функцию validUsername"
			p.ExpectedAction = "Показать Go-код, не повторяя согласованный план"
			p.Completed = "План утверждён: сначала длина, затем каждый ASCII-символ. Правила и ограничения согласованы."
			if err = update("save"); err != nil {
				return err
			}
		}
		if stage == "validation" {
			p.Stage = "validation"
			p.CurrentStep = "Проверить предложенную реализацию по всем требованиям"
			p.ExpectedAction = "Найти расхождения с требованиями; не утверждать, что тесты запускались"
			p.Result = "func validUsername(s string) bool { return len(s) >= 3 && len(s) <= 16 }"
			p.Completed += " Подготовлен первый вариант кода."
			if err = update("save"); err != nil {
				return err
			}
		}
		if err = update("pause"); err != nil {
			return err
		}
		store, err = memory.NewStore(filepath.Join(root, "memory"))
		if err != nil {
			return err
		}
		a, err = agent.NewPersistent(client, cfg.Model, owner, hist, agent.WithMemory(store))
		if err != nil {
			return err
		}
		_, err = a.Ask(context.Background(), agent.Request{Message: "Продолжи"})
		blocked := errors.Is(err, agent.ErrTaskPaused)
		if !blocked {
			return fmt.Errorf("pause failed: %v", err)
		}
		if err = update("resume"); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
		answer, err := a.Ask(ctx, agent.Request{Message: "Продолжи", LengthLimit: "Кратко, до 120 слов", ContextStrategy: "sliding_window"})
		cancel()
		if err != nil {
			return err
		}
		after, err := a.Memories()
		if err != nil {
			return err
		}
		report.Results = append(report.Results, result{stage, p, blocked, "Продолжи", answer.Text, after.Task.Workflow.Proposal, answer.TokenMetrics.ContextWarning})
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		if err = os.WriteFile(output, data, 0600); err != nil {
			return err
		}
	}
	return nil
}
