// Package brokerdemo implements a deterministic, local-only brokerage demo
// used to exercise long agent flows across several MCP servers.
package brokerdemo

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const DemoUserID = "demo-user"

type Order struct {
	ID                  string `json:"orderId"`
	OrderToken          string `json:"orderToken,omitempty"`
	RequestID           string `json:"requestId"`
	UserID              string `json:"userId"`
	Instrument          string `json:"instrument"`
	Quantity            int64  `json:"quantity"`
	LimitPriceMinor     int64  `json:"limitPriceMinor"`
	Currency            string `json:"currency"`
	Status              string `json:"status"`
	Reason              string `json:"reason,omitempty"`
	TradeID             string `json:"tradeId,omitempty"`
	ExecutionPriceMinor int64  `json:"executionPriceMinor,omitempty"`
	TotalMinor          int64  `json:"totalMinor,omitempty"`
	CommissionMinor     int64  `json:"commissionMinor,omitempty"`
	Version             int    `json:"version"`
	CreatedAt           int64  `json:"createdAt"`
	UpdatedAt           int64  `json:"updatedAt"`
}

type CreateOrderInput struct {
	RequestID       string `json:"requestId"`
	Instrument      string `json:"instrument"`
	Quantity        int64  `json:"quantity"`
	LimitPriceMinor int64  `json:"limitPriceMinor"`
}

type ValidateOrderInput struct {
	OrderToken string `json:"orderToken"`
}

type Validation struct {
	OrderID          string `json:"orderId"`
	UserID           string `json:"userId"`
	Instrument       string `json:"instrument"`
	Quantity         int64  `json:"quantity"`
	LimitPriceMinor  int64  `json:"limitPriceMinor"`
	MarketPriceMinor int64  `json:"marketPriceMinor"`
	Currency         string `json:"currency"`
	Decision         string `json:"decision"`
	Reason           string `json:"reason,omitempty"`
	QuoteID          string `json:"quoteId"`
	ExpiresAt        int64  `json:"expiresAt"`
	ValidationToken  string `json:"validationToken"`
}

type Execution struct {
	OrderID             string `json:"orderId"`
	UserID              string `json:"userId"`
	TradeID             string `json:"tradeId"`
	Instrument          string `json:"instrument"`
	Quantity            int64  `json:"quantity"`
	ExecutionPriceMinor int64  `json:"executionPriceMinor"`
	TotalMinor          int64  `json:"totalMinor"`
	CommissionMinor     int64  `json:"commissionMinor"`
	Currency            string `json:"currency"`
	Status              string `json:"status"`
	Reason              string `json:"reason,omitempty"`
	ExecutedAt          int64  `json:"executedAt"`
	ExecutionToken      string `json:"executionToken"`
}

type Account struct {
	UserID       string `json:"userId"`
	Currency     string `json:"currency"`
	BalanceMinor int64  `json:"balanceMinor"`
}

type Position struct {
	Instrument        string `json:"instrument"`
	Quantity          int64  `json:"quantity"`
	AveragePriceMinor int64  `json:"averagePriceMinor"`
}

type Portfolio struct {
	UserID    string     `json:"userId"`
	Positions []Position `json:"positions"`
}

func newID(prefix string) (string, error) {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(value[:]), nil
}

func sign(kind string, value any, secret string) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(kind + "." + encoded))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return kind + "." + encoded + "." + signature, nil
}

func verify(token, kind, secret string, target any) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != kind {
		return errors.New("invalid signed token")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(got, want) {
		return errors.New("invalid signed token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, target) != nil {
		return errors.New("invalid signed token payload")
	}
	return nil
}

func validationToken(value Validation, secret string) (string, error) {
	value.ValidationToken = ""
	return sign("validation", value, secret)
}

func orderToken(value Order, secret string) (string, error) {
	value.OrderToken = ""
	return sign("order", value, secret)
}

func executionToken(value Execution, secret string) (string, error) {
	value.ExecutionToken = ""
	return sign("execution", value, secret)
}

func validateSecret(secret string) error {
	if len(secret) < 16 {
		return fmt.Errorf("BROKER_DEMO_SECRET must contain at least 16 characters")
	}
	return nil
}

func unixNow() int64 { return time.Now().UTC().Unix() }
