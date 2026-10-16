package main

import (
	"github.com/spf13/cobra"

	"github.com/tjorourke/flightbay/internal/upgrade"
)

func upgradeCmd() *cobra.Command {
	var opts upgrade.Options
	c := &cobra.Command{
		Use: "upgrade", Short: "Upgrade installed binaries from a published GitHub release",
		Long: `Download the latest published stable release, verify its SHA-256 checksums, and replace
flightbay in place. A flightbay-desk installed beside it is upgraded too. Symlinks are preserved.
Downloads are verified and staged before either binary is replaced. Requires a writable install
directory. Supports macOS and Linux. Homebrew installations should use brew upgrade flightbay.

A push to main does not publish binaries. A new version tag runs the release workflow.`,
		Example: `  flightbay upgrade --check
  flightbay upgrade
  flightbay upgrade --version v0.2.5
  flightbay upgrade --collector-only`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return upgrade.Run(cmd.Context(), version, opts, cmd.OutOrStdout())
		},
	}
	c.Flags().BoolVar(&opts.Check, "check", false, "show the installed collector version and available release without changing files")
	c.Flags().StringVar(&opts.Version, "version", "", "published stable version to install (default latest)")
	c.Flags().BoolVar(&opts.CollectorOnly, "collector-only", false, "upgrade only flightbay, leaving a sibling desk unchanged")
	return c
}
