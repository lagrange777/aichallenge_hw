package brokerdemo

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var instrumentPattern = regexp.MustCompile(`^[A-Z][A-Z0-9._-]{0,19}$`)
var requestPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,100}$`)

const (
	maxOrderQuantity  = int64(1_000_000)
	maxUnitPriceMinor = int64(1_000_000_000_00)
)

type OrderService struct {
	db     *sql.DB
	secret string
	now    func() int64
}

func OpenOrderService(path, secret string) (*OrderService, error) {
	if err := validateSecret(secret); err != nil {
		return nil, err
	}
	db, err := openSQLite(path, `
CREATE TABLE IF NOT EXISTS orders(
 id TEXT PRIMARY KEY, request_id TEXT UNIQUE NOT NULL, user_id TEXT NOT NULL,
 instrument TEXT NOT NULL, quantity INTEGER NOT NULL, limit_price_minor INTEGER NOT NULL,
 currency TEXT NOT NULL, status TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '',
 trade_id TEXT NOT NULL DEFAULT '', execution_price_minor INTEGER NOT NULL DEFAULT 0,
 total_minor INTEGER NOT NULL DEFAULT 0, commission_minor INTEGER NOT NULL DEFAULT 0,
 version INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS orders_user_created ON orders(user_id, created_at DESC);`)
	if err != nil {
		return nil, err
	}
	return &OrderService{db: db, secret: secret, now: unixNow}, nil
}

func (s *OrderService) Close() error { return s.db.Close() }

func (s *OrderService) Create(input CreateOrderInput) (Order, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.Instrument = strings.ToUpper(strings.TrimSpace(input.Instrument))
	if !requestPattern.MatchString(input.RequestID) {
		return Order{}, errors.New("requestId must contain 8-100 letters, digits or ._:-")
	}
	if !instrumentPattern.MatchString(input.Instrument) {
		return Order{}, errors.New("invalid instrument ticker")
	}
	if input.Quantity < 1 || input.Quantity > maxOrderQuantity || input.LimitPriceMinor < 1 || input.LimitPriceMinor > maxUnitPriceMinor {
		return Order{}, errors.New("quantity or limit price is outside the demo limits")
	}
	if existing, err := s.byRequest(input.RequestID); err == nil {
		if existing.Instrument != input.Instrument || existing.Quantity != input.Quantity || existing.LimitPriceMinor != input.LimitPriceMinor {
			return Order{}, errors.New("requestId is already used for another order")
		}
		return s.withToken(existing)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Order{}, err
	}
	id, err := newID("ord")
	if err != nil {
		return Order{}, err
	}
	now := s.now()
	order := Order{ID: id, RequestID: input.RequestID, UserID: DemoUserID, Instrument: input.Instrument, Quantity: input.Quantity, LimitPriceMinor: input.LimitPriceMinor, Currency: "RUB", Status: "NEW", Version: 1, CreatedAt: now, UpdatedAt: now}
	_, err = s.db.Exec(`INSERT INTO orders(id,request_id,user_id,instrument,quantity,limit_price_minor,currency,status,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, order.ID, order.RequestID, order.UserID, order.Instrument, order.Quantity, order.LimitPriceMinor, order.Currency, order.Status, order.Version, order.CreatedAt, order.UpdatedAt)
	if err != nil {
		return Order{}, err
	}
	return s.withToken(order)
}

func (s *OrderService) Get(id string) (Order, error) {
	value, err := scanOrder(s.db.QueryRow(`SELECT id,request_id,user_id,instrument,quantity,limit_price_minor,currency,status,reason,trade_id,execution_price_minor,total_minor,commission_minor,version,created_at,updated_at FROM orders WHERE id=? AND user_id=?`, strings.TrimSpace(id), DemoUserID))
	if err != nil {
		return Order{}, err
	}
	return s.withToken(value)
}

func (s *OrderService) byRequest(requestID string) (Order, error) {
	return scanOrder(s.db.QueryRow(`SELECT id,request_id,user_id,instrument,quantity,limit_price_minor,currency,status,reason,trade_id,execution_price_minor,total_minor,commission_minor,version,created_at,updated_at FROM orders WHERE request_id=? AND user_id=?`, requestID, DemoUserID))
}

