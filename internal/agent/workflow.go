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
	next, previewErr := memory.PreviewWorkflow(state.Task.Workflow, cmd)
	if previewErr != nil {
		updated, err := a.memoryStore.UpdateWorkflow(a.conversationID, cmd)
		return a.memoryViewLocked(updated), err
	}
	if cmd.Action != "pause" && cmd.Action != "resume" && cmd.Action != "reject" {
		model := a.activeModel
		if model == "" {
			model = a.defaultModel
		}
		if _, err = a.validateWorkflowInvariants(ctx, model, state, next.Progress); err != nil {
			if auditErr := a.memoryStore.RecordWorkflowRejection(a.conversationID, cmd, err); auditErr != nil {
				return MemoryView{}, auditErr
			}
			updated, loadErr := a.memoryStore.Get(a.conversationID)
			if loadErr != nil {
				return MemoryView{}, loadErr
			}
			return a.memoryViewLocked(updated), err
		}
	}

	state, err = a.memoryStore.UpdateWorkflow(a.conversationID, cmd)
	return a.memoryViewLocked(state), err
}
