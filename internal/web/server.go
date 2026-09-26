package web

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/mcpclient"
	"codex-chat-cli/internal/memory"
	"codex-chat-cli/internal/models"
	"codex-chat-cli/internal/profile"
)

const (
	maxRequestBytes = 1 << 20
	maxOptionLength = 4000
	maxContextKeep  = 1000
	sessionCookie   = "codex_chat_session"
	sessionTTL      = 12 * time.Hour
	cookieTTL       = 365 * 24 * time.Hour
	maxSessions     = 256
)

//go:embed static/index.html static/app.css static/app.js static/memory.js static/task-state.js static/invariants.js static/profiles.js static/mcp.js static/markdown.js static/favicon.svg
var staticFiles embed.FS

type sessionEntry struct {
	agent    *agent.Agent
	lastUsed time.Time
}

type server struct {
	mcpStore     *mcpclient.Store
	llm          agent.LLM
	history      agent.History
	model        string
	models       []modelOption
	allowed      map[string]bool
	agentOptions []agent.Option

	mu       sync.Mutex
	sessions map[string]*sessionEntry
}

type chatRequest struct {
	TaskID              string   `json:"taskId,omitempty"`
	TaskVersion         *int     `json:"taskVersion,omitempty"`
	ProfileID           string   `json:"profileId,omitempty"`
	Message             string   `json:"message"`
	Model               string   `json:"model,omitempty"`
	ResponseFormat      string   `json:"responseFormat,omitempty"`
	LengthLimit         string   `json:"lengthLimit,omitempty"`
	CompletionCondition string   `json:"completionCondition,omitempty"`
	Temperature         *float64 `json:"temperature,omitempty"`
	ContextStrategy     string   `json:"contextStrategy,omitempty"`
	ContextKeepLast     *int     `json:"contextKeepLast,omitempty"`
}

type apiResponse struct {
	MCP      *mcpView                `json:"mcp,omitempty"`
	Profiles *agent.ProfileView      `json:"profiles,omitempty"`
	Memory   *agent.MemoryView       `json:"memory,omitempty"`
	Answer   string                  `json:"answer,omitempty"`
	Error    string                  `json:"error,omitempty"`
	Warning  string                  `json:"warning,omitempty"`
	Model    string                  `json:"model,omitempty"`
	Models   []modelOption           `json:"models,omitempty"`
	Metrics  *responseMetrics        `json:"metrics,omitempty"`
	Messages []agent.Message         `json:"messages,omitempty"`
	Context  *agent.StrategySnapshot `json:"context,omitempty"`
}

type branchRequest struct {
	BranchID string `json:"branchId"`
}

type modelOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type responseMetrics struct {
	DurationMS        int64    `json:"durationMs"`
	InputTokens       int      `json:"inputTokens"`
	CachedInputTokens int      `json:"cachedInputTokens"`
	CacheWriteTokens  int      `json:"cacheWriteTokens"`
	OutputTokens      int      `json:"outputTokens"`
	ReasoningTokens   int      `json:"reasoningTokens"`
	TotalTokens       int      `json:"totalTokens"`
	CostUSD           *float64 `json:"costUsd"`
	agent.TokenMetrics
}

// NewHandler returns the complete local web application handler.
func NewHandler(llm agent.LLM, model string, history agent.History, agentOptions ...agent.Option) http.Handler {
	return NewHandlerWithMCP(llm, model, history, nil, agentOptions...)
}

