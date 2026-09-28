package brokerdemo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testEndpoint(t *testing.T, server *mcp.Server) *httptest.Server {
	t.Helper()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	endpoint := httptest.NewServer(handler)
	t.Cleanup(endpoint.Close)
	return endpoint
}

func testSession(t *testing.T, endpoint string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "broker-flow-test", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func callValue[T any](t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) T {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("%s: %+v", name, result)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var value T
	if err = json.Unmarshal(data, &value); err != nil {
		t.Fatalf("%s result: %v: %s", name, err, data)
	}
	return value
}

func TestThreeMCPServersCompleteSignedOrderFlow(t *testing.T) {
	root := t.TempDir()
	const secret = "test-demo-shared-secret"
	orders, err := OpenOrderService(filepath.Join(root, "orders.db"), secret)
	if err != nil {
		t.Fatal(err)
	}
	defer orders.Close()
	validation, err := OpenValidationService(filepath.Join(root, "validation.db"), secret)
	if err != nil {
		t.Fatal(err)
	}
	defer validation.Close()
	broker, err := OpenBrokerService(filepath.Join(root, "broker.db"), secret)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()

	orderSession := testSession(t, testEndpoint(t, OrderMCP(orders)).URL)
	validationSession := testSession(t, testEndpoint(t, ValidationMCP(validation)).URL)
	brokerSession := testSession(t, testEndpoint(t, BrokerMCP(broker)).URL)

	order := callValue[Order](t, orderSession, "create_buy_order", map[string]any{"requestId": "flow-test-001", "instrument": "SBER", "quantity": 10, "limitPriceMinor": 32000})
	if order.Status != "NEW" || order.UserID != DemoUserID || order.OrderToken == "" {
		t.Fatalf("order: %+v", order)
	}
	checked := callValue[Validation](t, validationSession, "validate_order", map[string]any{"orderToken": order.OrderToken})
	if checked.Decision != "APPROVED" || checked.ValidationToken == "" || checked.MarketPriceMinor != 31850 {
		t.Fatalf("validation: %+v", checked)
	}
	execution := callValue[Execution](t, brokerSession, "execute_validated_order", map[string]any{"validationToken": checked.ValidationToken})
	if execution.Status != "FILLED" || execution.ExecutionToken == "" || execution.OrderID != order.ID {
		t.Fatalf("execution: %+v", execution)
	}
	completed := callValue[Order](t, orderSession, "record_order_execution", map[string]any{"executionToken": execution.ExecutionToken})
	account := callValue[Account](t, brokerSession, "get_account", map[string]any{})
	portfolio := callValue[Portfolio](t, brokerSession, "get_portfolio", map[string]any{})
	if completed.Status != "FILLED" || completed.TradeID != execution.TradeID {
		t.Fatalf("completed order: %+v", completed)
	}
	if account.BalanceMinor != 100000000-execution.TotalMinor-execution.CommissionMinor {
		t.Fatalf("account: %+v", account)
	}
	if len(portfolio.Positions) != 1 || portfolio.Positions[0].Instrument != "SBER" || portfolio.Positions[0].Quantity != 10 {
		t.Fatalf("portfolio: %+v", portfolio)
	}

	repeated := callValue[Execution](t, brokerSession, "execute_validated_order", map[string]any{"validationToken": checked.ValidationToken})
	afterRepeat := callValue[Account](t, brokerSession, "get_account", map[string]any{})
	if repeated.TradeID != execution.TradeID || afterRepeat.BalanceMinor != account.BalanceMinor {
		t.Fatalf("execution was not idempotent: first=%+v repeated=%+v balance=%+v", execution, repeated, afterRepeat)
	}
}

func TestRejectedValidationNeverExecutes(t *testing.T) {
	root := t.TempDir()
	const secret = "test-demo-shared-secret"
	orders, err := OpenOrderService(filepath.Join(root, "orders.db"), secret)
	if err != nil {
		t.Fatal(err)
	}
	defer orders.Close()
	validation, err := OpenValidationService(filepath.Join(root, "validation.db"), secret)
	if err != nil {
		t.Fatal(err)
	}
	defer validation.Close()
	broker, err := OpenBrokerService(filepath.Join(root, "broker.db"), secret)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	order, err := orders.Create(CreateOrderInput{RequestID: "flow-test-reject", Instrument: "SBER", Quantity: 10, LimitPriceMinor: 30000})
	if err != nil {
		t.Fatal(err)
	}
	tampered := order.OrderToken[:len(order.OrderToken)-1] + "A"
	if tampered == order.OrderToken {
		tampered = order.OrderToken[:len(order.OrderToken)-1] + "B"
	}
	if _, err = validation.Validate(ValidateOrderInput{OrderToken: tampered}); err == nil {
		t.Fatal("validation accepted a modified order token")
	}
	checked, err := validation.Validate(ValidateOrderInput{OrderToken: order.OrderToken})
	if err != nil || checked.Decision != "REJECTED" {
		t.Fatalf("validation: %+v %v", checked, err)
	}
	if _, err = broker.Execute(checked.ValidationToken); err == nil {
		t.Fatal("broker accepted rejected validation")
	}
	rejected, err := orders.RecordValidationRejection(checked.ValidationToken)
	if err != nil || rejected.Status != "REJECTED" {
		t.Fatalf("order rejection: %+v %v", rejected, err)
	}
}
