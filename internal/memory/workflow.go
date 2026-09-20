package memory

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Progress is the user-confirmed checkpoint. Stage changes only through a
// validated command; model output never becomes authoritative on its own.
type Progress struct {
	Stage          string `json:"stage"`
	Goal           string `json:"goal"`
	CurrentStep    string `json:"currentStep"`
	ExpectedActor  string `json:"expectedActor"`
	ExpectedAction string `json:"expectedAction"`
	Completed      string `json:"completed"`
	Result         string `json:"result"`
	OpenQuestions  string `json:"openQuestions"`
	Validation     string `json:"validation"`
}
type TaskProposal struct {
	Progress Progress `json:"progress"`
	Reason   string   `json:"reason"`
}
type TaskEvent struct {
	Action string    `json:"action"`
	From   string    `json:"from"`
	To     string    `json:"to"`
	Step   string    `json:"step"`
	At     time.Time `json:"at"`
}
type TaskTurn struct {
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}
type Workflow struct {
	Progress
	Paused   bool          `json:"paused"`
	Version  int           `json:"version"`
	Proposal *TaskProposal `json:"proposal,omitempty"`
	LastTurn *TaskTurn     `json:"lastTurn,omitempty"`
	Events   []TaskEvent   `json:"events"`
}
type WorkflowCommand struct {
	TaskID    string   `json:"taskId"`
	ProfileID string   `json:"profileId"`
	Version   int      `json:"version"`
	Action    string   `json:"action"`
	Progress  Progress `json:"progress"`
}

func initialWorkflow(name string) Workflow {
	return Workflow{Progress: Progress{Stage: "planning", Goal: name, CurrentStep: "Уточнить цель и составить план", ExpectedActor: "user", ExpectedAction: "Опишите желаемый результат и ограничения"}, Version: 1, Events: []TaskEvent{}}
}
func normalizeTask(t *Task) {
	// Migrate only tasks from before the workflow feature. Never infer progress
	// from old conversations or silently mark legacy tasks completed.
	if t.Workflow.Stage == "" {
		t.Workflow = initialWorkflow(t.Name)
	}
}
func AllowedTransition(from, to string) bool {
	return from == to && (from == "planning" || from == "execution" || from == "validation" || from == "done") ||
		from == "planning" && to == "execution" || from == "execution" && to == "validation" ||
		from == "validation" && (to == "execution" || to == "done")
}
func ValidateProgress(from string, p Progress) error {
	if !AllowedTransition(from, p.Stage) {
		return fmt.Errorf("%w: недопустимый переход этапа", ErrInvalid)
	}
	for _, f := range []struct {
		value string
		max   int
	}{{p.Goal, 2000}, {p.CurrentStep, 500}, {p.ExpectedAction, 2000}, {p.Completed, 6000}, {p.Result, 6000}, {p.OpenQuestions, 2000}, {p.Validation, 4000}} {
		if utf8.RuneCountInString(f.value) > f.max {
			return fmt.Errorf("%w: поле состояния слишком длинное", ErrInvalid)
		}
	}
	if strings.TrimSpace(p.Goal) == "" || strings.TrimSpace(p.CurrentStep) == "" || strings.TrimSpace(p.ExpectedAction) == "" || (p.ExpectedActor != "user" && p.ExpectedActor != "agent") {
		return fmt.Errorf("%w: заполните цель, шаг, исполнителя и ожидаемое действие", ErrInvalid)
	}
	if p.Stage == "validation" && strings.TrimSpace(p.Result) == "" {
		return fmt.Errorf("%w: перед проверкой сохраните результат выполнения", ErrInvalid)
	}
	if p.Stage == "done" && (strings.TrimSpace(p.Result) == "" || strings.TrimSpace(p.Validation) == "") {
		return fmt.Errorf("%w: для завершения нужны результат и итог проверки", ErrInvalid)
	}
	return nil
}
func (s *Store) UpdateWorkflow(owner string, cmd WorkflowCommand) (State, error) {
	return s.update(owner, func(state *State) error {
		w := &state.Task.Workflow
		if state.Task.ID != cmd.TaskID || w.Version != cmd.Version {
			return ErrConflict
		}
		before := w.Stage
		switch cmd.Action {
		case "pause":
			if w.Paused || w.Stage == "done" {
				return ErrConflict
			}
			w.Paused = true
		case "resume":
			if !w.Paused || w.Stage == "done" {
				return ErrConflict
			}
			w.Paused = false
		case "reject":
			if w.Proposal == nil {
				return ErrConflict
			}
			w.Proposal = nil
		case "save", "accept":
			if w.Paused || w.Stage == "done" {
				return ErrConflict
			}
			p := cmd.Progress
			if cmd.Action == "accept" {
				if w.Proposal == nil {
					return ErrConflict
				}
				p = w.Proposal.Progress
			}
			if err := ValidateProgress(w.Stage, p); err != nil {
				return err
			}
			w.Progress = p
			w.Proposal = nil
		default:
			return ErrInvalid
		}
		w.Version++
		w.Events = append(w.Events, TaskEvent{Action: cmd.Action, From: before, To: w.Stage, Step: w.CurrentStep, At: time.Now().UTC()})
		if len(w.Events) > 200 {
			w.Events = w.Events[len(w.Events)-200:]
		}
		return nil
	})
}
func truncateTurn(value string, limit int) string {
	chars := []rune(value)
	if len(chars) > limit {
		return string(chars[:limit]) + "\n[Сокращено; полный текст в истории диалога]"
	}
	return value
}

// RecordTaskTurn retains a recovery checkpoint even if extraction fails. The
// proposal is optional and remains separate from the confirmed checkpoint.
func (s *Store) RecordTaskTurn(owner, taskID string, version int, user, answer string, proposal *TaskProposal) (State, error) {
	return s.update(owner, func(state *State) error {
		w := &state.Task.Workflow
		if state.Task.ID != taskID || w.Version != version || w.Paused || w.Stage == "done" {
			return ErrConflict
		}
		if proposal != nil {
			if err := ValidateProgress(w.Stage, proposal.Progress); err != nil {
				return err
			}
			if strings.TrimSpace(proposal.Reason) == "" || utf8.RuneCountInString(proposal.Reason) > 1000 {
				return ErrInvalid
			}
		}
		w.LastTurn = &TaskTurn{User: truncateTurn(user, 6000), Assistant: truncateTurn(answer, 12000)}
		w.Proposal = proposal
		w.Version++
		return nil
	})
}
