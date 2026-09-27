package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ReadTool is for explicitly configured background sources. It never grants chat access.
// The exact connection revision is pinned by the scheduled job.
func ReadTool(ctx context.Context, store *Store, id string, version int, name string, args map[string]any) (*mcp.CallToolResult, error) {
	c, token, err := store.Get(id, version)
	if err != nil {
		return nil, ErrConflict
	}
	catalog, err := Discover(ctx, c.URL, token)
	if err != nil {
		return nil, err
	}
	var tool *mcp.Tool
	for _, t := range catalog.Tools {
		if t.Name == name && t.Annotations != nil && t.Annotations.ReadOnlyHint {
			tool = t
			break
		}
	}
	if tool == nil {
		return nil, errors.New("Инструмент недоступен или не помечен как readOnly")
	}
	data, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return nil, err
	}
	var schema jsonschema.Schema
	if json.Unmarshal(data, &schema) != nil {
		return nil, errors.New("Некорректная схема источника")
	}
	resolved, err := schema.Resolve(nil)
	if err != nil || resolved.Validate(args) != nil {
		return nil, errors.New("Аргументы не соответствуют схеме источника")
	}
	session, closeSession, err := connect(ctx, c.URL, token)
	if err != nil {
		return nil, err
	}
	defer closeSession()
	if _, _, err = store.Get(id, version); err != nil {
		return nil, ErrConflict
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, connectionError(ctx, err, "Не удалось вызвать MCP-инструмент")
	}
	if result == nil || result.IsError {
		return nil, errors.New("MCP-инструмент вернул ошибку. Проверьте параметры и доступ к серверу")
	}
	return result, nil
}
