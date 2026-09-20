// Package agent encapsulates the complete request/response flow of the coding
// assistant independently from HTTP delivery and the OpenAI transport.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"codex-chat-cli/internal/memory"
	"codex-chat-cli/internal/models"
	"codex-chat-cli/internal/profile"
)

// LLM is the transport boundary used by Agent to call a language model.
type LLM interface {
	Complete(ctx context.Context, request CompletionRequest) (CompletionResponse, error)
}

// TokenCounter returns exact input-token counts before an LLM request.
type TokenCounter interface {
	CountTokens(ctx context.Context, request CompletionRequest) (TokenCounts, error)
}

// Request is a user request accepted by the agent.
type Request struct {
	ProfileID           string
	TaskID              string
	TaskVersion         *int
	Message             string
	Model               string
	Format              string
	LengthLimit         string
	CompletionCondition string
	Temperature         *float64
	CompressionEnabled  *bool
	ContextStrategy     string
	ContextKeepLast     *int
}

// CompletionRequest is a normalized request sent from the agent to an LLM.
type CompletionRequest struct {
	Profile             *profile.Profile
	Internal            bool
	Input               string
	History             []ContextMessage
	Model               string
	PreviousResponseID  string
	Instructions        string
	Format              string
	LengthLimit         string
	CompletionCondition string
	Temperature         *float64
}

// ContextMessage is one prior conversational message replayed when an API
// response chain cannot be continued, for example after switching models.
type ContextMessage struct {
	Role    string
	Content string
}

// CompletionResponse is the transport-neutral result returned by an LLM.
type CompletionResponse struct {
	ResponseID string
	Output     string
	Model      string
	Usage      Usage
}

// Usage contains token counts reported by the LLM API.
type Usage struct {
	InputTokens       int
	CachedInputTokens int
	CacheWriteTokens  int
	OutputTokens      int
	ReasoningTokens   int
	TotalTokens       int
}

// Response is the final result prepared by the agent for the interface layer.
type Response struct {
	Text         string
	Model        string
	Usage        Usage
	Duration     time.Duration
	CostUSD      *float64
	TokenMetrics TokenMetrics
}

// MessageMetrics contains metadata saved together with an assistant message.
type MessageMetrics struct {
	DurationMS        int64    `json:"durationMs"`
	InputTokens       int      `json:"inputTokens"`
	CachedInputTokens int      `json:"cachedInputTokens"`
	CacheWriteTokens  int      `json:"cacheWriteTokens"`
	OutputTokens      int      `json:"outputTokens"`
	ReasoningTokens   int      `json:"reasoningTokens"`
	TotalTokens       int      `json:"totalTokens"`
	CostUSD           *float64 `json:"costUsd"`
	TokenMetrics
}

// Message is one durable item in a conversation transcript.
type Message struct {
	Profile *profile.Profile `json:"profile,omitempty"`
	ID      string           `json:"id"`
	Role    string           `json:"role"`
	Text    string           `json:"text"`
	Time    int64            `json:"time"`
	Model   string           `json:"model,omitempty"`
	Metrics *MessageMetrics  `json:"metrics,omitempty"`
}

// ConversationState is the complete state needed to restore an Agent.
type ConversationState struct {
	MemoryTaskID       string                 `json:"memoryTaskId,omitempty"`
	PreviousResponseID string                 `json:"previousResponseId,omitempty"`
	ActiveModel        string                 `json:"activeModel,omitempty"`
	Messages           []Message              `json:"messages"`
	Summary            *ConversationSummary   `json:"summary,omitempty"`
	Compression        *CompressionConfig     `json:"compression,omitempty"`
	Strategy           *StrategyConfig        `json:"strategy,omitempty"`
	Facts              map[string]string      `json:"facts,omitempty"`
	Memory             MemoryUsage            `json:"memory,omitempty"`
	Branches           map[string]BranchState `json:"branches,omitempty"`
	ActiveBranchID     string                 `json:"activeBranchId,omitempty"`
	Checkpoint         *Checkpoint            `json:"checkpoint,omitempty"`
	UpdatedAt          time.Time              `json:"updatedAt"`
}

