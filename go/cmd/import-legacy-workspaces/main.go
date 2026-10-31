package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lyeith/codexpro/go/internal/legacyworkspace"
)

func run() error {
	options := legacyworkspace.Options{}
	flag.StringVar(&options.SnapshotRoot, "snapshot-root", "", "Explicit immutable private snapshot directory")
	flag.StringVar(&options.CatalogFile, "catalog", "", "Catalog file inside the snapshot")
	flag.StringVar(&options.ManagedRoot, "managed-root", "", "Managed storage directory inside the snapshot")
	flag.StringVar(&options.Destination, "destination", "", "Separate private archive directory")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	receipt, err := legacyworkspace.Import(ctx, options)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(receipt)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
