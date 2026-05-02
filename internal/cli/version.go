package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Build identification. GoReleaser injects production values via
// -ldflags "-X github.com/mrz1836/hush/internal/cli.Version=...".
//
//nolint:gochecknoglobals // build-time injected metadata; immutable at runtime
var (
	// Version is the semantic version of the binary, or "dev" on a
	// development build.
	Version = "dev"
	// Commit is the short commit identifier, or "unknown".
	Commit = "unknown"
	// Date is the RFC 3339 build date, or "unknown".
	Date = "unknown"
)

// versionDoc is the locked non-TTY JSON shape (FR-019a). Three keys,
// always present, in this exact order.
type versionDoc struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := outputFromCmd(cmd).stdout
			text := fmt.Sprintf("hush version %s\ncommit:  %s\nbuilt:   %s",
				Version, Commit, Date)
			doc := versionDoc{Version: Version, Commit: Commit, Date: Date}
			return out.Auto(text, doc)
		},
	}
}
