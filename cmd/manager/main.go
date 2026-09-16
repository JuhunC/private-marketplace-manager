package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/JuhunC/private-marketplace-manager/internal/buildinfo"
	"github.com/JuhunC/private-marketplace-manager/internal/server"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func secret(k string) (string, error) {
	filename := os.Getenv(k + "_FILE")
	if filename != "" {
		b, e := os.ReadFile(filename)
		return strings.TrimSpace(string(b)), e
	}
	return os.Getenv(k), nil
}
func run() error {
	version := flag.Bool("version", false, "print version")
	health := flag.Bool("healthcheck", false, "check local manager readiness")
	flag.Parse()
	if *version {
		fmt.Println(buildinfo.Version)
		return nil
	}
	addr := env("LISTEN_ADDR", ":8080")
	if *health {
		client := http.Client{Timeout: 5 * time.Second}
		r, e := client.Get("http://127.0.0.1:8080/health/ready")
		if e != nil {
			return e
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			return fmt.Errorf("not ready: %d", r.StatusCode)
		}
		return nil
	}
	token, e := secret("API_TOKEN")
	if e != nil {
		return e
	}
	password, e := secret("ADMIN_PASSWORD")
	if e != nil {
		return e
	}
	max, e := strconv.ParseInt(env("MAX_UPLOAD_BYTES", "2147483648"), 10, 64)
	if e != nil || max <= 0 {
		return fmt.Errorf("invalid MAX_UPLOAD_BYTES")
	}
	s, e := server.New(server.Config{Extensions: env("EXTENSIONS_DIR", "/data/extensions"), State: env("STATE_DIR", "/data/state"), Token: token, Password: password, PublicURL: env("PUBLIC_URL", "http://localhost:8080"), MaxUpload: max})
	if e != nil {
		return e
	}
	defer s.Close()
	h := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Minute, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		h.Shutdown(c)
	}()
	slog.Info("manager ready", "address", addr, "version", buildinfo.Version)
	e = h.ListenAndServe()
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if e := run(); e != nil {
		slog.Error("manager stopped", "error", e)
		os.Exit(1)
	}
}
