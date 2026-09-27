package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
)

// uiCmd opens the investigation UI. The UI lives in flightbay-desk (it links
// DuckDB, so it is a separate binary); this finds it next to flightbay or on
// PATH and hands over.
func uiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ui [BUNDLE | DIR]",
		Short: "Open the UI on a bundle (default: the newest flightbay-*.tar.zst in this directory)",
		Example: `  flightbay ui                                   # newest bundle here
  flightbay ui flightbay-DEMO-20260927T1842Z.tar.zst
  flightbay ui ~/Downloads                         # newest bundle in a folder`,
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true, // flags go to flightbay-desk open
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, a := range args {
				if a == "-h" || a == "--help" {
					return cmd.Help()
				}
			}
			desk, err := findDesk()
			if err != nil {
				return err
			}
			argv := append([]string{desk, "open"}, args...)
			if err := syscall.Exec(desk, argv, os.Environ()); err != nil {
				c := exec.Command(desk, argv[1:]...) // platforms without exec
				c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
				return c.Run()
			}
			return nil
		},
	}
}

func findDesk() (string, error) {
	name := "flightbay-desk"
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		p := filepath.Join(filepath.Dir(exe), name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", errors.New(fmt.Sprintf("%s is not installed next to flightbay or on PATH. Install it (make install, or download flightbay-desk from the release) and run flightbay ui again", name))
}
