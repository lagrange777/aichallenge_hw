package brokerdemo

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
)

type BrokerService struct {
	db     *sql.DB
	secret string
	now    func() int64
}

func OpenBrokerService(path, secret string) (*BrokerService, error) {
	if err := validateSecret(secret); err != nil {
		return nil, err
	}
	db, err := openSQLite(path, `
CREATE TABLE IF NOT EXISTS accounts(user_id TEXT PRIMARY KEY,currency TEXT NOT NULL,balance_minor INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS positions(user_id TEXT NOT NULL,instrument TEXT NOT NULL,quantity INTEGER NOT NULL,average_price_minor INTEGER NOT NULL,PRIMARY KEY(user_id,instrument));
CREATE TABLE IF NOT EXISTS trades(trade_id TEXT PRIMARY KEY,order_id TEXT UNIQUE NOT NULL,user_id TEXT NOT NULL,instrument TEXT NOT NULL,quantity INTEGER NOT NULL,execution_price_minor INTEGER NOT NULL,total_minor INTEGER NOT NULL,commission_minor INTEGER NOT NULL,currency TEXT NOT NULL,status TEXT NOT NULL,reason TEXT NOT NULL,executed_at INTEGER NOT NULL);
INSERT OR IGNORE INTO accounts VALUES('demo-user','RUB',100000000);`)
	if err != nil {
		return nil, err
	}
	return &BrokerService{db: db, secret: secret, now: unixNow}, nil
}

func (s *BrokerService) Close() error { return s.db.Close() }

func (s *BrokerService) Execute(token string) (Execution, error) {
	var validation Validation
	if err := verify(strings.TrimSpace(token), "validation", s.secret, &validation); err != nil {
		return Execution{}, errors.New("validation token is invalid")
	}
	if validation.UserID != DemoUserID || validation.Decision != "APPROVED" {
		return Execution{}, errors.New("only approved demo-user orders may be executed")
	}
	if validation.OrderID == "" || !instrumentPattern.MatchString(validation.Instrument) || validation.Quantity < 1 || validation.Quantity > maxOrderQuantity || validation.MarketPriceMinor < 1 || validation.MarketPriceMinor > validation.LimitPriceMinor || validation.LimitPriceMinor > maxUnitPriceMinor || validation.Currency != "RUB" || validation.QuoteID == "" {
		return Execution{}, errors.New("approved validation data is invalid")
	}
	if existing, err := s.trade(validation.OrderID); err == nil {
		return s.withToken(existing)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Execution{}, err
	}
	if validation.ExpiresAt < s.now() {
		return Execution{}, errors.New("validation quote has expired; validate the order again")
	}
	if validation.Quantity > math.MaxInt64/validation.MarketPriceMinor {
		return Execution{}, errors.New("order total overflows demo limits")
	}
	total := validation.Quantity * validation.MarketPriceMinor
	commission := total / 1000
	if total%1000 != 0 {
		commission++
	}
	tradeID, err := newID("trade")
	if err != nil {
		return Execution{}, err
	}
	result := Execution{OrderID: validation.OrderID, UserID: validation.UserID, TradeID: tradeID, Instrument: validation.Instrument, Quantity: validation.Quantity, ExecutionPriceMinor: validation.MarketPriceMinor, TotalMinor: total, CommissionMinor: commission, Currency: validation.Currency, Status: "FILLED", ExecutedAt: s.now()}
	tx, err := s.db.Begin()
	if err != nil {
		return Execution{}, err
	}
	defer tx.Rollback()
	var balance int64
	if err = tx.QueryRow(`SELECT balance_minor FROM accounts WHERE user_id=? AND currency=?`, DemoUserID, validation.Currency).Scan(&balance); err != nil {
		return Execution{}, err
	}
	if balance < total+commission {
		result.Status, result.Reason = "REJECTED", "Недостаточно средств на счёте"
	} else {
		if _, err = tx.Exec(`UPDATE accounts SET balance_minor=balance_minor-? WHERE user_id=? AND currency=?`, total+commission, DemoUserID, validation.Currency); err != nil {
			return Execution{}, err
		}
		var oldQuantity, oldAverage int64
		err = tx.QueryRow(`SELECT quantity,average_price_minor FROM positions WHERE user_id=? AND instrument=?`, DemoUserID, validation.Instrument).Scan(&oldQuantity, &oldAverage)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Execution{}, err
		}
		newQuantity := oldQuantity + validation.Quantity
		newAverage := (oldQuantity*oldAverage + total) / newQuantity
		if _, err = tx.Exec(`INSERT INTO positions(user_id,instrument,quantity,average_price_minor) VALUES(?,?,?,?) ON CONFLICT(user_id,instrument) DO UPDATE SET quantity=excluded.quantity,average_price_minor=excluded.average_price_minor`, DemoUserID, validation.Instrument, newQuantity, newAverage); err != nil {
			return Execution{}, err
		}
	}
	_, err = tx.Exec(`INSERT INTO trades(trade_id,order_id,user_id,instrument,quantity,execution_price_minor,total_minor,commission_minor,currency,status,reason,executed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, result.TradeID, result.OrderID, result.UserID, result.Instrument, result.Quantity, result.ExecutionPriceMinor, result.TotalMinor, result.CommissionMinor, result.Currency, result.Status, result.Reason, result.ExecutedAt)
	if err != nil {
		return Execution{}, err
	}
	if err = tx.Commit(); err != nil {
		return Execution{}, err
	}
	return s.withToken(result)
}

func (s *BrokerService) withToken(value Execution) (Execution, error) {
	token, err := executionToken(value, s.secret)
	value.ExecutionToken = token
	return value, err
}

func (s *BrokerService) trade(orderID string) (Execution, error) {
	var value Execution
	err := s.db.QueryRow(`SELECT order_id,user_id,trade_id,instrument,quantity,execution_price_minor,total_minor,commission_minor,currency,status,reason,executed_at FROM trades WHERE order_id=?`, orderID).Scan(&value.OrderID, &value.UserID, &value.TradeID, &value.Instrument, &value.Quantity, &value.ExecutionPriceMinor, &value.TotalMinor, &value.CommissionMinor, &value.Currency, &value.Status, &value.Reason, &value.ExecutedAt)
	return value, err
}

func (s *BrokerService) Account() (Account, error) {
	var value Account
	err := s.db.QueryRow(`SELECT user_id,currency,balance_minor FROM accounts WHERE user_id=?`, DemoUserID).Scan(&value.UserID, &value.Currency, &value.BalanceMinor)
	return value, err
}

func (s *BrokerService) Portfolio() (Portfolio, error) {
	rows, err := s.db.Query(`SELECT instrument,quantity,average_price_minor FROM positions WHERE user_id=? ORDER BY instrument`, DemoUserID)
	if err != nil {
		return Portfolio{}, err
	}
	defer rows.Close()
	value := Portfolio{UserID: DemoUserID, Positions: []Position{}}
	for rows.Next() {
		var position Position
		if err = rows.Scan(&position.Instrument, &position.Quantity, &position.AveragePriceMinor); err != nil {
			return Portfolio{}, err
		}
		value.Positions = append(value.Positions, position)
	}
	if err = rows.Err(); err != nil {
		return Portfolio{}, fmt.Errorf("read portfolio: %w", err)
	}
	return value, nil
}