// ErrHistoryNotFound indicates that a conversation has not been saved yet.
var ErrHistoryNotFound = errors.New("conversation history not found")

// History is the persistence boundary owned by Agent.
type History interface {
	Load(conversationID string) (ConversationState, error)
	Save(conversationID string, state ConversationState) error
	Delete(conversationID string) error
}

// Agent owns one conversation and encapsulates its LLM request/response flow.
type Agent struct {
	profileStore  *profile.Store
	browserID     string
	activeProfile *profile.Profile
	memoryStore   *memory.Store
	llm           LLM
	tokenCounter  TokenCounter
	defaultModel  string

	mu                 sync.Mutex
	previousResponseID string
	activeModel        string
	messages           []Message
	summary            ConversationSummary
	compression        CompressionConfig
	strategy           StrategyConfig
	defaultStrategy    StrategyConfig
	facts              map[string]string
	memory             MemoryUsage
	branches           map[string]BranchState
	activeBranchID     string
	checkpoint         *Checkpoint
	history            History
	conversationID     string
	taskID             string
}

// New creates an independent agent conversation.
func New(llm LLM, defaultModel string, options ...Option) *Agent {
	defaultStrategy := normalizeStrategyConfig(StrategyConfig{Type: StrategySlidingWindow})
	a := &Agent{
		llm:             llm,
		defaultModel:    strings.TrimSpace(defaultModel),
		compression:     normalizeCompressionConfig(CompressionConfig{}),
		strategy:        defaultStrategy,
		defaultStrategy: defaultStrategy,
		facts:           make(map[string]string),
		branches:        make(map[string]BranchState),
	}
	if counter, ok := llm.(TokenCounter); ok {
		a.tokenCounter = counter
	}
	for _, option := range options {
		if option != nil {
			option(a)
		}
	}
	return a
}

// NewPersistent creates an agent and restores its conversation when it exists.
func NewPersistent(llm LLM, defaultModel, conversationID string, history History, options ...Option) (*Agent, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" && history != nil {
		return nil, errors.New("conversation ID is required for persistent history")
	}

	a := New(llm, defaultModel, options...)
	a.history = history
	a.conversationID = conversationID
	a.browserID = conversationID
	if a.profileStore != nil {
		profiles, err := a.profileStore.Get(a.browserID)
		if err != nil {
			return nil, err
		}
		p := profiles.Active()
		a.activeProfile = &p
		a.conversationID = p.ID
	}
	if a.memoryStore != nil {
		layers, err := a.memoryStore.Get(a.conversationID)
		if err != nil {
			return nil, err
		}
		a.taskID = layers.Task.ID
	}
	state, err := a.loadTaskHistory(a.taskID)
	if err != nil {
		return nil, err
	}
	a.restoreConversationLocked(state)
	return a, nil
}

func (a *Agent) historyKey(taskID string) string {
	if taskID == "" {
		return a.conversationID
	}
	return a.conversationID + "/" + taskID
}

func (a *Agent) loadTaskHistory(taskID string) (ConversationState, error) {
	if a.history == nil {
		return ConversationState{}, nil
	}
	state, err := a.history.Load(a.historyKey(taskID))
	if errors.Is(err, ErrHistoryNotFound) && taskID != "" {
		// Read the pre-task-switching history only when it belongs to this task.
		state, err = a.history.Load(a.conversationID)
		if err == nil {
			oldTask := state.MemoryTaskID
			if oldTask == "" {
				oldTask = a.conversationID
			}
			if oldTask != taskID {
				return ConversationState{}, nil
			}
		}
	}
	if errors.Is(err, ErrHistoryNotFound) {
		return ConversationState{}, nil
	}
	if err != nil {
		return ConversationState{}, fmt.Errorf("load conversation history: %w", err)
	}
	return state, nil
}

