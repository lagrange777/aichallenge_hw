package agent

import (
	"errors"
	"fmt"

	"codex-chat-cli/internal/memory"
	"codex-chat-cli/internal/profile"
)

var ErrTaskPaused = errors.New("Задача приостановлена. Нажмите «Продолжить» во вкладке «Задачи».")
var ErrTaskDone = errors.New("Задача завершена. Для новой работы создайте другую задачу.")

func (a *Agent) UpdateWorkflow(cmd memory.WorkflowCommand) (MemoryView, error) {
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
	state, err := a.memoryStore.UpdateWorkflow(a.conversationID, cmd)
	return a.memoryViewLocked(state), err
}