// NewHandlerWithMCP adds project-wide MCP connection settings to the web app.
func NewHandlerWithMCP(llm agent.LLM, model string, history agent.History, mcpStore *mcpclient.Store, agentOptions ...agent.Option) http.Handler {
	model = strings.TrimSpace(model)
	definitions := models.Available(model)
	options := make([]modelOption, 0, len(definitions))
	allowed := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		options = append(options, modelOption{ID: definition.ID, Label: definition.Label})
		allowed[definition.ID] = true
	}
	app := &server{
		mcpStore:     mcpStore,
		llm:          llm,
		history:      history,
		model:        model,
		models:       options,
		allowed:      allowed,
		agentOptions: agentOptions,
		sessions:     make(map[string]*sessionEntry),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp.js", func(w http.ResponseWriter, r *http.Request) {
		app.serveStatic(w, r, "mcp.js", "text/javascript; charset=utf-8")
	})
	mux.HandleFunc("/api/mcp", app.handleMCP)
	mux.HandleFunc("/api/mcp/save", app.handleMCPMutation)
	mux.HandleFunc("/api/mcp/delete", app.handleMCPMutation)
	mux.HandleFunc("/api/mcp/check", app.handleMCPMutation)
	mux.HandleFunc("/", app.handleIndex)
	mux.HandleFunc("/app.css", app.handleCSS)
	mux.HandleFunc("/app.js", app.handleJS)
	mux.HandleFunc("/task-state.js", func(w http.ResponseWriter, r *http.Request) {
		app.serveStatic(w, r, "task-state.js", "text/javascript; charset=utf-8")
	})
	mux.HandleFunc("/memory.js", func(w http.ResponseWriter, r *http.Request) {
		app.serveStatic(w, r, "memory.js", "text/javascript; charset=utf-8")
	})
	mux.HandleFunc("/markdown.js", app.handleMarkdownJS)
	mux.HandleFunc("/favicon.svg", app.handleFavicon)
	mux.HandleFunc("/api/chat", app.handleChat)
	mux.HandleFunc("/api/history", app.handleHistory)
	mux.HandleFunc("/api/reset", app.handleReset)
	mux.HandleFunc("/api/branches/create", app.handleCreateBranches)
	mux.HandleFunc("/api/branches/switch", app.handleSwitchBranch)
	mux.HandleFunc("/api/status", app.handleStatus)
	mux.HandleFunc("/healthz", app.handleHealth)
	mux.HandleFunc("/api/memory", app.handleMemory)
	mux.HandleFunc("/api/memory/review", app.handleMemoryMutation)
	mux.HandleFunc("/api/memory/from-message", app.handleMemoryMutation)
	mux.HandleFunc("/api/memory/edit", app.handleMemoryMutation)
	mux.HandleFunc("/api/memory/delete", app.handleMemoryMutation)
	mux.HandleFunc("/api/tasks/new", app.handleMemoryMutation)
	mux.HandleFunc("/api/tasks/switch", app.handleMemoryMutation)
	mux.HandleFunc("/api/tasks/state", app.handleWorkflow)
	mux.HandleFunc("/api/tasks/invariants", app.handleInvariants)
	mux.HandleFunc("/invariants.js", func(w http.ResponseWriter, r *http.Request) {
		app.serveStatic(w, r, "invariants.js", "text/javascript; charset=utf-8")
	})
	mux.HandleFunc("/profiles.js", func(w http.ResponseWriter, r *http.Request) {
		app.serveStatic(w, r, "profiles.js", "text/javascript; charset=utf-8")
	})
	mux.HandleFunc("/api/profiles", app.handleProfiles)
	mux.HandleFunc("/api/profiles/new", app.handleProfileMutation)
	mux.HandleFunc("/api/profiles/edit", app.handleProfileMutation)
	mux.HandleFunc("/api/profiles/switch", app.handleProfileMutation)
	return securityHeaders(mux)
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.serveStatic(w, r, "index.html", "text/html; charset=utf-8")
}

func (s *server) handleCSS(w http.ResponseWriter, r *http.Request) {
	s.serveStatic(w, r, "app.css", "text/css; charset=utf-8")
}

func (s *server) handleJS(w http.ResponseWriter, r *http.Request) {
	s.serveStatic(w, r, "app.js", "text/javascript; charset=utf-8")
}

func (s *server) handleMarkdownJS(w http.ResponseWriter, r *http.Request) {
	s.serveStatic(w, r, "markdown.js", "text/javascript; charset=utf-8")
}

func (s *server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	s.serveStatic(w, r, "favicon.svg", "image/svg+xml")
}

