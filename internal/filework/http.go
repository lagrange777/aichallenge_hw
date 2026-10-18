package filework

import (
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

//go:embed static/*
var assets embed.FS

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /work", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, "index.html", "text/html; charset=utf-8") })
	mux.HandleFunc("GET /work/app.js", func(w http.ResponseWriter, r *http.Request) {
		serveAsset(w, "app.js", "text/javascript; charset=utf-8")
	})
	mux.HandleFunc("GET /work/app.css", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, "app.css", "text/css; charset=utf-8") })
	mux.HandleFunc("/api/work/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if o := r.Header.Get("Origin"); o != "" {
			u, e := url.Parse(o)
			if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				reply(w, 403, map[string]string{"error": "Origin forbidden"})
				return
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			reply(w, 403, map[string]string{"error": "Cross-site forbidden"})
			return
		}
		owner := ""
		if c, e := r.Cookie("codex_file_session"); e == nil && len(c.Value) == 48 {
			owner = c.Value
		}
		if owner == "" {
			owner = ID()
			http.SetCookie(w, &http.Cookie{Name: "codex_file_session", Value: owner, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: 365 * 86400})
		}
		if r.Method == "GET" {
			switch r.URL.Path {
			case "/api/work/tasks":
				reply(w, 200, map[string]any{"tasks": s.List(owner)})
			case "/api/work/task":
				t, e := s.Get(owner, r.URL.Query().Get("id"))
				if e != nil {
					reply(w, 404, map[string]string{"error": e.Error()})
					return
				}
				reply(w, 200, t)
			default:
				http.NotFound(w, r)
			}
			return
		}
		if r.Method != "POST" {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method forbidden", 405)
			return
		}
		if r.Header.Get("X-File-Work") != "1" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			reply(w, 415, map[string]string{"error": "JSON and X-File-Work required"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		var in struct {
			Goal string `json:"goal"`
			ID   string `json:"id"`
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if dec.Decode(&in) != nil || dec.Decode(&struct{}{}) != io.EOF {
			reply(w, 400, map[string]string{"error": "Invalid JSON"})
			return
		}
		switch r.URL.Path {
		case "/api/work/start":
			t, e := s.Start(owner, in.Goal)
			if e != nil {
				reply(w, 409, map[string]string{"error": e.Error()})
				return
			}
			reply(w, 202, t)
		case "/api/work/apply":
			t, e := s.Apply(owner, in.ID)
			if e != nil {
				reply(w, 409, map[string]string{"error": e.Error()})
				return
			}
			reply(w, 200, t)
		default:
			http.NotFound(w, r)
		}
	})
	return http.TimeoutHandler(mux, 30*time.Second, "request timeout")
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func serveAsset(w http.ResponseWriter, name, kind string) {
	b, e := assets.ReadFile("static/" + name)
	if e != nil {
		http.Error(w, "asset unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Write(b)
}