func (a *Agent) restoreConversationLocked(state ConversationState) {
	a.clearConversationLocked()
	a.previousResponseID = strings.TrimSpace(state.PreviousResponseID)
	a.activeModel = strings.TrimSpace(state.ActiveModel)
	a.messages = cloneMessages(state.Messages)
	if state.Strategy != nil {
		a.strategy = normalizeStrategyConfig(*state.Strategy)
	} else if state.Compression != nil {
		// Histories created by the previous summary-only version migrate to a
		// plain sliding window so new sessions never depend on a summary.
		a.strategy = normalizeStrategyConfig(StrategyConfig{Type: StrategySlidingWindow, KeepLast: state.Compression.KeepLast})
		a.previousResponseID = ""
	}
	a.facts = cloneFacts(state.Facts)
	a.memory = cloneMemoryUsage(state.Memory)
	a.branches = cloneBranches(state.Branches)
	a.activeBranchID = strings.TrimSpace(state.ActiveBranchID)
	a.checkpoint = cloneCheckpoint(state.Checkpoint)
	if state.Summary != nil {
		a.summary = normalizeSummary(*state.Summary, len(a.messages))
	}
	if state.Compression != nil {
		a.compression = normalizeCompressionConfig(*state.Compression)
	}
	if a.strategy.Type == StrategyBranching && a.activeBranchID != "" {
		if branch, ok := a.branches[a.activeBranchID]; ok {
			a.restoreBranchLocked(branch)
		}
	}
	if !a.compression.Enabled && a.summary.MessageCount > 0 {
		a.previousResponseID = ""
	}
}

