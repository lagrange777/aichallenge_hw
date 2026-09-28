package brokerdemo

import (
	"database/sql"
	"encoding/json"
	"errors"
)

type ValidationService struct {
	db     *sql.DB
	secret string
	now    func() int64
}

func OpenValidationService(path, secret string) (*ValidationService, error) {
	if err := validateSecret(secret); err != nil {
		return nil, err
	}
	db, err := openSQLite(path, `
CREATE TABLE IF NOT EXISTS instruments(symbol TEXT PRIMARY KEY,name TEXT NOT NULL,price_minor INTEGER NOT NULL,available_quantity INTEGER NOT NULL,tradable INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS validations(id INTEGER PRIMARY KEY AUTOINCREMENT,order_id TEXT NOT NULL,created_at INTEGER NOT NULL,payload TEXT NOT NULL);
INSERT OR IGNORE INTO instruments VALUES('SBER','Сбербанк',31850,10000,1);
INSERT OR IGNORE INTO instruments VALUES('GAZP','Газпром',17240,8000,1);
INSERT OR IGNORE INTO instruments VALUES('YDEX','Яндекс',425000,500,1);
INSERT OR IGNORE INTO instruments VALUES('HALT','Приостановленный инструмент',10000,1000,0);`)
	if err != nil {
		return nil, err
	}
	return &ValidationService{db: db, secret: secret, now: unixNow}, nil
}

func (s *ValidationService) Close() error { return s.db.Close() }

func (s *ValidationService) Validate(input ValidateOrderInput) (Validation, error) {
	var order Order
	if err := verify(input.OrderToken, "order", s.secret, &order); err != nil {
		return Validation{}, errors.New("order token is invalid")
	}
	if order.ID == "" || order.UserID != DemoUserID || order.Status != "NEW" || !instrumentPattern.MatchString(order.Instrument) || order.Quantity < 1 || order.Quantity > maxOrderQuantity || order.LimitPriceMinor < 1 || order.LimitPriceMinor > maxUnitPriceMinor || order.Currency != "RUB" {
		return Validation{}, errors.New("order data is invalid")
	}
	value := Validation{OrderID: order.ID, UserID: order.UserID, Instrument: order.Instrument, Quantity: order.Quantity, LimitPriceMinor: order.LimitPriceMinor, Currency: order.Currency, QuoteID: "quote-" + order.ID, ExpiresAt: s.now() + 300}
	var available int64
	var tradable bool
	err := s.db.QueryRow(`SELECT price_minor,available_quantity,tradable FROM instruments WHERE symbol=?`, order.Instrument).Scan(&value.MarketPriceMinor, &available, &tradable)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		value.Decision, value.Reason = "REJECTED", "Инструмент не найден"
	case err != nil:
		return Validation{}, err
	case !tradable:
		value.Decision, value.Reason = "REJECTED", "Торги инструментом приостановлены"
	case value.MarketPriceMinor > order.LimitPriceMinor:
		value.Decision, value.Reason = "REJECTED", "Рыночная цена выше лимита заявки"
	case available < order.Quantity:
		value.Decision, value.Reason = "REJECTED", "Недостаточный доступный объём"
	default:
		value.Decision = "APPROVED"
	}
	token, err := validationToken(value, s.secret)
	if err != nil {
		return Validation{}, err
	}
	value.ValidationToken = token
	payloadJSON, err := json.Marshal(value)
	if err != nil {
		return Validation{}, err
	}
	payload := string(payloadJSON)
	_, err = s.db.Exec(`INSERT INTO validations(order_id,created_at,payload) VALUES(?,?,?)`, value.OrderID, s.now(), payload)
	return value, err
}
