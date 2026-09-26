package main

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = ""

// resolveVersion prefers the ldflags value, then the module version recorded
// by `go install`, then the VCS revision, and finally "dev".
func resolveVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev != "" {
		if len(rev) > 12 {
			rev = rev[:12]
		}
		return "dev-" + rev + dirty
	}
	return "dev"
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the ecdy version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "ecdy %s\n", resolveVersion()); err != nil {
				return fmt.Errorf("write version: %w", err)
			}
			return nil
		},
	}
}
