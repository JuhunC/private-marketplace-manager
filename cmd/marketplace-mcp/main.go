package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/JuhunC/private-marketplace-manager/internal/adminapi"
	"github.com/JuhunC/private-marketplace-manager/internal/buildinfo"
	"github.com/JuhunC/private-marketplace-manager/internal/mcpadmin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func run() error {
	flags := flag.NewFlagSet("marketplace-mcp", flag.ContinueOnError)
	config := flags.String("config", "mcp-settings.json", "JSON configuration path")
	tokenFile := flags.String("token-file", "", "override API token file")
	version := flags.Bool("version", false, "print version")
	if e := flags.Parse(os.Args[1:]); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if *version {
		fmt.Println(buildinfo.Version)
		return nil
	}
	c, e := adminapi.LoadConfig(*config)
	if e != nil {
		return fmt.Errorf("load MCP configuration: %w", e)
	}
	if *tokenFile != "" {
		c.TokenFile = *tokenFile
	}
	api, e := adminapi.New(c)
	if e != nil {
		return fmt.Errorf("configure manager API: %w", e)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return mcpadmin.New(api, buildinfo.Version).Run(ctx, &mcp.StdioTransport{})
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if e := run(); e != nil {
		slog.Error("MCP server stopped", "error", e)
		os.Exit(1)
	}
}
