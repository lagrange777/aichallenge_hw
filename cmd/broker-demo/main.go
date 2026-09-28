package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"codex-chat-cli/internal/brokerdemo"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	role := env("BROKER_DEMO_ROLE", "orders")
	addr := env("LISTEN_ADDR", "127.0.0.1:8091")
	if _, port, err := net.SplitHostPort(addr); err != nil || port == "" || port == "0" {
		return errors.New("LISTEN_ADDR must include a nonzero port")
	}
	server, closeStore, err := brokerdemo.Open(role, env("BROKER_DEMO_DB", "data/broker-"+role+".db"), env("BROKER_DEMO_SECRET", "local-demo-secret-change-me"))
	if err != nil {
		return err
	}
	defer closeStore()
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	httpServer := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() {
		log.Printf("Broker demo %s MCP listening on %s", role, addr)
		done <- httpServer.ListenAndServe()
	}()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}
