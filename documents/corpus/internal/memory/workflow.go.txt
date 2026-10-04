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
	Plan           string `json:"plan"`
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
	Action   string    `json:"action"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	Step     string    `json:"step"`
	At       time.Time `json:"at"`
	Rejected bool      `json:"rejected,omitempty"`
	Reason   string    `json:"reason,omitempty"`
}
type TaskTurn struct {
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}
type Workflow struct {
	Progress
	PlanVersion            int           `json:"planVersion"`
	ApprovedPlanVersion    int           `json:"approvedPlanVersion"`
	ResultVersion          int           `json:"resultVersion"`
	ValidatedResultVersion int           `json:"validatedResultVersion"`
	ValidationStatus       string        `json:"validationStatus"`
	ValidationBasis        string        `json:"validationBasis"`
	Paused                 bool          `json:"paused"`
	Version                int           `json:"version"`
	Proposal               *TaskProposal `json:"proposal,omitempty"`
	LastTurn               *TaskTurn     `json:"lastTurn,omitempty"`
	Events                 []TaskEvent   `json:"events"`
}
type WorkflowCommand struct {
	TaskID           string   `json:"taskId"`
	ProfileID        string   `json:"profileId"`
	Version          int      `json:"version"`
	Action           string   `json:"action"`
	Progress         Progress `json:"progress"`
	ValidationStatus string   `json:"validationStatus,omitempty"`
	ValidationBasis  string   `json:"validationBasis,omitempty"`
}

func initialWorkflow(name string) Workflow {
	return Workflow{Progress: Progress{Stage: "planning", Goal: name, CurrentStep: "Уточнить цель и составить план", ExpectedActor: "user", ExpectedAction: "Опишите желаемый результат и ограничения"}, Version: 1, ValidationStatus: "pending", Events: []TaskEvent{}}
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
		from == "validation" && (to == "execution" || to == "done" || to == "planning") || from == "execution" && to == "planning"
}
func ValidateProgress(from string, p Progress) error {
	if !AllowedTransition(from, p.Stage) {
		return fmt.Errorf("%w: недопустимый переход этапа", ErrInvalid)
	}
	for _, f := range []struct {
		value string
		max   int
	}{{p.Plan, 6000}, {p.Goal, 2000}, {p.CurrentStep, 500}, {p.ExpectedAction, 2000}, {p.Completed, 6000}, {p.Result, 6000}, {p.OpenQuestions, 2000}, {p.Validation, 4000}} {
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

// PreviewWorkflow is the single authority for all user-confirmed transitions.
// Metadata is server-owned and is never read from a model proposal.
func PreviewWorkflow(w Workflow, cmd WorkflowCommand) (Workflow, error) {
	next := w
	invalid := func(reason string) (Workflow, error) { return w, fmt.Errorf("%w: %s", ErrInvalid, reason) }
	if w.Stage == "done" {
		return w, ErrConflict
	}
	switch cmd.Action {
	case "pause":
		if w.Paused {
			return w, ErrConflict
		}
		next.Paused = true
		return next, nil
	case "resume":
		if !w.Paused {
			return w, ErrConflict
		}
		next.Paused = false
		return next, nil
	case "reject":
		if w.Proposal == nil {
			return w, ErrConflict
		}
		next.Proposal = nil
		return next, nil
	}
	if w.Paused {
		return w, ErrConflict
	}
	p := cmd.Progress
	if cmd.Action == "accept" {
		if w.Proposal == nil {
			return w, ErrConflict
		}
		p = w.Proposal.Progress
	}
	target := w.Stage
	switch cmd.Action {
	case "save", "accept":
		if p.Stage != w.Stage {
			return invalid("этап меняется только явным действием: утвердить план, отправить на проверку, вернуть на доработку или завершить")
		}
	case "approve_plan":
		if w.Stage != "planning" {
			return invalid("утвердить план можно только на этапе planning")
		}
		target = "execution"
		if strings.TrimSpace(p.Plan) == "" {
			return invalid("сначала сохраните и утвердите непустой план")
		}
	case "submit_result":
		if w.Stage != "execution" {
			return invalid("отправить результат на проверку можно только из execution")
		}
		target = "validation"
	case "request_changes":
		if w.Stage != "validation" {
			return invalid("вернуть на доработку можно только из validation")
		}
		target = "execution"
		if strings.TrimSpace(p.Validation) == "" {
			return invalid("укажите замечания для доработки")
		}
	case "replan":
		if w.Stage != "execution" && w.Stage != "validation" {
			return invalid("вернуться к плану можно из execution или validation")
		}
		target = "planning"
	case "record_validation":
		if w.Stage != "validation" {
			return invalid("проверка доступна только на этапе validation")
		}
		if cmd.ValidationStatus != "passed" && cmd.ValidationStatus != "failed" {
			return invalid("выберите итог проверки: passed или failed")
		}
		if cmd.ValidationBasis != "user_report" && cmd.ValidationBasis != "conceptual_review" {
			return invalid("укажите основание: проверка пользователя или анализ результата")
		}
		if strings.TrimSpace(p.Validation) == "" {
			return invalid("опишите, что проверено и с каким результатом")
		}
	case "complete":
		if w.Stage != "validation" {
			return invalid("завершение возможно только после validation")
		}
		target = "done"
		if w.ValidationStatus != "passed" || w.ResultVersion == 0 || w.ValidatedResultVersion != w.ResultVersion || strings.TrimSpace(w.Validation) == "" {
			return invalid("нужна успешная проверка текущей версии результата")
		}
	default:
		return invalid("неизвестное действие")
	}
	if p.Stage != target {
		return invalid("целевой этап не соответствует выбранному действию")
	}
	if err := ValidateProgress(w.Stage, p); err != nil {
		return w, err
	}
	planChanged := p.Plan != w.Plan || p.Goal != w.Goal
	if w.Stage != "planning" && cmd.Action != "replan" && planChanged {
		return invalid("для изменения плана или цели сначала вернитесь к планированию")
	}
	if cmd.Action != "replan" && w.Stage != "planning" && (w.PlanVersion == 0 || w.ApprovedPlanVersion != w.PlanVersion) {
		return invalid("нет утверждённого плана; вернитесь к планированию и утвердите его")
	}
	if w.Stage == "validation" && cmd.Action != "request_changes" && cmd.Action != "replan" && p.Result != w.Result {
		return invalid("для изменения результата вернитесь на доработку; прежняя проверка будет сброшена")
	}
	if cmd.Action == "complete" && p.Validation != w.Validation {
		return invalid("сначала сохраните новую проверку отдельным действием")
	}
	if planChanged {
		next.PlanVersion++
		next.ApprovedPlanVersion = 0
	}
	if p.Result != w.Result {
		next.ResultVersion++
	}
	reset := func() { next.ValidationStatus = "pending"; next.ValidatedResultVersion = 0; next.ValidationBasis = "" }
	if planChanged || p.Result != w.Result || p.Validation != w.Validation {
		reset()
	}
	switch cmd.Action {
	case "approve_plan":
		if next.PlanVersion == 0 {
			next.PlanVersion = 1
		}
		next.ApprovedPlanVersion = next.PlanVersion
		reset()
		p.Validation = ""
	case "submit_result":
		if next.ResultVersion == 0 {
			next.ResultVersion = 1
		}
		reset()
		p.Validation = ""
	case "request_changes", "replan":
		reset()
		if cmd.Action == "replan" {
			next.ApprovedPlanVersion = 0
		}
	case "record_validation":
		next.ValidationStatus = cmd.ValidationStatus
		next.ValidationBasis = cmd.ValidationBasis
		next.ValidatedResultVersion = next.ResultVersion
	}
	next.Progress = p
	next.Proposal = nil
	return next, nil
}

func appendWorkflowEvent(w *Workflow, cmd WorkflowCommand, from string, rejected error) {
	event := TaskEvent{Action: cmd.Action, From: from, To: w.Stage, Step: w.CurrentStep, At: time.Now().UTC()}
	if rejected != nil {
		event.Rejected = true
		event.Reason = rejected.Error()
		event.To = cmd.Progress.Stage
		if event.To == "" {
			event.To = from
		}
	}
	w.Events = append(w.Events, event)
	if len(w.Events) > 200 {
		w.Events = w.Events[len(w.Events)-200:]
	}
}
func (s *Store) UpdateWorkflow(owner string, cmd WorkflowCommand) (State, error) {
	var denied error
	state, err := s.update(owner, func(state *State) error {
		w := &state.Task.Workflow
		if state.Task.ID != cmd.TaskID || w.Version != cmd.Version {
			return ErrConflict
		}
		before := w.Stage
		next, e := PreviewWorkflow(*w, cmd)
		if e != nil {
			denied = e
			appendWorkflowEvent(w, cmd, before, e)
			return nil
		}
		*w = next
		w.Version++
		appendWorkflowEvent(w, cmd, before, nil)
		return nil
	})
	if err != nil {
		return state, err
	}
	return state, denied
}

// RecordWorkflowRejection records a semantic rejection without changing progress.
func (s *Store) RecordWorkflowRejection(owner string, cmd WorkflowCommand, reason error) error {
	_, err := s.update(owner, func(state *State) error {
		if state.Task.ID != cmd.TaskID || state.Task.Workflow.Version != cmd.Version {
			return ErrConflict
		}
		appendWorkflowEvent(&state.Task.Workflow, cmd, state.Task.Workflow.Stage, reason)
		return nil
	})
	return err
}
func truncateTurn(value string, limit int) string {
	chars := []rune(value)
	if len(chars) > limit {
		return string(chars[:limit]) + "\n[Сокращено; полный текст в истории диалога]"
	}
	return value
}

// PreserveApprovedArtifacts keeps model paraphrases from replacing immutable
// plan/goal or the artifact under review. Replanning is an explicit exception.
func PreserveApprovedArtifacts(w Workflow, p Progress) Progress {
	if w.Stage != "planning" && p.Stage != "planning" {
		p.Plan = w.Plan
		p.Goal = w.Goal
	}
	if w.Stage == "validation" {
		p.Result = w.Result
	}
	return p
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
			copy := *proposal
			copy.Progress = PreserveApprovedArtifacts(*w, copy.Progress)
			proposal = &copy
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
