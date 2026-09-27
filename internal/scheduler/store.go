// Package scheduler runs durable, project-wide MCP tasks independently of browser sessions.
package scheduler

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("Задание изменилось. Обновите список")

type Schedule struct {
	Kind            string `json:"kind"`
	IntervalSeconds int    `json:"intervalSeconds,omitempty"`
	At              int64  `json:"at,omitempty"`
	Time            string `json:"time,omitempty"`
	Timezone        string `json:"timezone,omitempty"`
}
type Spec struct {
	Processor      string         `json:"processor,omitempty"`
	ToolName       string         `json:"toolName,omitempty"`
	Arguments      map[string]any `json:"arguments,omitempty"`
	Schedule       Schedule       `json:"schedule,omitempty"`
	SummaryMode    string         `json:"summaryMode,omitempty"`
	SummaryPrompt  string         `json:"summaryPrompt,omitempty"`
	Name           string         `json:"name"`
	ConnectionID   string         `json:"connectionId"`
	Tickers        []string       `json:"tickers,omitempty"`
	CollectSeconds int            `json:"collectSeconds,omitempty"`
	SummarySeconds int            `json:"summarySeconds,omitempty"`
	RequestID      string         `json:"requestId,omitempty"`
}
type Job struct {
	ID string `json:"id"`
	Spec
	Version           int    `json:"version"`
	ConnectionVersion int    `json:"connectionVersion"`
	SourceName        string `json:"sourceName"`
	Created           int64  `json:"created"`
	Paused            bool   `json:"paused"`
	NextCollect       int64  `json:"nextCollect"`
	NextSummary       int64  `json:"nextSummary"`
	SummaryFrom       int64  `json:"summaryFrom"`
	LastRun           int64  `json:"lastRun"`
	LastError         string `json:"lastError,omitempty"`
	Failures          int    `json:"failures"`
	Completed         bool   `json:"completed"`
	RunNow            bool   `json:"runNow,omitempty"`
	Running           bool   `json:"running"`
}
type Quote struct {
	Ticker        string  `json:"ticker"`
	Price         float64 `json:"price"`
	SourceTime    string  `json:"sourceTime"`
	TradingStatus string  `json:"tradingStatus"`
	Observed      int64   `json:"observed"`
}
type Stat struct {
	Ticker        string  `json:"ticker"`
	Count         int     `json:"count"`
	First         float64 `json:"first"`
	Last          float64 `json:"last"`
	Min           float64 `json:"min"`
	Max           float64 `json:"max"`
	ChangePercent float64 `json:"changePercent"`
	SourceTime    string  `json:"sourceTime"`
	TradingStatus string  `json:"tradingStatus"`
}
type Summary struct {
	ID      string   `json:"id"`
	JobID   string   `json:"jobId"`
	From    int64    `json:"from"`
	To      int64    `json:"to"`
	Stats   []Stat   `json:"stats"`
	Missing []string `json:"missing"`
	Text    string   `json:"text"`
	Warning string   `json:"warning,omitempty"`
	Model   string   `json:"model,omitempty"`
	Tokens  int      `json:"tokens"`
}
type Run struct {
	Result   json.RawMessage `json:"result,omitempty"`
	Executed bool            `json:"executed,omitempty"`
	At       int64           `json:"at"`
	Samples  int             `json:"samples"`
	Error    string          `json:"error,omitempty"`
	Summary  bool            `json:"summary"`
}
type Store struct{ db *sql.DB }

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY, request_id TEXT UNIQUE NOT NULL, payload TEXT NOT NULL, paused INTEGER NOT NULL, due INTEGER NOT NULL, lease TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS samples(job_id TEXT NOT NULL, ticker TEXT NOT NULL, source_time TEXT NOT NULL, observed INTEGER NOT NULL, payload TEXT NOT NULL, PRIMARY KEY(job_id,ticker,source_time));
 CREATE INDEX IF NOT EXISTS samples_window ON samples(job_id,observed);
 CREATE TABLE IF NOT EXISTS summaries(id TEXT PRIMARY KEY,job_id TEXT NOT NULL,created INTEGER NOT NULL,payload TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS runs(id INTEGER PRIMARY KEY,job_id TEXT NOT NULL,created INTEGER NOT NULL,payload TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value INTEGER NOT NULL);
 INSERT OR IGNORE INTO settings VALUES('allow_chat_changes',0);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) AllowChat() (bool, error) {
	var v int
	err := s.db.QueryRow("SELECT value FROM settings WHERE key='allow_chat_changes'").Scan(&v)
	return v == 1, err
}
func (s *Store) SetAllowChat(v bool) error {
	_, err := s.db.Exec("UPDATE settings SET value=? WHERE key='allow_chat_changes'", v)
	return err
}
func (s *Store) List() ([]Job, error) {
	rows, err := s.db.Query("SELECT payload,lease_until FROM jobs ORDER BY rowid DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var data string
		var lease int64
		if err = rows.Scan(&data, &lease); err != nil {
			return nil, err
		}
		var j Job
		if err = json.Unmarshal([]byte(data), &j); err != nil {
			return nil, err
		}
		j.Running = lease > time.Now().Unix()
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s *Store) Get(id string) (Job, error) {
	var data string
	err := s.db.QueryRow("SELECT payload FROM jobs WHERE id=?", id).Scan(&data)
	if err != nil {
		return Job{}, err
	}
	var j Job
	err = json.Unmarshal([]byte(data), &j)
	return j, err
}
func (s *Store) Create(j Job) (Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRow("SELECT payload FROM jobs WHERE request_id=?", j.RequestID).Scan(&existing)
	if err == nil {
		var old Job
		err = json.Unmarshal([]byte(existing), &old)
		if err != nil {
			return Job{}, err
		}
		a, _ := json.Marshal(old.Spec)
		b, _ := json.Marshal(j.Spec)
		if string(a) != string(b) {
			return Job{}, errors.New("requestId уже использован для другого задания")
		}
		return old, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Job{}, err
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM jobs").Scan(&count); err != nil {
		return Job{}, err
	}
	if count >= 20 {
		return Job{}, errors.New("Лимит: 20 заданий")
	}
	b, err := json.Marshal(j)
	if err != nil {
		return Job{}, err
	}
	_, err = tx.Exec("INSERT INTO jobs(id,request_id,payload,paused,due) VALUES(?,?,?,0,?)", j.ID, j.RequestID, string(b), due(j))
	if err != nil {
		return Job{}, err
	}
	return j, tx.Commit()
}
func (s *Store) Pause(id string, version int, paused bool, now int64) (Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var data string
	if err = tx.QueryRow("SELECT payload FROM jobs WHERE id=?", id).Scan(&data); err != nil {
		return Job{}, err
	}
	var j Job
	if err = json.Unmarshal([]byte(data), &j); err != nil {
		return Job{}, err
	}
	if version != j.Version {
		return Job{}, ErrConflict
	}
	j.Version++
	j.Paused = paused
	j.Running = false
	j.RunNow = false
	if !paused {
		if j.Completed {
			return Job{}, errors.New("Задание завершено; измените расписание для повторного выполнения")
		}
		if j.ToolName != "" {
			j.NextCollect = firstRun(j.Schedule, time.Unix(now, 0))
			if j.Schedule.Kind == "once" && j.NextCollect <= now {
				j.NextCollect = now
			}
		} else {
			j.NextCollect = now
		}
		j.NextSummary = now + int64(j.SummarySeconds)
		j.SummaryFrom = now
		j.Failures = 0
		j.LastError = ""
	}
	b, _ := json.Marshal(j)
	_, err = tx.Exec("UPDATE jobs SET payload=?,paused=?,due=?,lease='',lease_until=0 WHERE id=?", string(b), paused, due(j), id)
	if err != nil {
		return Job{}, err
	}
	return j, tx.Commit()
}
func (s *Store) claim(now int64) (Job, string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Job{}, "", err
	}
	defer tx.Rollback()
	var data string
	err = tx.QueryRow("SELECT payload FROM jobs WHERE paused=0 AND due<=? AND lease_until<=? ORDER BY due,id LIMIT 1", now, now).Scan(&data)
	if err != nil {
		return Job{}, "", err
	}
	var j Job
	if err = json.Unmarshal([]byte(data), &j); err != nil {
		return Job{}, "", err
	}
	token := newID()
	result, err := tx.Exec("UPDATE jobs SET lease=?,lease_until=? WHERE id=? AND lease_until<=? AND paused=0", token, now+300, j.ID, now)
	if err != nil {
		return Job{}, "", err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return Job{}, "", ErrConflict
	}
	return j, token, tx.Commit()
}
func (s *Store) quotes(id string, from, to int64) ([]Quote, error) {
	rows, err := s.db.Query("SELECT payload FROM samples WHERE job_id=? AND observed>=? AND observed<=? ORDER BY observed,source_time", id, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var quotes []Quote
	for rows.Next() {
		var data string
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var q Quote
		if err = json.Unmarshal([]byte(data), &q); err != nil {
			return nil, err
		}
		quotes = append(quotes, q)
	}
	return quotes, rows.Err()
}
func (s *Store) finish(j Job, token string, quotes []Quote, summary *Summary, run Run) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, _ := json.Marshal(j)
	res, err := tx.Exec("UPDATE jobs SET payload=?,due=?,lease='',lease_until=0 WHERE id=? AND lease=? AND paused=0", string(b), due(j), j.ID, token)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	for _, q := range quotes {
		b, e := json.Marshal(q)
		if e != nil {
			return e
		}
		if _, err = tx.Exec("INSERT OR IGNORE INTO samples VALUES(?,?,?,?,?)", j.ID, q.Ticker, q.SourceTime, q.Observed, string(b)); err != nil {
			return err
		}
	}
	if summary != nil {
		b, e := json.Marshal(summary)
		if e != nil {
			return e
		}
		if _, err = tx.Exec("INSERT INTO summaries VALUES(?,?,?,?)", summary.ID, j.ID, summary.To, string(b)); err != nil {
			return err
		}
	}
	b, _ = json.Marshal(run)
	if _, err = tx.Exec("INSERT INTO runs(job_id,created,payload) VALUES(?,?,?)", j.ID, run.At, string(b)); err != nil {
		return err
	}
	// Retention is bounded per job; samples cover at least one 24-hour period at the minimum interval (10 tickers).
	for _, query := range []string{
		"DELETE FROM samples WHERE job_id=? AND rowid NOT IN (SELECT rowid FROM samples WHERE job_id=? ORDER BY observed DESC,rowid DESC LIMIT 30000)",
		"DELETE FROM summaries WHERE job_id=? AND id NOT IN (SELECT id FROM summaries WHERE job_id=? ORDER BY created DESC LIMIT 100)",
		"DELETE FROM runs WHERE job_id=? AND id NOT IN (SELECT id FROM runs WHERE job_id=? ORDER BY id DESC LIMIT 1500)",
	} {
		if _, err = tx.Exec(query, j.ID, j.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) Summaries(id string) ([]Summary, error) {
	var out []Summary
	err := s.readHistory("summaries", id, func(b []byte) error {
		var v Summary
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		out = append(out, v)
		return nil
	})
	if out == nil {
		out = []Summary{}
	}
	return out, err
}
func (s *Store) Runs(id string) ([]Run, error) {
	var out []Run
	err := s.readHistory("runs", id, func(b []byte) error {
		var v Run
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		out = append(out, v)
		return nil
	})
	if out == nil {
		out = []Run{}
	}
	return out, err
}
func (s *Store) readHistory(table, id string, consume func([]byte) error) error {
	if table != "runs" && table != "summaries" {
		return fmt.Errorf("unknown history")
	}
	rows, err := s.db.Query("SELECT payload FROM "+table+" WHERE job_id=? ORDER BY created DESC,rowid DESC LIMIT 100", id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		if err = rows.Scan(&data); err != nil {
			return err
		}
		if err = consume([]byte(data)); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) windowRuns(id string, from, to int64) ([]Run, error) {
	rows, err := s.db.Query("SELECT payload FROM runs WHERE job_id=? AND created>=? AND created<=? ORDER BY created,id", id, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var b string
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		var r Run
		if err = json.Unmarshal([]byte(b), &r); err != nil {
			return nil, err
		}
		if r.Executed {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}
func (s *Store) replace(id string, version int, j Job) (Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var data string
	if err = tx.QueryRow("SELECT payload FROM jobs WHERE id=?", id).Scan(&data); err != nil {
		return Job{}, err
	}
	var old Job
	if err = json.Unmarshal([]byte(data), &old); err != nil {
		return Job{}, err
	}
	if old.Version != version {
		return Job{}, ErrConflict
	}
	j.ID = id
	j.Created = old.Created
	j.LastRun = old.LastRun
	j.Version = old.Version + 1
	j.Paused = old.Paused
	j.RequestID = old.RequestID
	b, _ := json.Marshal(j)
	_, err = tx.Exec("UPDATE jobs SET payload=?,due=?,lease='',lease_until=0 WHERE id=?", string(b), due(j), id)
	if err != nil {
		return Job{}, err
	}
	return j, tx.Commit()
}
func (s *Store) RunOnce(id string, version int) (Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var data string
	var lease int64
	if err = tx.QueryRow("SELECT payload,lease_until FROM jobs WHERE id=?", id).Scan(&data, &lease); err != nil {
		return Job{}, err
	}
	var j Job
	if err = json.Unmarshal([]byte(data), &j); err != nil {
		return Job{}, err
	}
	if j.Version != version || lease > time.Now().Unix() {
		return Job{}, ErrConflict
	}
	if j.Paused || j.Completed {
		return Job{}, errors.New("Сначала возобновите задание или измените завершённое расписание")
	}
	if j.ToolName == "" {
		j.NextCollect = time.Now().Unix()
	} else {
		j.RunNow = true
	}
	j.Version++
	b, _ := json.Marshal(j)
	_, err = tx.Exec("UPDATE jobs SET payload=?,due=? WHERE id=?", string(b), time.Now().Unix(), id)
	if err != nil {
		return Job{}, err
	}
	return j, tx.Commit()
}
func (s *Store) Delete(id string, version int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var data string
	if err = tx.QueryRow("SELECT payload FROM jobs WHERE id=?", id).Scan(&data); err != nil {
		return err
	}
	var j Job
	if err = json.Unmarshal([]byte(data), &j); err != nil {
		return err
	}
	if j.Version != version {
		return ErrConflict
	}
	for _, table := range []string{"samples", "summaries", "runs"} {
		if _, err = tx.Exec("DELETE FROM "+table+" WHERE job_id=?", id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("DELETE FROM jobs WHERE id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}
