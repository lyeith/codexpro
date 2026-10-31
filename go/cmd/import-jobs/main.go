// Command import-jobs imports a bounded private TypeScript job-history snapshot.
// It never starts an HTTP server, runner, scope helper or legacy JobManager.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/jobs"
)

func main() {
	state := flag.String("state", "", "never-used private Go state directory")
	source := flag.String("source-jobs", "", "private snapshot directory containing jobs.json")
	original := flag.String("original-jobs-dir", "", "original directory named by the snapshot records (provenance only)")
	digest := flag.String("metadata-sha256", "", "required SHA-256 of the private jobs.json")
	maximum := flag.Int64("max-bytes", 512<<20, "maximum total raw plus rendered output bytes")
	flag.Parse()
	if flag.NArg() != 0 || *state == "" || *source == "" || *original == "" || *digest == "" {
		flag.Usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	report, err := jobs.ImportLegacySnapshot(ctx, config.New(*state), jobs.LegacyImportOptions{SourceDir: *source, OriginalJobsDir: *original, ExpectedMetadataSHA256: *digest, MaxBytes: *maximum})
	if err != nil {
		fmt.Fprintln(os.Stderr, "legacy job import:", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
