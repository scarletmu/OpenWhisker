package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/scarletmu/openwhisker/internal/core"
	"github.com/scarletmu/openwhisker/internal/storage"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) < 2 || args[0] != "ingest" || args[1] != "raw" {
		fmt.Fprintln(stderr, "usage: openwhisker ingest raw [--text TEXT] [--db data/openwhisker.db] [--vault testdata/vault]")
		return flag.ErrHelp
	}
	fs := flag.NewFlagSet("ingest raw", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target test vault root")
	text := fs.String("text", "", "raw text to ingest; stdin is used when empty")
	source := fs.String("source", "cli", "input source label")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	rawText := *text
	if strings.TrimSpace(rawText) == "" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		rawText = string(data)
	}
	if err := os.MkdirAll(*vaultRoot, 0o755); err != nil {
		return err
	}

	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	service := core.NewIngestService(store, *vaultRoot)
	result, err := service.IngestRaw(context.Background(), core.IngestRawRequest{
		Text:   rawText,
		Source: *source,
	})
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(encoded))
	return nil
}
