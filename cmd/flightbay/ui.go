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
		Use:   "ui [BUNDLE | DIR] | ui --live [--context CTX] [scope flags]",
		Short: "Open the UI: the start page with no arguments, a bundle, the newest bundle in a folder, or a live cluster with --live",
		Long: `With no arguments flightbay ui opens the start page, with three cards: live stream a cluster, open a
file (recent bundles here and in ~/Downloads are listed), or scan a repo. Flags such as --listen,
--no-browser, --workdir, --keep and -i carry over to whatever you open from it.

With a bundle it opens that bundle, with a directory the newest flightbay-*.tar.zst in it, and with
--live it mirrors a cluster read-only. --agent (or AGENT=1) opens agent mode: Traffic, the agentic
Scorecard, Resources, the Log explorer and Ambient. Everything else after ui is passed to flightbay-desk.`,
		Example: `  flightbay ui                                   # the start page
  flightbay ui .                                 # the newest bundle here
  flightbay ui flightbay-DEMO-20260927T1842Z.tar.zst
  flightbay ui ~/Downloads                         # newest bundle in a folder
  flightbay ui BUNDLE --sign flightbay-signing.key # remove objects, logs or namespaces on Review and send, export a signed copy
  flightbay ui --live --context kind-mesh1 --namespaces kagent   # mirror a cluster live, read-only
  flightbay ui --agent --live --context kind-mesh1                # agent mode: Traffic, agentic Scorecard, Resources, Logs
  AGENT=1 flightbay ui --live --context kind-mesh1                # the same`,
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
			sub := "open" // with no bundle argument, open shows the start page
			if len(args) == 0 {
				sub = "start"
			}
			var rest []string
			env := os.Environ()
			for _, a := range args {
				if a == "--live" || a == "--live=true" {
					sub = "live" // flags after it go to flightbay-desk live
					continue
				}
				if a == "--agent" || a == "--agent=true" {
					env = append(env, "FLIGHTBAY_AGENT=1") // agent mode, the same as AGENT=1
					continue
				}
				rest = append(rest, a)
			}
			if sub == "open" && len(rest) == 0 {
				sub = "start"
			}
			argv := append([]string{desk, sub}, rest...)
			if err := syscall.Exec(desk, argv, env); err != nil {
				c := exec.Command(desk, argv[1:]...) // platforms without exec
				c.Env = env
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
