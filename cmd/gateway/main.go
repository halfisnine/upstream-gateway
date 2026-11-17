package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/halfisnine/upstream-gateway/internal/admin"
	"github.com/halfisnine/upstream-gateway/internal/config"
	"github.com/halfisnine/upstream-gateway/internal/console"
	"github.com/halfisnine/upstream-gateway/internal/gateway"
	"github.com/halfisnine/upstream-gateway/internal/store"
)

var version = "dev"
var revision = "unknown"

func main() {
	show := flag.Bool("version", false, "print build version and exit")
	migrate := flag.Bool("migrate", false, "initialize configuration database schema and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	if *show {
		fmt.Printf("upstream-gateway %s (%s)\n", version, revision)
		return
	}
	if e := run(*migrate); e != nil {
		slog.Error("gateway stopped", "category", e.Error())
		os.Exit(1)
	}
}
func env(name, fallback string) string {
	if s := os.Getenv(name); s != "" {
		return s
	}
	return fallback
}
func secret(name string) (string, error) {
	path := os.Getenv(name)
	if path == "" {
		return "", nil
	}
	data, e := os.ReadFile(path)
	if e != nil || len(data) > 16384 {
		return "", errors.New("secret_file_unreadable")
	}
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" || strings.ContainsAny(s, "\r\n\x00") {
		return "", errors.New("invalid_secret_file")
	}
	return s, nil
}
func run(migrate bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	dsn, e := secret("GATEWAY_DATABASE_URL_FILE")
	if e != nil {
		return e
	}
	adminHandler := admin.Handler()
	servers := []*http.Server{}
	var node *gateway.Node
	var db *store.Store
	if dsn != "" {
		db, e = store.Connect(dsn)
		if e != nil {
			return e
		}
		defer db.DB.Close()
		if migrate {
			c, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if e = db.Init(c); e != nil {
				return errors.New("database_migration_failed")
			}
			return nil
		}
		keyText, e := secret("GATEWAY_MASTER_KEY_FILE")
		if e != nil {
			return e
		}
		key, e := hex.DecodeString(keyText)
		if e != nil || len(key) != 32 {
			return errors.New("master_key_must_be_32_bytes_hex")
		}
		id := os.Getenv("GATEWAY_NODE_ID")
		if !config.ValidID(id) {
			return errors.New("invalid_node_id")
		}
		token, e := secret("GATEWAY_ADMIN_TOKEN_FILE")
		if e != nil {
			return e
		}
		if token != "" && len(token) < 32 {
			return errors.New("admin_token_too_short")
		}
		node = gateway.New(db, key, id)
		defer node.Close()
		adminHandler = (&console.Console{Store: db, Key: key, Token: token, Node: node}).Handler()
		servers = append(servers, &http.Server{Addr: env("GATEWAY_LISTEN", "127.0.0.1:8080"), Handler: node.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10, BaseContext: func(net.Listener) context.Context { return context.Background() }})
	} else if migrate {
		return errors.New("database_configuration_required")
	}
	servers = append(servers, &http.Server{Addr: env("GATEWAY_ADMIN_LISTEN", "127.0.0.1:9090"), Handler: adminHandler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10})
	listeners := []net.Listener{}
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	for _, s := range servers {
		l, e := net.Listen("tcp", s.Addr)
		if e != nil {
			return errors.New("listener_start_failed")
		}
		listeners = append(listeners, l)
	}
	syncCtx, syncCancel := context.WithCancel(context.Background())
	defer syncCancel()
	syncDone := make(chan struct{})
	if node != nil {
		go func() { defer close(syncDone); node.Run(syncCtx) }()
	} else {
		close(syncDone)
	}
	results := make(chan error, len(servers))
	for i, s := range servers {
		go func(s *http.Server, l net.Listener) { results <- s.Serve(l) }(s, listeners[i])
	}
	slog.Info("gateway started", "version", version, "configured", node != nil)
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-results:
	}
	if node != nil {
		node.Drain()
	}
	syncCancel()
	<-syncDone
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan struct{}, len(servers))
	for _, s := range servers {
		go func(s *http.Server) {
			if e := s.Shutdown(shutdown); e != nil {
				_ = s.Close()
			}
			done <- struct{}{}
		}(s)
	}
	for range servers {
		<-done
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return errors.New("http_server_failed")
	}
	return nil
}
