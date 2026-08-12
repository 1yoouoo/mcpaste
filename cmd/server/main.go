package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/1yoouoo/mcpaste/internal/config"
	"github.com/1yoouoo/mcpaste/internal/httpserver"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "mcpaste server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadOS()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	handler := httpserver.NewRecoveryMiddleware(logger)(
		httpserver.NewAccessLogMiddleware(logger)(httpserver.NewHandler(nil)),
	)
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	defer listener.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErrors := make(chan error, 1)
	logger.Info("server listening", slog.String("address", listener.Addr().String()), slog.String("environment", string(cfg.Environment)))
	go func() {
		serverErrors <- server.Serve(listener)
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
