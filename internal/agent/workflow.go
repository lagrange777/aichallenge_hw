package agent

import (
	"context"
	"errors"
	"fmt"

	"codex-chat-cli/internal/memory"
	"codex-chat-cli/internal/profile"
)

var ErrTaskPaused = errors.New("Задача приостановлена. Нажмите «Продолжить» во вкладке «Задачи».")
var ErrTaskDone = errors.New("Задача завершена. Для новой работы создайте другую задачу.")

func (a *Agent) UpdateWorkflow(cmd memory.WorkflowCommand) (MemoryView, error) {
	return a.UpdateWorkflowContext(context.Background(), cmd)
}
func (a *Agent) UpdateWorkflowContext(ctx context.Context, cmd memory.WorkflowCommand) (MemoryView, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.memoryStore == nil {
		return MemoryView{}, fmt.Errorf("memory is disabled")
	}
	if a.profileStore != nil {
		state, err := a.profileStore.Get(a.browserID)
		if err != nil {
			return MemoryView{}, err
		}
		if cmd.ProfileID != state.ActiveID || state.ActiveID != a.conversationID {
			return MemoryView{}, profile.ErrConflict
		}
	}
	if cmd.TaskID != a.taskID {
		return MemoryView{}, memory.ErrConflict
	}
	state, err := a.memoryStore.Get(a.conversationID)
	if err != nil {
		return MemoryView{}, err
	}
	if state.Task.ID != cmd.TaskID || state.Task.Workflow.Version != cmd.Version {
		return MemoryView{}, memory.ErrConflict
	}
	if cmd.Action == "save" || cmd.Action == "accept" {
		if state.Task.Workflow.Paused || state.Task.Workflow.Stage == "done" {
			return MemoryView{}, memory.ErrConflict
		}
		p := cmd.Progress
		if cmd.Action == "accept" {
			if state.Task.Workflow.Proposal == nil {
				return MemoryView{}, memory.ErrConflict
			}
			p = state.Task.Workflow.Proposal.Progress
		}
		if err = memory.ValidateProgress(state.Task.Workflow.Stage, p); err != nil {
			return MemoryView{}, err
		}
		model := a.activeModel
		if model == "" {
			model = a.defaultModel
		}
		if _, err = a.validateWorkflowInvariants(ctx, model, state, p); err != nil {
			return MemoryView{}, err
		}
	}
	state, err = a.memoryStore.UpdateWorkflow(a.conversationID, cmd)
	return a.memoryViewLocked(state), err
}
