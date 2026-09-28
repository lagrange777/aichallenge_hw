package brokerdemo

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func falsePointer() *bool { value := false; return &value }
func truePointer() *bool  { value := true; return &value }

var closedWrite = &mcp.ToolAnnotations{DestructiveHint: falsePointer(), OpenWorldHint: falsePointer()}
var closedDestructiveWrite = &mcp.ToolAnnotations{DestructiveHint: truePointer(), OpenWorldHint: falsePointer()}
var readOnly = &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: falsePointer()}

func OrderMCP(service *OrderService) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "demo-order-mcp", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name: "create_buy_order", Description: "Create an idempotent BUY order for demo-user. Call only after the user explicitly asks to buy and supplies ticker, quantity and maximum price. Prices are integer kopecks: 320 RUB is 32000. Continue with demo-validation-mcp/validate_order using the returned orderToken unchanged.",
		Annotations: closedWrite,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"requestId":       map[string]any{"type": "string", "minLength": 8, "maxLength": 100, "description": "Stable unique request ID generated once for this user request"},
			"instrument":      map[string]any{"type": "string", "pattern": instrumentPattern.String(), "description": "Exchange ticker, for example SBER"},
			"quantity":        map[string]any{"type": "integer", "minimum": 1, "maximum": 1000000},
			"limitPriceMinor": map[string]any{"type": "integer", "minimum": 1, "description": "Maximum unit price in kopecks"},
		}, "required": []string{"requestId", "instrument", "quantity", "limitPriceMinor"}, "additionalProperties": false},
	}, func(_ context.Context, _ *mcp.CallToolRequest, input CreateOrderInput) (*mcp.CallToolResult, Order, error) {
		value, err := service.Create(input)
		return nil, value, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "get_order", Description: "Get one demo-user order by orderId.", Annotations: readOnly, InputSchema: orderIDSchema()}, func(_ context.Context, _ *mcp.CallToolRequest, input struct {
		OrderID string `json:"orderId"`
	}) (*mcp.CallToolResult, Order, error) {
		value, err := service.Get(input.OrderID)
		return nil, value, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "list_my_orders", Description: "List the latest demo-user orders and their terminal or pending status.", Annotations: readOnly}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, map[string]any, error) {
		orders, err := service.List()
		return nil, map[string]any{"userId": DemoUserID, "orders": orders}, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "record_order_execution", Description: "Record the signed broker result in the matching order. Call immediately after execute_validated_order, passing its executionToken unchanged.", Annotations: closedWrite,
		InputSchema: tokenSchema("executionToken", "Signed execution token returned by demo-broker-mcp"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, input struct {
		ExecutionToken string `json:"executionToken"`
	}) (*mcp.CallToolResult, Order, error) {
		value, err := service.RecordExecution(input.ExecutionToken)
		return nil, value, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "record_validation_rejection", Description: "Mark an order rejected after validate_order returns REJECTED. Pass validationToken unchanged. Never call for APPROVED validations.", Annotations: closedWrite,
		InputSchema: tokenSchema("validationToken", "Signed rejection token returned by demo-validation-mcp"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, input struct {
		ValidationToken string `json:"validationToken"`
	}) (*mcp.CallToolResult, Order, error) {
		value, err := service.RecordValidationRejection(input.ValidationToken)
		return nil, value, err
	})
	return server
}

func ValidationMCP(service *ValidationService) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "demo-validation-mcp", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name: "validate_order", Description: "Validate an order returned by demo-order-mcp against the deterministic demo exchange. Pass orderToken unchanged. If APPROVED, continue with demo-broker-mcp/execute_validated_order using validationToken. If REJECTED, call record_validation_rejection instead.",
		Annotations: closedWrite,
		InputSchema: tokenSchema("orderToken", "Signed order token returned by demo-order-mcp"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, input ValidateOrderInput) (*mcp.CallToolResult, Validation, error) {
		value, err := service.Validate(input)
		return nil, value, err
	})
	return server
}

func BrokerMCP(service *BrokerService) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "demo-broker-mcp", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name: "execute_validated_order", Description: "Execute exactly one APPROVED demo order. Call only after validate_order and pass validationToken unchanged. The signed token fixes order, quote, quantity and price; repeated calls are idempotent by orderId.", Annotations: closedDestructiveWrite,
		InputSchema: tokenSchema("validationToken", "Signed APPROVED validation token returned by demo-validation-mcp"),
	}, func(_ context.Context, _ *mcp.CallToolRequest, input struct {
		ValidationToken string `json:"validationToken"`
	}) (*mcp.CallToolResult, Execution, error) {
		value, err := service.Execute(input.ValidationToken)
		return nil, value, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "get_account", Description: "Get demo-user RUB cash balance after any completed trades. balanceMinor is an integer number of kopecks; divide it by exactly 100 to display rubles.", Annotations: readOnly}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, Account, error) {
		value, err := service.Account()
		return nil, value, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "get_portfolio", Description: "Get demo-user positions and average purchase prices. averagePriceMinor is an integer number of kopecks; divide it by exactly 100 to display rubles.", Annotations: readOnly}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, Portfolio, error) {
		value, err := service.Portfolio()
		return nil, value, err
	})
	return server
}

func orderIDSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"orderId": map[string]any{"type": "string", "minLength": 1}}, "required": []string{"orderId"}, "additionalProperties": false}
}

func tokenSchema(name, description string) map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{name: map[string]any{"type": "string", "minLength": 20, "description": description}}, "required": []string{name}, "additionalProperties": false}
}

func Open(role, path, secret string) (*mcp.Server, func() error, error) {
	switch role {
	case "orders":
		service, err := OpenOrderService(path, secret)
		if err != nil {
			return nil, nil, err
		}
		return OrderMCP(service), service.Close, nil
	case "validation":
		service, err := OpenValidationService(path, secret)
		if err != nil {
			return nil, nil, err
		}
		return ValidationMCP(service), service.Close, nil
	case "broker":
		service, err := OpenBrokerService(path, secret)
		if err != nil {
			return nil, nil, err
		}
		return BrokerMCP(service), service.Close, nil
	default:
		return nil, nil, errors.New("BROKER_DEMO_ROLE must be orders, validation or broker")
	}
}
