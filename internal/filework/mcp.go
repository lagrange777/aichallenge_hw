package filework

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type pathInput struct {
	Path string `json:"path"`
}
type queryInput struct {
	Query string `json:"query"`
}
type patchInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type finishInput struct {
	Outcome string `json:"outcome"`
	Summary string `json:"summary"`
}
type match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

func (s *Service) connect(ctx context.Context, id string) (*mcp.ClientSession, func(), error) {
	server := mcp.NewServer(&mcp.Implementation{Name: "codex-file-workspace", Version: "1.0.0"}, nil)
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	mcp.AddTool(server, &mcp.Tool{Name: "list_files", Description: "Discover public source/documentation paths in this workspace. No secret or generated files.", Annotations: ro}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		names, e := list(s.root)
		return nil, map[string]any{"files": names}, e
	})
	mcp.AddTool(server, &mcp.Tool{Name: "search_files", Description: "Case-insensitive literal search across public UTF-8 files. Returns at most 60 matches and indicates truncation. Use read_file for full context.", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, any, error) {
		if len(in.Query) < 2 || len(in.Query) > 200 {
			return nil, nil, errors.New("query must be 2..200 bytes")
		}
		names, e := list(s.root)
		if e != nil {
			return nil, nil, e
		}
		matches := []match{}
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			f, e := read(s.root, name)
			if e != nil {
				continue
			}
			for i, line := range strings.Split(f.Content, "\n") {
				if strings.Contains(strings.ToLower(line), strings.ToLower(in.Query)) {
					if len(matches) == 60 {
						return nil, map[string]any{"matches": matches, "truncated": true}, nil
					}
					runes := []rune(line)
					if len(runes) > 350 {
						line = string(runes[:350]) + "…"
					}
					matches = append(matches, match{name, i + 1, line})
				}
			}
		}
		return nil, map[string]any{"matches": matches, "truncated": false}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "read_file", Description: "Read current complete public UTF-8 file (max 96 KiB), with SHA-256. Missing file returns exists=false. Required for existing patch targets.", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in pathInput) (*mcp.CallToolResult, File, error) {
		s.applyMu.Lock()
		defer s.applyMu.Unlock()
		f, e := read(s.root, in.Path)
		if e == nil && f.Exists {
			s.mu.Lock()
			s.tasks[id].Reads[in.Path] = f
			s.mu.Unlock()
		}
		return nil, f, e
	})
	closed, destructive := false, false
	mcp.AddTool(server, &mcp.Tool{Name: "propose_patch", Description: "Stage complete UTF-8 content for README.md or docs/*.md. Does NOT modify workspace. Read at least 3 distinct existing relevant files first; read existing target before proposing. Max 8 targets.", Annotations: &mcp.ToolAnnotations{OpenWorldHint: &closed, DestructiveHint: &destructive}}, func(ctx context.Context, _ *mcp.CallToolRequest, in patchInput) (*mcp.CallToolResult, any, error) {
		s.applyMu.Lock()
		defer s.applyMu.Unlock()
		s.mu.Lock()
		defer s.mu.Unlock()
		t := s.tasks[id]
		if !writable(in.Path) || len(in.Content) > maxFile || !utf8.ValidString(in.Content) || strings.ContainsRune(in.Content, 0) {
			return nil, nil, errors.New("invalid writable path or content")
		}
		if len(t.Reads) < 3 {
			return nil, nil, errors.New("read at least three distinct existing files first")
		}
		before, e := read(s.root, in.Path)
		if e != nil {
			return nil, nil, e
		}
		if before.Exists {
			observed, ok := t.Reads[in.Path]
			if !ok || observed.Hash != before.Hash {
				return nil, nil, errors.New("read current target before proposing a change")
			}
		}
		p := Patch{Path: in.Path, Before: before.Content, After: in.Content, Hash: before.Hash, Exists: before.Exists}
		p.Diff = unified(p)
		for i, q := range t.Patches {
			if q.Path == p.Path {
				t.Patches = append(t.Patches[:i], t.Patches[i+1:]...)
				break
			}
		}
		if p.Diff == "" {
			return nil, map[string]any{"status": "unchanged", "path": p.Path}, nil
		}
		if len(t.Patches) >= 8 {
			return nil, nil, errors.New("at most eight patch targets")
		}
		t.Patches = append(t.Patches, p)
		return nil, map[string]any{"status": "proposed_not_applied", "path": p.Path, "diff": p.Diff}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "finish_task", Description: "Required before final answer. outcome=ready only after actual requested patches and get_diff; no_changes only after verifying the goal is ALREADY satisfied; blocked if unable. summary: Russian findings and fully read source paths. Never call no_changes merely because you have not prepared a patch yet."}, func(ctx context.Context, _ *mcp.CallToolRequest, in finishInput) (*mcp.CallToolResult, any, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		t := s.tasks[id]
		if len(t.Reads) < 3 || len(in.Summary) < 10 || len(in.Summary) > 10000 {
			return nil, nil, errors.New("read three files and provide a bounded factual summary")
		}
		switch in.Outcome {
		case "ready":
			if len(t.Patches) == 0 {
				return nil, nil, errors.New("prepare the requested patches before finishing as ready")
			}
		case "no_changes":
			if len(t.Patches) > 0 {
				return nil, nil, errors.New("pending patches exist")
			}
		case "blocked":
		default:
			return nil, nil, errors.New("outcome must be ready, no_changes or blocked")
		}
		t.Outcome, t.Answer = in.Outcome, in.Summary
		return nil, map[string]string{"outcome": in.Outcome}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "get_diff", Description: "Return all proposed changes. They are not saved until the human clicks Apply.", Annotations: ro}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return nil, map[string]any{"patches": s.tasks[id].Patches}, nil
	})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		return nil, nil, err
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "codex-work-agent", Version: "1.0.0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		ss.Close()
		return nil, nil, err
	}
	return cs, func() { cs.Close(); ss.Close() }, nil
}
func callMCP(ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) (string, error) {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", err
	}
	var text strings.Builder
	for _, c := range result.Content {
		if v, ok := c.(*mcp.TextContent); ok {
			text.WriteString(v.Text)
		}
	}
	if result.IsError {
		return "", fmt.Errorf("MCP: %s", text.String())
	}
	if text.Len() == 0 {
		b, _ := json.Marshal(result.StructuredContent)
		return string(b), nil
	}
	return text.String(), nil
}
