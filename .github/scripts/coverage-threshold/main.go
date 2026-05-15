package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

func main() {
	cover := flag.String("cover", "cover.out", "path to a Go cover.out coverage profile")
	minProject := flag.Float64("min-project", 90.0, "minimum project-wide coverage percent (FR-013)")
	constitution := flag.String("constitution", ".specify/memory/constitution.md", "path to constitution for FR-016 byte-equality")
	flag.Parse()

	if err := verifyConstitutionList(*constitution); err != nil {
		fmt.Fprintf(os.Stderr, "::error::coverage-threshold: %v\n", err)
		os.Exit(3)
	}
	f, err := os.Open(*cover) //nolint:gosec // cover path is a CI flag, not user input.
	if err != nil {
		fmt.Fprintf(os.Stderr, "::error::coverage-threshold: open %s: %v\n", *cover, errors.Join(err, ErrMalformedCoverOut))
		os.Exit(2)
	}
	defer func() { _ = f.Close() }()
	snap, err := parseCoverOut(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "::error::coverage-threshold: %v\n", err)
		os.Exit(2)
	}
	if err := checkThresholds(snap, *minProject); err != nil {
		fmt.Fprintf(os.Stderr, "::error::coverage-threshold: %v\n", err)
		os.Exit(1)
	}
	writeReport(os.Stdout, snap, *minProject)
}