// Ask normalizes a user request, calls the LLM and prepares the final response.
// Conversation state advances only after a successful completion.
func (a *Agent) Ask(ctx context.Context, request Request) (Response, error) {
	started := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.profileStore != nil {
		profiles, err := a.profileStore.Get(a.browserID)
		if err != nil {
			return Response{}, err
		}
		if profiles.ActiveID != a.conversationID || (request.ProfileID != "" && request.ProfileID != profiles.ActiveID) {
			return Response{}, profile.ErrConflict
		}
		p := profiles.Active()
		a.activeProfile = &p
	}
	var layers memory.State
	if a.memoryStore != nil {
		var err error
		layers, err = a.memoryStore.Get(a.conversationID)
		if err != nil {
			return Response{}, fmt.Errorf("load memory: %w", err)
		}
		if layers.Task.ID != a.taskID || (request.TaskID != "" && request.TaskID != layers.Task.ID) || (request.TaskVersion != nil && *request.TaskVersion != layers.Task.Workflow.Version) {
			return Response{}, memory.ErrConflict
		}
		if layers.Task.Workflow.Paused {
			return Response{}, ErrTaskPaused
		}
		if layers.Task.Workflow.Stage == "done" {
			return Response{}, ErrTaskDone
		}
	}
	request.Message = strings.TrimSpace(request.Message)
	if request.Message == "" {
		return Response{}, errors.New("message is required")
	}
	request.Model = strings.TrimSpace(request.Model)
	if request.Model == "" {
		request.Model = a.defaultModel
	}
	if request.Model == "" {
		return Response{}, errors.New("model is required")
	}
	request.Format = strings.TrimSpace(request.Format)
	request.LengthLimit = strings.TrimSpace(request.LengthLimit)
	request.CompletionCondition = strings.TrimSpace(request.CompletionCondition)
	if request.CompressionEnabled != nil {
		if err := a.applySessionCompression(request.CompressionEnabled, request.ContextKeepLast); err != nil {
			return Response{}, err
		}
	} else if err := a.applySessionStrategy(request.ContextStrategy, request.ContextKeepLast); err != nil {
		return Response{}, err
	}

	prepared := preparedCompression{Summary: cloneSummary(a.summary)}
	preparedFacts := cloneFacts(a.facts)
	preparedMemory := cloneMemoryUsage(a.memory)
	strategyWarning := ""
	previousResponseID := ""
	var replayHistory []ContextMessage
	var metricSummary *ConversationSummary
	switch a.strategy.Type {
	case StrategyNone:
		replayHistory = contextMessages(a.messages)
	case StrategySummary:
		prepared = a.prepareCompression(ctx, request.Model)
		metricSummary = &prepared.Summary
		previousResponseID = a.previousResponseID
		if len(a.messages) > 0 && (prepared.Applied || previousResponseID == "" || request.Model != a.activeModel) {
			previousResponseID = ""
			replayHistory = a.requestHistory(prepared.Summary)
		}
	case StrategyStickyFacts:
		var delta MemoryUsage
		preparedFacts, delta, strategyWarning = a.updateFacts(ctx, request.Model, request.Message)
		preparedMemory = mergeMemoryUsage(a.memory, delta)
		replayHistory = a.factsHistory(preparedFacts)
	case StrategyBranching:
		previousResponseID = a.previousResponseID
		if len(a.messages) > 0 && (previousResponseID == "" || request.Model != a.activeModel) {
			previousResponseID = ""
			replayHistory = contextMessages(a.messages)
		}
	case StrategySlidingWindow:
		replayHistory = a.windowHistory()
	default:
		return Response{}, fmt.Errorf("unsupported context strategy %q", a.strategy.Type)
	}
	completionRequest := CompletionRequest{
		Profile:             a.activeProfile,
		Input:               request.Message,
		History:             replayHistory,
		Model:               request.Model,
		PreviousResponseID:  previousResponseID,
		Format:              request.Format,
		LengthLimit:         request.LengthLimit,
		CompletionCondition: request.CompletionCondition,
		Temperature:         request.Temperature,
	}
	if a.activeProfile != nil && completionRequest.PreviousResponseID != "" {
		// Profile edits must not keep older instructions in a hidden response chain.
		completionRequest.History = contextMessages(a.messages)
		completionRequest.PreviousResponseID = ""
	}
	if a.memoryStore != nil {
		// Replay explicitly so edits/deletions cannot survive in a hidden API chain.
		if completionRequest.PreviousResponseID != "" {
			completionRequest.History = contextMessages(a.messages)
			completionRequest.PreviousResponseID = ""
		}
		completionRequest.History = append([]ContextMessage{memoryContext(layers)}, completionRequest.History...)
	}
	response := Response{
		Model:        request.Model,
		TokenMetrics: a.measureTokens(ctx, completionRequest, metricSummary),
	}
	response.TokenMetrics.ContextStrategy = string(a.strategy.Type)
	response.TokenMetrics.WindowMessages = countConversationMessages(replayHistory)
	if a.strategy.Type == StrategyBranching {
		response.TokenMetrics.WindowMessages = len(a.messages)
	}
	response.TokenMetrics.FactsCount = len(preparedFacts)
	response.TokenMetrics.ActiveBranchID = a.activeBranchID
	response.TokenMetrics.MemoryUpdates = preparedMemory.Updates
	response.TokenMetrics.MemoryInputTokens = preparedMemory.InputTokens
	response.TokenMetrics.MemoryOutputTokens = preparedMemory.OutputTokens
	response.TokenMetrics.MemoryTotalTokens = preparedMemory.TotalTokens
	response.TokenMetrics.CompressionApplied = prepared.Applied
	response.TokenMetrics.CompressionWarning = prepared.Warning
	combinedWarning := strings.TrimSpace(strings.Join([]string{prepared.Warning, strategyWarning}, " "))
	if combinedWarning != "" {
		if response.TokenMetrics.ContextWarning == "" {
			response.TokenMetrics.ContextWarning = combinedWarning
		} else {
			response.TokenMetrics.ContextWarning += " " + combinedWarning
		}
	}
	completion, err := a.llm.Complete(ctx, completionRequest)
	if err != nil {
		response.Duration = time.Since(started)
		return response, err
	}

	if completion.Model == "" {
		completion.Model = request.Model
	}
	if completion.Usage.TotalTokens == 0 {
		completion.Usage.TotalTokens = completion.Usage.InputTokens + completion.Usage.OutputTokens
	}

	response.Text = completion.Output
	response.Model = completion.Model
	response.Usage = completion.Usage
	response.Duration = time.Since(started)
	if cost, ok := models.EstimateCost(request.Model, completion.Usage.InputTokens, completion.Usage.CachedInputTokens, completion.Usage.CacheWriteTokens, completion.Usage.OutputTokens); ok {
		response.CostUSD = &cost
	}
	var proposals []memory.Proposal
	var taskProposal *memory.TaskProposal
	if a.memoryStore != nil {
		var usage Usage
		var cost *float64
		var warning string
		proposals, taskProposal, usage, cost, warning = a.proposeMemories(ctx, request.Model, request.Message, response.Text, layers)
		response.TokenMetrics.ProposalTokens = usage.TotalTokens
		response.Usage.InputTokens += usage.InputTokens
		response.Usage.CachedInputTokens += usage.CachedInputTokens
		response.Usage.CacheWriteTokens += usage.CacheWriteTokens
		response.Usage.OutputTokens += usage.OutputTokens
		response.Usage.ReasoningTokens += usage.ReasoningTokens
		response.Usage.TotalTokens += usage.TotalTokens
		if usage.TotalTokens > 0 {
			response.CostUSD = addKnownCosts(response.CostUSD, cost)
		}
		response.TokenMetrics.ContextWarning = strings.TrimSpace(response.TokenMetrics.ContextWarning + " " + warning)
		response.Duration = time.Since(started)
	}
	response.TokenMetrics = a.completeTokenMetrics(response.TokenMetrics, response.Usage, response.CostUSD)
	if preparedMemory.TotalTokens > a.memory.TotalTokens {
		deltaTotal := preparedMemory.TotalTokens - a.memory.TotalTokens
		deltaInput := preparedMemory.InputTokens - a.memory.InputTokens
		deltaOutput := preparedMemory.OutputTokens - a.memory.OutputTokens
		response.TokenMetrics.CumulativeInputTokens += max(0, deltaInput)
		response.TokenMetrics.CumulativeOutputTokens += max(0, deltaOutput)
		response.TokenMetrics.CumulativeTotalTokens += max(0, deltaTotal)
		response.TokenMetrics.CumulativeCostUSD = addKnownCosts(response.TokenMetrics.CumulativeCostUSD, costDifference(a.memory.CostUSD, preparedMemory.CostUSD))
	}
	// The token snapshot is prepared before the model call. The visible
	// conversation counters, however, describe the state after this successful
	// turn, which adds both the user request and the assistant response.
	response.TokenMetrics.RetainedMessages = max(0, len(a.messages)+2-response.TokenMetrics.SummaryMessages)

	durationMS := response.Duration.Milliseconds()
	if durationMS < 1 {
		durationMS = 1
	}
	nextMessages := append(cloneMessages(a.messages),
		Message{Role: "user", Text: request.Message, Time: started.UnixMilli()},
		Message{
			Role:    "assistant",
			Profile: a.activeProfile,
			Text:    response.Text,
			Time:    time.Now().UnixMilli(),
			Model:   response.Model,
			Metrics: &MessageMetrics{
				DurationMS:        durationMS,
				InputTokens:       response.Usage.InputTokens,
				CachedInputTokens: response.Usage.CachedInputTokens,
				CacheWriteTokens:  response.Usage.CacheWriteTokens,
				OutputTokens:      response.Usage.OutputTokens,
				ReasoningTokens:   response.Usage.ReasoningTokens,
				TotalTokens:       response.Usage.TotalTokens,
				CostUSD:           response.CostUSD,
				TokenMetrics:      response.TokenMetrics,
			},
		},
	)
	nextBranches := cloneBranches(a.branches)
	if a.strategy.Type == StrategyBranching && a.activeBranchID != "" {
		branch := nextBranches[a.activeBranchID]
		branch.PreviousResponseID = completion.ResponseID
		branch.ActiveModel = request.Model
		branch.Messages = cloneMessages(nextMessages)
		branch.UpdatedAt = time.Now().UTC()
		nextBranches[a.activeBranchID] = branch
	}
	if a.history != nil {
		state := ConversationState{
			MemoryTaskID:       layers.Task.ID,
			PreviousResponseID: completion.ResponseID,
			ActiveModel:        request.Model,
			Messages:           nextMessages,
			Summary:            summaryPointer(prepared.Summary),
			Compression:        compressionPointer(a.compression),
			Strategy:           strategyPointer(a.strategy),
			Facts:              cloneFacts(preparedFacts),
			Memory:             cloneMemoryUsage(preparedMemory),
			Branches:           nextBranches,
			ActiveBranchID:     a.activeBranchID,
			Checkpoint:         cloneCheckpoint(a.checkpoint),
			UpdatedAt:          time.Now().UTC(),
		}
		if err := a.history.Save(a.historyKey(a.taskID), state); err != nil {
			return Response{}, fmt.Errorf("save conversation history: %w", err)
		}
	}
	a.previousResponseID = completion.ResponseID
	a.activeModel = request.Model
	a.messages = nextMessages
	a.summary = prepared.Summary
	a.facts = preparedFacts
	a.memory = preparedMemory
	a.branches = nextBranches
	if a.memoryStore != nil && len(proposals) > 0 {
		source := memorySource(request.Message, response.Text)
		if _, err := a.memoryStore.Propose(a.conversationID, layers.Task.ID, source, proposals); err != nil {
			response.TokenMetrics.ContextWarning = strings.TrimSpace(response.TokenMetrics.ContextWarning + " Не удалось сохранить предложения памяти; ответ сохранён.")
		}
	}
	if a.memoryStore != nil {
		if _, err := a.memoryStore.RecordTaskTurn(a.conversationID, layers.Task.ID, layers.Task.Workflow.Version, request.Message, response.Text, taskProposal); err != nil {
			response.TokenMetrics.ContextWarning = strings.TrimSpace(response.TokenMetrics.ContextWarning + " Не удалось сохранить точку продолжения задачи; ответ сохранён в диалоге.")
		}
	}
	return response, nil
}

