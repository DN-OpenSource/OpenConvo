// Command openstream is the OpenStream Chat server CLI (SPEC.md §2.2, §18):
// one binary with subcommands for the all-in-one server, individual service
// tiers, migrations, app/key management, token minting and diagnostics.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	configPath string
	version    = "0.1.0-dev"
)

func main() {
	root := &cobra.Command{
		Use:           "openstream",
		Short:         "OpenStream Chat — open-source, self-hostable chat infrastructure",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&configPath, "config", "c", "", "path to openstream.yaml")

	root.AddCommand(
		serveCmd(),
		apiCmd(),
		realtimeCmd(),
		workerCmd(),
		gatewayCmd(),
		migrateCmd(),
		appCmd(),
		tokenCmd(),
		doctorCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
