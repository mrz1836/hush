// Package main is the hush binary entry point. All logic lives in
// internal/cli; this file is intentionally a two-line shim.
package main

import (
	"context"
	"os"

	"github.com/mrz1836/hush/internal/cli"
)

// Build identification injected via ldflags by goreleaser / magex:
//
//	-X main.version=...   -X main.commit=...   -X main.buildDate=...
//
//nolint:gochecknoglobals // build-time injected metadata; immutable at runtime
var (
	version   string
	commit    string
	buildDate string
)

//nolint:gochecknoinits // ldflags can only target package-level vars in main; copy into cli on startup
func init() {
	if version != "" {
		cli.Version = version
	}
	if commit != "" {
		cli.Commit = commit
	}
	if buildDate != "" {
		cli.Date = buildDate
	}
}

func main() {
	os.Exit(cli.Execute(context.Background()))
}
