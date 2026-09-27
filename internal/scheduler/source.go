package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"codex-chat-cli/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type SourceInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version int    `json:"version"`
}
type Source interface {
	Resolve(context.Context, string) (SourceInfo, error)
	Collect(context.Context, Job) ([]Quote, error)
}
type NeurlySource struct{ Store *mcpclient.Store }

func (n *NeurlySource) Resolve(ctx context.Context, id string) (SourceInfo, error) {
	list, err := n.Store.List()
	if err != nil {
		return SourceInfo{}, errors.New("Не удалось прочитать подключения MCP")
	}
	for _, c := range list {
		if c.ID == id {
			_, token, e := n.Store.Get(id, c.Version)
			if e != nil {
				return SourceInfo{}, e
			}
			d, e := mcpclient.Discover(ctx, c.URL, token)
			if e != nil {
				return SourceInfo{}, e
			}
			for _, t := range d.Tools {
				if t.Name == "moex_quotes" && t.Annotations != nil && t.Annotations.ReadOnlyHint {
					return SourceInfo{c.ID, c.Name, c.Version}, nil
				}
			}
			return SourceInfo{}, errors.New("У источника нет moex_quotes. Включите Московскую биржу в Neurly")
		}
	}
	return SourceInfo{}, errors.New("Выберите сохранённое MCP-подключение Neurly")
}
func (n *NeurlySource) Collect(ctx context.Context, j Job) ([]Quote, error) {
	// Only this fixed read-only operation is used, never an arbitrary scheduled tool or prompt.
	tickers := make([]any, len(j.Tickers))
	for i, t := range j.Tickers {
		tickers[i] = t
	}
	r, err := mcpclient.ReadTool(ctx, n.Store, j.ConnectionID, j.ConnectionVersion, "moex_quotes", map[string]any{"tickers": tickers, "board": "TQBR", "engine": "stock", "market": "shares"})
	if errors.Is(err, mcpclient.ErrConflict) {
		return nil, errors.New("Настройки MCP изменились. Приостановите задание и создайте новое наблюдение")
	}
	if err != nil {
		return nil, err
	}
	return parseQuotes(r, j.Tickers, time.Now())
}
func parseQuotes(result *mcp.CallToolResult, tickers []string, now time.Time) ([]Quote, error) {
	var candidates []any
	if result.StructuredContent != nil {
		b, _ := json.Marshal(result.StructuredContent)
		var v any
		if json.Unmarshal(b, &v) == nil {
			candidates = append(candidates, v)
		}
	}
	for _, c := range result.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			var v any
			if json.Unmarshal([]byte(text.Text), &v) == nil {
				candidates = append(candidates, v)
			}
		}
	}
	var tables []map[string]any
	var walk func(any, int)
	walk = func(v any, depth int) {
		if depth > 12 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			if _, ok := x["columns"].([]any); ok {
				if _, ok := x["data"].([]any); ok {
					tables = append(tables, x)
				}
			}
			for _, child := range x {
				walk(child, depth+1)
			}
		case []any:
			for _, child := range x {
				walk(child, depth+1)
			}
		}
	}
	for _, v := range candidates {
		walk(v, 0)
	}
	found := map[string]Quote{}
	for _, table := range tables {
		columns := table["columns"].([]any)
		indices := map[string]int{}
		for i, c := range columns {
			if name, ok := c.(string); ok {
				indices[strings.ToUpper(name)] = i
			}
		}
		if _, ok := indices["LAST"]; !ok {
			continue
		}
		for _, row := range table["data"].([]any) {
			cells, ok := row.([]any)
			if !ok {
				continue
			}
			get := func(name string) any {
				i, ok := indices[name]
				if !ok || i >= len(cells) {
					return nil
				}
				return cells[i]
			}
			str := func(name string) string { s, _ := get(name).(string); return s }
			ticker := str("SECID")
			if board := str("BOARDID"); board != "" && board != "TQBR" {
				continue
			}
			price, ok := get("LAST").(float64)
			if !ok || price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
				continue
			}
			stamp := str("SYSTIME")
			if stamp == "" && str("TRADEDATE") != "" && str("UPDATETIME") != "" {
				stamp = str("TRADEDATE") + " " + str("UPDATETIME")
			}
			sourceTime, err := parseSourceTime(stamp)
			if err != nil || sourceTime.After(now.Add(5*time.Minute)) {
				continue
			}
			found[ticker] = Quote{Ticker: ticker, Price: price, SourceTime: sourceTime.UTC().Format(time.RFC3339), TradingStatus: str("TRADINGSTATUS"), Observed: now.Unix()}
		}
	}
	var out []Quote
	var missing []string
	for _, t := range tickers {
		if q, ok := found[t]; ok {
			out = append(out, q)
		} else {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return out, fmt.Errorf("Нет котировки LAST с датой источника для: %s", strings.Join(missing, ", "))
	}
	return out, nil
}
func parseSourceTime(value string) (time.Time, error) {
	if t, e := time.Parse(time.RFC3339, value); e == nil {
		return t, nil
	}
	zone := time.FixedZone("Europe/Moscow", 3*60*60)
	return time.ParseInLocation("2006-01-02 15:04:05", value, zone)
}