func (s *server) serveStatic(w http.ResponseWriter, r *http.Request, name, contentType string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	content, err := staticFiles.ReadFile("static/" + name)
	if err != nil {
		http.Error(w, "asset unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method == http.MethodGet {
		_, _ = w.Write(content)
	}
}

func (s *server) handleChat(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodPost) {
		return
	}
	if !hasJSONContentType(r) {
		writeJSON(w, http.StatusUnsupportedMediaType, apiResponse{Error: "Ожидается Content-Type: application/json"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	var request chatRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, apiResponse{Error: "Некорректное тело запроса"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "В запросе должен быть один JSON-объект"})
		return
	}

	request.Message = strings.TrimSpace(request.Message)
	if request.Message == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "Введите сообщение"})
		return
	}
	request.Model = strings.TrimSpace(request.Model)
	if request.Model == "" {
		request.Model = s.model
	}
	if !s.allowed[request.Model] {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "Выберите модель из списка"})
		return
	}
	request.ResponseFormat = strings.TrimSpace(request.ResponseFormat)
	request.LengthLimit = strings.TrimSpace(request.LengthLimit)
	request.CompletionCondition = strings.TrimSpace(request.CompletionCondition)
	if len(request.ResponseFormat) > maxOptionLength || len(request.LengthLimit) > maxOptionLength || len(request.CompletionCondition) > maxOptionLength {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "Дополнительное поле слишком длинное"})
		return
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 2) {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "Температура должна быть от 0 до 2"})
		return
	}
	if request.ContextKeepLast != nil && (*request.ContextKeepLast < 1 || *request.ContextKeepLast > maxContextKeep) {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "Количество последних сообщений должно быть от 1 до 1000"})
		return
	}
	request.ContextStrategy = strings.TrimSpace(request.ContextStrategy)
	if request.ContextStrategy != "" && request.ContextStrategy != string(agent.StrategyNone) && request.ContextStrategy != string(agent.StrategySlidingWindow) && request.ContextStrategy != string(agent.StrategyStickyFacts) && request.ContextStrategy != string(agent.StrategyBranching) {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "Выберите стратегию управления контекстом из списка"})
		return
	}

	chatAgent, err := s.agentFor(w, r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "Не удалось создать сессию"})
		return
	}
	result, err := chatAgent.Ask(r.Context(), agent.Request{
		ProfileID: request.ProfileID,
		TaskID:    request.TaskID, TaskVersion: request.TaskVersion,
		Message:             request.Message,
		Model:               request.Model,
		Format:              request.ResponseFormat,
		LengthLimit:         request.LengthLimit,
		CompletionCondition: request.CompletionCondition,
		Temperature:         request.Temperature,
		ContextStrategy:     request.ContextStrategy,
		ContextKeepLast:     request.ContextKeepLast,
	})
	if err != nil {
		status := http.StatusBadGateway
		message := err.Error()
		if errors.Is(err, profile.ErrConflict) {
			status, message = 409, "Активный профиль изменился. Обновите страницу перед отправкой."
		}
		if errors.Is(err, memory.ErrConflict) {
			status, message = 409, "Задача изменилась. Обновите страницу перед отправкой."
		}
		if errors.Is(err, agent.ErrTaskPaused) || errors.Is(err, agent.ErrTaskDone) {
			status = 409
		}
		if errors.Is(err, agent.ErrStrategyLocked) {
			status = http.StatusConflict
			message = "Стратегию и N можно изменить только до первого сообщения"
		}
		writeJSON(w, status, apiResponse{
			Error:   message,
			Warning: result.TokenMetrics.ContextWarning,
			Model:   result.Model,
			Metrics: metricsFromResponse(result),
		})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{
		Answer:   result.Text,
		Messages: chatAgent.Messages(),
		Warning:  result.TokenMetrics.ContextWarning,
		Model:    result.Model,
		Metrics:  metricsFromResponse(result),
		Context:  snapshotPointer(chatAgent.Snapshot()),
	})
}

func metricsFromResponse(result agent.Response) *responseMetrics {
	durationMS := result.Duration.Milliseconds()
	if durationMS < 1 {
		durationMS = 1
	}
	return &responseMetrics{
		DurationMS:        durationMS,
		InputTokens:       result.Usage.InputTokens,
		CachedInputTokens: result.Usage.CachedInputTokens,
		CacheWriteTokens:  result.Usage.CacheWriteTokens,
		OutputTokens:      result.Usage.OutputTokens,
		ReasoningTokens:   result.Usage.ReasoningTokens,
		TotalTokens:       result.Usage.TotalTokens,
		CostUSD:           result.CostUSD,
		TokenMetrics:      result.TokenMetrics,
	}
}

func (s *server) handleReset(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodPost) {
		return
	}
	chatAgent, err := s.agentFor(w, r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "Не удалось создать сессию"})
		return
	}
	if err := chatAgent.Reset(); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "Не удалось очистить историю"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Error: "Метод не поддерживается"})
		return
	}
	chatAgent, err := s.agentFor(w, r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "Не удалось загрузить историю"})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{
		Messages: chatAgent.Messages(),
		Context:  snapshotPointer(chatAgent.Snapshot()),
	})
}