func countConversationMessages(history []ContextMessage) int {
	count := 0
	for _, message := range history {
		if message.Role == "user" || message.Role == "assistant" {
			count++
		}
	}
	return count
}

func contextMessages(messages []Message) []ContextMessage {
	context := make([]ContextMessage, 0, len(messages))
	for _, message := range messages {
		role := strings.TrimSpace(message.Role)
		content := message.Text
		if strings.TrimSpace(content) == "" || (role != "user" && role != "assistant") {
			continue
		}
		context = append(context, ContextMessage{Role: role, Content: content})
	}
	return context
}

// Reset clears the conversation context without replacing the agent.
func (a *Agent) Reset() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.history != nil {
		var err error
		if a.taskID != "" {
			// An empty task record also prevents legacy history from reappearing.
			err = a.history.Save(a.historyKey(a.taskID), ConversationState{MemoryTaskID: a.taskID})
		} else {
			err = a.history.Delete(a.conversationID)
		}
		if err != nil {
			return fmt.Errorf("clear conversation history: %w", err)
		}
	}
	a.clearConversationLocked()
	return nil
}

func (a *Agent) clearConversationLocked() {
	a.previousResponseID = ""
	a.activeModel = ""
	a.messages = nil
	a.summary = ConversationSummary{}
	a.strategy = a.defaultStrategy
	a.facts = make(map[string]string)
	a.memory = MemoryUsage{}
	a.branches = make(map[string]BranchState)
	a.activeBranchID = ""
	a.checkpoint = nil
}

// Messages returns a snapshot of the conversation transcript.
func (a *Agent) Messages() []Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneMessages(a.messages)
}

func cloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]Message, len(messages))
	for index, message := range messages {
		cloned[index] = message
		cloned[index].ID = messageID(message)
		if message.Profile != nil {
			p := *message.Profile
			cloned[index].Profile = &p
		}
		if message.Metrics != nil {
			metrics := *message.Metrics
			if message.Metrics.CostUSD != nil {
				cost := *message.Metrics.CostUSD
				metrics.CostUSD = &cost
			}
			if message.Metrics.CumulativeCostUSD != nil {
				cost := *message.Metrics.CumulativeCostUSD
				metrics.CumulativeCostUSD = &cost
			}
			cloned[index].Metrics = &metrics
		}
	}
	return cloned
}
