package agent

import (
	"context"
	"fmt"

	"codex-chat-cli/internal/profile"
)

func WithProfiles(store *profile.Store) Option { return func(a *Agent) { a.profileStore = store } }

func (a *Agent) completeInternal(ctx context.Context, request CompletionRequest) (CompletionResponse, error) {
	request.Profile, request.Internal = a.activeProfile, true
	return a.llm.Complete(ctx, request)
}

type ProfileView struct {
	profile.State
	Presets  []profile.Profile `json:"presets"`
	Memory   *MemoryView       `json:"memory,omitempty"`
	Messages []Message         `json:"messages"`
	Context  StrategySnapshot  `json:"context"`
}

func (a *Agent) profileViewLocked(state profile.State) (ProfileView, error) {
	view := ProfileView{State: state, Presets: profile.Presets(), Messages: cloneMessages(a.messages), Context: a.snapshotLocked()}
	if a.memoryStore != nil {
		layers, err := a.memoryStore.Get(a.conversationID)
		if err != nil {
			return ProfileView{}, err
		}
		memory := a.memoryViewLocked(layers)
		view.Memory = &memory
	}
	return view, nil
}

func (a *Agent) Profiles() (ProfileView, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.profileStore == nil {
		return ProfileView{}, fmt.Errorf("profiles are disabled")
	}
	state, err := a.profileStore.Get(a.browserID)
	if err != nil {
		return ProfileView{}, err
	}
	return a.profileViewLocked(state)
}

func (a *Agent) SaveProfile(expectedID string, p profile.Profile, create bool) (ProfileView, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.profileStore == nil {
		return ProfileView{}, fmt.Errorf("profiles are disabled")
	}
	if expectedID != a.conversationID {
		return ProfileView{}, profile.ErrConflict
	}
	state, err := a.profileStore.Save(a.browserID, expectedID, p, create)
	if err != nil {
		return ProfileView{}, err
	}
	active := state.Active()
	a.activeProfile = &active
	return a.profileViewLocked(state)
}

func (a *Agent) SwitchProfile(expectedID, targetID string) (ProfileView, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.profileStore == nil {
		return ProfileView{}, fmt.Errorf("profiles are disabled")
	}
	state, err := a.profileStore.Get(a.browserID)
	if err != nil {
		return ProfileView{}, err
	}
	if expectedID != a.conversationID || state.ActiveID != expectedID {
		return ProfileView{}, profile.ErrConflict
	}
	if targetID == expectedID {
		return a.profileViewLocked(state)
	}
	found := false
	for _, p := range state.Profiles {
		if p.ID == targetID {
			found = true
			break
		}
	}
	if !found {
		return ProfileView{}, profile.ErrConflict
	}
	// Prepare the target first. Failed I/O must not change the active profile.
	candidate, err := NewPersistent(a.llm, a.defaultModel, targetID, a.history, WithMemory(a.memoryStore), WithContextStrategy(a.defaultStrategy))
	if err != nil {
		return ProfileView{}, err
	}
	if err = a.saveCurrentTaskLocked(); err != nil {
		return ProfileView{}, err
	}
	view, err := candidate.profileViewLocked(state)
	if err != nil {
		return ProfileView{}, err
	}
	state, err = a.profileStore.Switch(a.browserID, expectedID, targetID)
	if err != nil {
		return ProfileView{}, err
	}
	a.conversationID, a.taskID = candidate.conversationID, candidate.taskID
	a.previousResponseID, a.activeModel, a.messages = candidate.previousResponseID, candidate.activeModel, candidate.messages
	a.summary, a.compression, a.strategy = candidate.summary, candidate.compression, candidate.strategy
	a.facts, a.memory, a.branches = candidate.facts, candidate.memory, candidate.branches
	a.activeBranchID, a.checkpoint = candidate.activeBranchID, candidate.checkpoint
	p := state.Active()
	a.activeProfile = &p
	view.State = state
	return view, nil
}