func (s *OrderService) List() ([]Order, error) {
	rows, err := s.db.Query(`SELECT id,request_id,user_id,instrument,quantity,limit_price_minor,currency,status,reason,trade_id,execution_price_minor,total_minor,commission_minor,version,created_at,updated_at FROM orders WHERE user_id=? ORDER BY created_at DESC,id DESC LIMIT 100`, DemoUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orders := []Order{}
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		order, err = s.withToken(order)
		if err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, rows.Err()
}

func (s *OrderService) withToken(value Order) (Order, error) {
	token, err := orderToken(value, s.secret)
	value.OrderToken = token
	return value, err
}

type rowScanner interface{ Scan(...any) error }

func scanOrder(row rowScanner) (Order, error) {
	var value Order
	err := row.Scan(&value.ID, &value.RequestID, &value.UserID, &value.Instrument, &value.Quantity, &value.LimitPriceMinor, &value.Currency, &value.Status, &value.Reason, &value.TradeID, &value.ExecutionPriceMinor, &value.TotalMinor, &value.CommissionMinor, &value.Version, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}

func (s *OrderService) RecordExecution(token string) (Order, error) {
	var execution Execution
	if err := verify(strings.TrimSpace(token), "execution", s.secret, &execution); err != nil {
		return Order{}, errors.New("broker execution token is invalid")
	}
	if execution.UserID != DemoUserID || (execution.Status != "FILLED" && execution.Status != "REJECTED") {
		return Order{}, errors.New("broker execution result is invalid")
	}
	order, err := s.Get(execution.OrderID)
	if err != nil {
		return Order{}, err
	}
	if execution.Instrument != order.Instrument || execution.Quantity != order.Quantity || execution.Currency != order.Currency || execution.ExecutionPriceMinor < 1 || execution.ExecutionPriceMinor > order.LimitPriceMinor || execution.TradeID == "" {
		return Order{}, errors.New("broker execution does not match the order")
	}
	total := execution.Quantity * execution.ExecutionPriceMinor
	commission := total / 1000
	if total%1000 != 0 {
		commission++
	}
	if execution.TotalMinor != total || execution.CommissionMinor != commission {
		return Order{}, errors.New("broker execution totals do not match the order")
	}
	if order.Status == execution.Status && order.TradeID == execution.TradeID {
		return order, nil
	}
	if order.Status != "NEW" {
		return Order{}, fmt.Errorf("order is already in terminal status %s", order.Status)
	}
	result, err := s.db.Exec(`UPDATE orders SET status=?,reason=?,trade_id=?,execution_price_minor=?,total_minor=?,commission_minor=?,version=version+1,updated_at=? WHERE id=? AND status='NEW' AND version=?`, execution.Status, execution.Reason, execution.TradeID, execution.ExecutionPriceMinor, execution.TotalMinor, execution.CommissionMinor, s.now(), order.ID, order.Version)
	if err != nil {
		return Order{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return Order{}, errors.New("order changed concurrently")
	}
	return s.Get(order.ID)
}

func (s *OrderService) RecordValidationRejection(token string) (Order, error) {
	var validation Validation
	if err := verify(strings.TrimSpace(token), "validation", s.secret, &validation); err != nil {
		return Order{}, errors.New("validation token is invalid")
	}
	if validation.UserID != DemoUserID || validation.Decision != "REJECTED" {
		return Order{}, errors.New("validation result is not a rejection")
	}
	order, err := s.Get(validation.OrderID)
	if err != nil {
		return Order{}, err
	}
	if order.Status == "REJECTED" {
		return order, nil
	}
	if order.Status != "NEW" {
		return Order{}, fmt.Errorf("order is already in terminal status %s", order.Status)
	}
	if validation.Instrument != order.Instrument || validation.Quantity != order.Quantity || validation.LimitPriceMinor != order.LimitPriceMinor || validation.Currency != order.Currency {
		return Order{}, errors.New("validation result does not match the order")
	}
	result, err := s.db.Exec(`UPDATE orders SET status='REJECTED',reason=?,version=version+1,updated_at=? WHERE id=? AND status='NEW' AND version=?`, validation.Reason, s.now(), order.ID, order.Version)
	if err != nil {
		return Order{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return Order{}, errors.New("order changed concurrently")
	}
	return s.Get(order.ID)
}
