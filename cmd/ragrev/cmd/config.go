package cmd

import (
	"fmt"
	"os"

	"github.com/canonical/ragrev/internal/config"
	"github.com/canonical/ragrev/internal/gitutil"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Open the config file in your editor ($EDITOR)",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := ragrevDir()
		if err != nil {
			return fmt.Errorf("could not find .git directory: %w", err)
		}

		// Load existing config, merge new defaults, and re-save.
		// Unknown / removed fields are surfaced as warnings.
		cfg, warnings, err := config.Update(dir)
		if err != nil {
			return err
		}
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}

		// Auto-detect and save repo on first run.
		if cfg.Repo == "" {
			if repo, err := gitutil.DetectRepo(); err == nil {
				cfg.Repo = repo
				_ = config.Save(dir, cfg)
			}
		}

		fmt.Fprintf(os.Stderr, "Opening %s\n", config.Path(dir))
		return config.Edit(dir)
	},
}

func init() {
	rootCmd.AddCommand(configCmd)
}
