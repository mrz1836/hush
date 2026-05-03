// Package main — test helper used by hush request unit + integration
// tests. Echoes os.Environ() to stdout, one entry per line. Compiled
// into t.TempDir() at test time so the parent's --exec path can
// verify env-injection without invoking a real shell.
package main

import (
	"fmt"
	"os"
)

func main() {
	for _, e := range os.Environ() {
		fmt.Println(e)
	}
}
