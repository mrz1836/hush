// Package main is the hush binary entry point. All logic lives in
// internal/cli; this file is intentionally a two-line shim.
package main

import (
	"context"
	"os"

	"github.com/mrz1836/hush/internal/cli"
)

func main() {
	os.Exit(cli.Execute(context.Background()))
}
