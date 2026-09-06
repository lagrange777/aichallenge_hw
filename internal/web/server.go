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

	"codex-chat-cli/internal/chat"
	"codex-chat-cli/internal/models"
)

const (
	maxRequestBytes = 1 << 20
	maxOptionLength = 4000
	sessionCookie   = "codex_chat_session"
	sessionTTL      = 12 * time.Hour
	maxSessions     = 256
)

//go:embed static/index.html static/app.css static/app.js static/markdown.js static/favicon.svg
var staticFiles embed.FS

type sessionEntry struct {
	session  *chat.Session
	lastUsed time.Time
}

type server struct {
	responder chat.Responder
	model     string
	models    []modelOption
	allowed   map[string]bool

	mu       sync.Mutex
	sessions map[string]*sessionEntry
}

type chatRequest struct {
	Message             string   `json:"message"`
	Model               string   `json:"model,omitempty"`
	ResponseFormat      string   `json:"responseFormat,omitempty"`
	LengthLimit         string   `json:"lengthLimit,omitempty"`
	CompletionCondition string   `json:"completionCondition,omitempty"`
	Temperature         *float64 `json:"temperature,omitempty"`
}

type apiResponse struct {
	Answer  string           `json:"answer,omitempty"`
	Error   string           `json:"error,omitempty"`
	Model   string           `json:"model,omitempty"`
	Models  []modelOption    `json:"models,omitempty"`
	Metrics *responseMetrics `json:"metrics,omitempty"`
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
}

// NewHandler returns the complete local web application handler.
func NewHandler(responder chat.Responder, model string) http.Handler {
	model = strings.TrimSpace(model)
	definitions := models.Available(model)
	options := make([]modelOption, 0, len(definitions))
	allowed := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		options = append(options, modelOption{ID: definition.ID, Label: definition.Label})
		allowed[definition.ID] = true
	}
	app := &server{
		responder: responder,
		model:     model,
		models:    options,
		allowed:   allowed,
		sessions:  make(map[string]*sessionEntry),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleIndex)
	mux.HandleFunc("/app.css", app.handleCSS)
	mux.HandleFunc("/app.js", app.handleJS)
	mux.HandleFunc("/markdown.js", app.handleMarkdownJS)
	mux.HandleFunc("/favicon.svg", app.handleFavicon)
	mux.HandleFunc("/api/chat", app.handleChat)
	mux.HandleFunc("/api/reset", app.handleReset)
	mux.HandleFunc("/api/status", app.handleStatus)
	mux.HandleFunc("/healthz", app.handleHealth)
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

	session, err := s.sessionFor(w, r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "Не удалось создать сессию"})
		return
	}
	started := time.Now()
	result, err := session.Ask(r.Context(), request.Message, chat.ResponseOptions{
		Model:               request.Model,
		Format:              request.ResponseFormat,
		LengthLimit:         request.LengthLimit,
		CompletionCondition: request.CompletionCondition,
		Temperature:         request.Temperature,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiResponse{Error: err.Error()})
		return
	}
	durationMS := time.Since(started).Milliseconds()
	if durationMS < 1 {
		durationMS = 1
	}
	writeJSON(w, http.StatusOK, apiResponse{
		Answer: result.Output,
		Model:  result.Model,
		Metrics: &responseMetrics{
			DurationMS:        durationMS,
			InputTokens:       result.Usage.InputTokens,
			CachedInputTokens: result.Usage.CachedInputTokens,
			CacheWriteTokens:  result.Usage.CacheWriteTokens,
			OutputTokens:      result.Usage.OutputTokens,
			ReasoningTokens:   result.Usage.ReasoningTokens,
			TotalTokens:       result.Usage.TotalTokens,
			CostUSD:           result.CostUSD,
		},
	})
}

func (s *server) handleReset(w http.ResponseWriter, r *http.Request) {
	if !allowAPIRequest(w, r, http.MethodPost) {
		return
	}
	session, err := s.sessionFor(w, r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: "Не удалось создать сессию"})
		return
	}
	session.Reset()
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
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

func (s *server) sessionFor(w http.ResponseWriter, r *http.Request) (*chat.Session, error) {
	now := time.Now()
	if cookie, err := r.Cookie(sessionCookie); err == nil && validSessionID(cookie.Value) {
		s.mu.Lock()
		entry := s.sessions[cookie.Value]
		if entry != nil && now.Sub(entry.lastUsed) <= sessionTTL {
			entry.lastUsed = now
			s.mu.Unlock()
			return entry.session, nil
		}
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
	}

	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	entry := &sessionEntry{session: chat.NewSession(s.responder), lastUsed: now}

	s.mu.Lock()
	s.pruneSessions(now)
	s.sessions[id] = entry
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	return entry.session, nil
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