func (s *server) handleCreateBranches(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodPost) {
		return
	}
	chatAgent, err := s.agentFor(w, r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "Не удалось загрузить сессию"})
		return
	}
	snapshot, err := chatAgent.CreateBranches()
	if err != nil {
		writeJSON(w, http.StatusConflict, apiResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Messages: chatAgent.Messages(), Context: snapshotPointer(snapshot)})
}

func (s *server) handleSwitchBranch(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodPost) {
		return
	}
	if !hasJSONContentType(r) {
		writeJSON(w, http.StatusUnsupportedMediaType, apiResponse{Error: "Ожидается Content-Type: application/json"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	var request branchRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || strings.TrimSpace(request.BranchID) == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "Выберите ветку"})
		return
	}
	chatAgent, err := s.agentFor(w, r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "Не удалось загрузить сессию"})
		return
	}
	snapshot, err := chatAgent.SwitchBranch(request.BranchID)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, agent.ErrBranchNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, apiResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Messages: chatAgent.Messages(), Context: snapshotPointer(snapshot)})
}

func snapshotPointer(snapshot agent.StrategySnapshot) *agent.StrategySnapshot {
	return &snapshot
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Error: "Метод не поддерживается"})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{Model: s.model, Models: s.models})
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

func allowAPIRequest(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeJSON(w, http.StatusMethodNotAllowed, apiResponse{Error: "Метод не поддерживается"})
		return false
	}
	if r.Header.Get("X-Codex-Chat") != "1" || !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, apiResponse{Error: "Запрос отклонён"})
		return false
	}
	return true
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && strings.EqualFold(parsed.Host, r.Host)
}

func hasJSONContentType(r *http.Request) bool {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && contentType == "application/json"
}

func (s *server) agentFor(w http.ResponseWriter, r *http.Request) (*agent.Agent, error) {
	now := time.Now()
	if cookie, err := r.Cookie(sessionCookie); err == nil && validSessionID(cookie.Value) {
		s.mu.Lock()
		entry := s.sessions[cookie.Value]
		if entry != nil && now.Sub(entry.lastUsed) <= sessionTTL {
			entry.lastUsed = now
			s.mu.Unlock()
			s.setSessionCookie(w, r, cookie.Value)
			return entry.agent, nil
		}
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()

		chatAgent, err := agent.NewPersistent(s.llm, s.model, cookie.Value, s.history, s.agentOptions...)
		if err != nil {
			return nil, err
		}
		entry = &sessionEntry{agent: chatAgent, lastUsed: now}
		s.mu.Lock()
		if current := s.sessions[cookie.Value]; current != nil {
			entry = current
			entry.lastUsed = now
		} else {
			s.pruneSessions(now)
			s.sessions[cookie.Value] = entry
		}
		s.mu.Unlock()
		s.setSessionCookie(w, r, cookie.Value)
		return entry.agent, nil
	}

	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	chatAgent, err := agent.NewPersistent(s.llm, s.model, id, s.history, s.agentOptions...)
	if err != nil {
		return nil, err
	}
	entry := &sessionEntry{agent: chatAgent, lastUsed: now}

	s.mu.Lock()
	s.pruneSessions(now)
	s.sessions[id] = entry
	s.mu.Unlock()

	s.setSessionCookie(w, r, id)
	return entry.agent, nil
}

func (s *server) setSessionCookie(w http.ResponseWriter, r *http.Request, id string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		MaxAge:   int(cookieTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
}

func (s *server) pruneSessions(now time.Time) {
	for id, entry := range s.sessions {
		if now.Sub(entry.lastUsed) > sessionTTL {
			delete(s.sessions, id)
		}
	}
	for len(s.sessions) >= maxSessions {
		var oldestID string
		var oldest time.Time
		for id, entry := range s.sessions {
			if oldestID == "" || entry.lastUsed.Before(oldest) {
				oldestID = id
				oldest = entry.lastUsed
			}
		}
		delete(s.sessions, oldestID)
	}
}

func newSessionID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func validSessionID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func writeJSON(w http.ResponseWriter, status int, response apiResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
