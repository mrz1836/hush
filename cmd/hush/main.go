// Package main is the hush binary entry point. The body is two lines
// per docs/PACKAGE-MAP.md — all logic lives in internal/cli.
package main

import (
	"context"
	"os"

	"github.com/mrz1836/hush/internal/cli"
)

func main() {
	os.Exit(cli.Execute(context.Background()))
}
