package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/halfisnine/upstream-gateway/internal/admin"
)

var version = "dev"
var revision = "unknown"

func main() {
	showVersion := flag.Bool("version", false, "print build version and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	if *showVersion {
		fmt.Printf("upstream-gateway %s (%s)\n", version, revision)
		return
	}
	if err := run(); err != nil {
		slog.Error("admin server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	address := os.Getenv("GATEWAY_ADMIN_LISTEN")
	if address == "" {
		address = "127.0.0.1:9090"
	}
	server := &http.Server{
		Addr:              address,
		Handler:           admin.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	slog.Info("bootstrap admin server starting; model forwarding is not implemented", "listen", address, "version", version)
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}
		err := <-result
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
