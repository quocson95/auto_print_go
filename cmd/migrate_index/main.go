package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/sondq/auto_print/internal/config"
	s3pkg "github.com/sondq/auto_print/internal/s3"
)

func main() {
	var overridesPath string
	var dryRun bool
	var inputPath string
	var outputPath string
	var remote bool

	flag.StringVar(&overridesPath, "overrides", "", "path to JSON overrides mapping legacy S3 keys or legacy titles to corrected titles")
	flag.BoolVar(&dryRun, "dry-run", false, "show what would change without rewriting index.json or copying PDFs")
	flag.StringVar(&inputPath, "input", "index.json", "path to the local index.json file")
	flag.StringVar(&outputPath, "output", "", "write migrated JSON to this path instead of updating the input file")
	flag.BoolVar(&remote, "remote", false, "migrate the remote S3 pdfs/index.json instead of a local file")
	flag.Parse()

	setupLogging()

	overrides, err := loadOverrides(overridesPath)
	if err != nil {
		slog.Error("Failed to load overrides", "path", overridesPath, "error", err)
		os.Exit(1)
	}

	if remote {
		runRemoteMigration(overrides, dryRun)
		return
	}

	runLocalMigration(inputPath, outputPath, overrides, dryRun)
}

func loadOverrides(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var overrides map[string]string
	if err := json.Unmarshal(data, &overrides); err != nil {
		return nil, err
	}

	return overrides, nil
}

func setupLogging() {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	slog.SetDefault(slog.New(handler))
}

func runLocalMigration(inputPath, outputPath string, overrides map[string]string, dryRun bool) {
	entries, err := s3pkg.ReadIndexEntriesFile(inputPath)
	if err != nil {
		slog.Error("Failed to read index file", "path", inputPath, "error", err)
		os.Exit(1)
	}

	updated, result := s3pkg.MigrateIndexEntries(entries, overrides)
	if !dryRun {
		targetPath := outputPath
		if targetPath == "" {
			targetPath = inputPath
		}

		if err := s3pkg.WriteIndexEntriesFile(targetPath, updated); err != nil {
			slog.Error("Failed to write migrated index file", "path", targetPath, "error", err)
			os.Exit(1)
		}
	}

	slog.Info("Local index migration complete",
		"input", inputPath,
		"output", chooseOutputPath(inputPath, outputPath),
		"dryRun", dryRun,
		"entries", result.TotalEntries,
		"updatedTitles", result.UpdatedTitles,
		"renamedKeys", result.RenamedKeys,
		"removedDuplicates", result.RemovedDuplicates,
	)
}

func runRemoteMigration(overrides map[string]string, dryRun bool) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	if cfg.S3BucketName == "" || cfg.AWSAccessKeyID == "" || cfg.AWSSecretAccessKey == "" {
		slog.Error("S3 configuration is required", "bucket", cfg.S3BucketName != "", "accessKey", cfg.AWSAccessKeyID != "", "secretKey", cfg.AWSSecretAccessKey != "")
		os.Exit(1)
	}

	uploader, err := s3pkg.NewUploader(cfg)
	if err != nil {
		slog.Error("Failed to create S3 uploader", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	result, err := uploader.MigrateIndexJSON(ctx, s3pkg.MigrationOptions{
		Overrides: overrides,
		DryRun:    dryRun,
	})
	if err != nil {
		slog.Error("Remote index migration failed", "error", err)
		os.Exit(1)
	}

	slog.Info("Remote index migration complete",
		"dryRun", dryRun,
		"entries", result.TotalEntries,
		"updatedTitles", result.UpdatedTitles,
		"renamedKeys", result.RenamedKeys,
		"removedDuplicates", result.RemovedDuplicates,
		"copiedObjects", result.CopiedObjects,
	)
}

func chooseOutputPath(inputPath, outputPath string) string {
	if outputPath != "" {
		return outputPath
	}
	return inputPath
}
