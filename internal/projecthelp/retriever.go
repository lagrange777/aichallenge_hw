package projecthelp

import (
	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/mcpclient"
	"context"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"slices"
	"strings"
	"time"
)

type Retriever struct {
	Index    *Index
	Store    *mcpclient.Store
	Endpoint string
}

func (r *Retriever) Retrieve(ctx context.Context, query string) (*agent.Retrieval, error) {
	start := time.Now()
	snapshot, err := r.Index.Refresh()
	if err != nil {
		return nil, err
	}
	result := &agent.Retrieval{Mode: "rag", Query: query, Strategy: "project BM25", CorpusHash: snapshot.Hash, Sources: Search(snapshot, query), Project: &agent.ProjectContext{Documents: snapshot.Documents, Chunks: len(snapshot.Chunks), IndexHash: snapshot.Hash}}
	tools := []string{"git_branch"}
	lower := strings.ToLower(query)
	if strings.Contains(lower, "структур") || strings.Contains(lower, "файл") || strings.Contains(lower, "каталог") || strings.Contains(lower, "files") {
		tools = append(tools, "project_files")
	}
	for _, name := range tools {
		run := agent.ToolRun{ServerName: "codex-project-mcp", Name: name, Arguments: "{}"}
		toolStart := time.Now()
		value, e := r.call(ctx, name)
		run.DurationMS = time.Since(toolStart).Milliseconds()
		if e != nil {
			run.Error = true
			run.Output = "Контекст MCP недоступен; текущее состояние Git неизвестно."
			result.Warning = run.Output
		} else {
			run.Output = value
			result.Sources = append(result.Sources, agent.Source{Ref: fmt.Sprintf("S%d", len(result.Sources)+1), Source: "mcp://codex-project-mcp/" + name, Title: "Актуальный контекст проекта через MCP", Section: name, ChunkID: name + "@" + time.Now().UTC().Format(time.RFC3339Nano), Text: value})
		}
		result.Project.Runs = append(result.Project.Runs, run)
	}
	result.Empty = len(result.Sources) == 0
	result.DurationMS = time.Since(start).Milliseconds()
	return result, nil
}
func (r *Retriever) call(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if r.Store == nil {
		return "", fmt.Errorf("MCP store unavailable")
	}
	connections, e := r.Store.List()
	if e != nil {
		return "", e
	}
	for _, c := range connections {
		if c.URL != r.Endpoint || !c.Enabled || !slices.Contains(c.AllowedTools, name) {
			continue
		}
		value, e := mcpclient.ReadTool(ctx, r.Store, c.ID, c.Version, name, map[string]any{})
		if e != nil {
			return "", e
		}
		current, _, e := r.Store.Get(c.ID, c.Version)
		if e != nil || !current.Enabled || !slices.Contains(current.AllowedTools, name) {
			return "", mcpclient.ErrConflict
		}
		// The SDK emits structured content for typed tools. Keep the same data
		// as printable numbered lines for the existing citation validator.
		if value.StructuredContent != nil {
			data, e := json.MarshalIndent(value.StructuredContent, "", "  ")
			if e != nil || len(data) > 24<<10 {
				return "", fmt.Errorf("invalid MCP output")
			}
			return string(data), nil
		}
		for _, item := range value.Content {
			if text, ok := item.(*mcp.TextContent); ok && json.Valid([]byte(text.Text)) && len(text.Text) <= 24<<10 {
				var decoded any
				json.Unmarshal([]byte(text.Text), &decoded)
				data, _ := json.MarshalIndent(decoded, "", "  ")
				return string(data), nil
			}
		}
		return "", fmt.Errorf("MCP output unavailable")
	}
	return "", fmt.Errorf("Project MCP permission not granted")
}
