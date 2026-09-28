package mcpclient

import (
	"codex-chat-cli/internal/agent"
	"codex-chat-cli/internal/brokerdemo"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type brokerFlowLLM struct {
	t        *testing.T
	step     int
	selected []string
}

func (f *brokerFlowLLM) Complete(_ context.Context, request agent.CompletionRequest) (agent.CompletionResponse, error) {
	if f.step == 6 {
		portfolio := f.previousResult(request)
		if _, ok := portfolio["positions"]; !ok {
			f.t.Fatalf("final step did not receive the portfolio: %+v", portfolio)
		}
		return agent.CompletionResponse{ResponseID: "broker-final", Output: "Заявка исполнена, счёт и портфель получены."}, nil
	}
	definitions := map[string]agent.ToolDefinition{}
	for _, definition := range request.Tools {
		definitions[definition.ToolName] = definition
	}
	var toolName string
	var arguments map[string]any
	switch f.step {
	case 0:
		toolName = "create_buy_order"
		arguments = map[string]any{"requestId": "provider-flow-001", "instrument": "SBER", "quantity": 10, "limitPriceMinor": 32000}
	case 1:
		toolName = "validate_order"
		arguments = map[string]any{"orderToken": requiredString(f.t, f.previousResult(request), "orderToken")}
	case 2:
		toolName = "execute_validated_order"
		arguments = map[string]any{"validationToken": requiredString(f.t, f.previousResult(request), "validationToken")}
	case 3:
		toolName = "record_order_execution"
		arguments = map[string]any{"executionToken": requiredString(f.t, f.previousResult(request), "executionToken")}
	case 4:
		if requiredString(f.t, f.previousResult(request), "status") != "FILLED" {
			f.t.Fatal("order was not recorded as filled")
		}
		toolName, arguments = "get_account", map[string]any{}
	case 5:
		if balance, ok := f.previousResult(request)["balanceMinor"].(float64); !ok || balance != 99681181 {
			f.t.Fatalf("unexpected account result: %+v", f.previousResult(request))
		}
		toolName, arguments = "get_portfolio", map[string]any{}
	default:
		f.t.Fatalf("unexpected step %d", f.step)
	}
	definition, ok := definitions[toolName]
	if !ok {
		f.t.Fatalf("tool %s is absent from the registered catalog", toolName)
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		f.t.Fatal(err)
	}
	f.selected = append(f.selected, definition.ServerName+"/"+definition.ToolName)
	f.step++
	return agent.CompletionResponse{ResponseID: fmt.Sprintf("broker-step-%d", f.step), ToolCalls: []agent.ToolCall{{CallID: fmt.Sprintf("call-%d", f.step), Name: definition.Name, Arguments: string(raw)}}}, nil
}

func (f *brokerFlowLLM) previousResult(request agent.CompletionRequest) map[string]any {
	f.t.Helper()
	if len(request.ToolOutputs) != 1 {
		f.t.Fatalf("expected one previous tool output, got %+v", request.ToolOutputs)
	}
	var envelope struct {
		Result string `json:"result"`
		Error  bool   `json:"error"`
	}
	if err := json.Unmarshal([]byte(request.ToolOutputs[0].Output), &envelope); err != nil || envelope.Error {
		f.t.Fatalf("invalid tool output envelope: %s (%v)", request.ToolOutputs[0].Output, err)
	}
	var result struct {
		StructuredContent map[string]any `json:"structuredContent"`
	}
	if err := json.Unmarshal([]byte(envelope.Result), &result); err != nil || result.StructuredContent == nil {
		f.t.Fatalf("invalid MCP result: %s (%v)", envelope.Result, err)
	}
	return result.StructuredContent
}

func requiredString(t *testing.T, value map[string]any, key string) string {
	t.Helper()
	result, ok := value[key].(string)
	if !ok || result == "" {
		t.Fatalf("missing %s in %+v", key, value)
	}
	return result
}

func startBrokerMCP(t *testing.T, server *mcp.Server) string {
	t.Helper()
	remote := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	t.Cleanup(remote.Close)
	return remote.URL
}

func registerBrokerMCP(t *testing.T, store *Store, name, endpoint string, tools []string) {
	t.Helper()
	connection, err := store.Save(Connection{Name: name, URL: endpoint}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetPermissions(connection.ID, connection.Version, true, tools); err != nil {
		t.Fatal(err)
	}
}

func TestAgentCompletesFlowThroughThreeRegisteredMCPServers(t *testing.T) {
	root := t.TempDir()
	const secret = "provider-flow-shared-secret"
	orders, err := brokerdemo.OpenOrderService(filepath.Join(root, "orders.db"), secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { orders.Close() })
	validation, err := brokerdemo.OpenValidationService(filepath.Join(root, "validation.db"), secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { validation.Close() })
	broker, err := brokerdemo.OpenBrokerService(filepath.Join(root, "broker.db"), secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { broker.Close() })

	store, err := NewStore(filepath.Join(root, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	registerBrokerMCP(t, store, "Demo Orders", startBrokerMCP(t, brokerdemo.OrderMCP(orders)), []string{"create_buy_order", "get_order", "list_my_orders", "record_order_execution", "record_validation_rejection"})
	registerBrokerMCP(t, store, "Demo Validation", startBrokerMCP(t, brokerdemo.ValidationMCP(validation)), []string{"validate_order"})
	registerBrokerMCP(t, store, "Demo Broker", startBrokerMCP(t, brokerdemo.BrokerMCP(broker)), []string{"execute_validated_order", "get_account", "get_portfolio"})

	llm := &brokerFlowLLM{t: t}
	chat := agent.New(llm, "test", agent.WithTools(NewProvider(store)))
	response, err := chat.Ask(context.Background(), agent.Request{Message: "Купи 10 SBER не дороже 320 рублей и покажи счёт и портфель"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Demo Orders/create_buy_order",
		"Demo Validation/validate_order",
		"Demo Broker/execute_validated_order",
		"Demo Orders/record_order_execution",
		"Demo Broker/get_account",
		"Demo Broker/get_portfolio",
	}
	if response.Text == "" || fmt.Sprint(llm.selected) != fmt.Sprint(want) {
		t.Fatalf("response=%q selected=%v", response.Text, llm.selected)
	}
	account, err := broker.Account()
	if err != nil || account.BalanceMinor != 99681181 {
		t.Fatalf("account after flow: %+v %v", account, err)
	}
	portfolio, err := broker.Portfolio()
	if err != nil || len(portfolio.Positions) != 1 || portfolio.Positions[0].Quantity != 10 {
		t.Fatalf("portfolio after flow: %+v %v", portfolio, err)
	}
}
