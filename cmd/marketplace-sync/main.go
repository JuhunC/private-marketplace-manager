package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/JuhunC/private-marketplace-manager/internal/buildinfo"
	"github.com/JuhunC/private-marketplace-manager/internal/syncer"
)

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: marketplace-sync discover|sync --list extensions.txt [--config sync-settings.json] [--token-file secret]")
	}
	command := os.Args[1]
	if command == "--help" || command == "-h" {
		fmt.Println("Usage: marketplace-sync discover|sync --list extensions.txt [--config sync-settings.json] [--token-file secret]\n       marketplace-sync version")
		return nil
	}
	if command == "version" || command == "--version" {
		fmt.Println(buildinfo.Version)
		return nil
	}
	if command != "discover" && command != "sync" {
		return fmt.Errorf("unknown command %q", command)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	list := flags.String("list", "extensions.txt", "extension identifier list")
	config := flags.String("config", "", "JSON configuration path")
	token := flags.String("token-file", "", "override API token file")
	if e := flags.Parse(os.Args[2:]); e != nil {
		if e == flag.ErrHelp {
			return nil
		}
		return e
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	c, e := syncer.LoadConfig(*config)
	if e != nil {
		return e
	}
	if *token != "" {
		c.TokenFile = *token
	}
	r, e := syncer.New(c)
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, e := r.Run(ctx, *list, command == "discover")
	_ = json.NewEncoder(os.Stdout).Encode(report)
	return e
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
