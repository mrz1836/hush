package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mrz1836/hush/internal/config"
)

func newServerURLCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "server-url",
		Short: "Print the hush server URL from --config",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := outputFromCmd(cmd)
			cfgPath := readGlobalFlags(cmd).configPath
			return runServerURL(cmd.Context(), out.stdout, out.stderr, cfgPath)
		},
	}
}

func runServerURL(ctx context.Context, stdout, stderr *Stream, configPath string) error {
	cfg, err := config.LoadServer(ctx, configPath)
	if err != nil {
		_ = stderr.WriteText("hush: server-url: load config %q: %v", configPath, err)
		return fmt.Errorf("hush/cli: server-url: %w", err)
	}
	return stdout.WriteText("http://%s/h/%s", cfg.Server.ListenAddr.String(), cfg.Server.PathPrefix)
}
