package cmd

import (
	"fmt"
	"os"

	"github.com/mohqas/ragrev/internal/config"
	"github.com/mohqas/ragrev/internal/gitutil"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Open the config file in your editor ($EDITOR)",
	Long: `Open the ragrev configuration file in your editor ($EDITOR).

When run inside a git repository the per-repo config (.git/ragrev/config.yml)
is opened.  When run outside a repository, or when --global is passed, the
global (per-user) config is opened instead.

The global config lives at:
  - $SNAP_USER_COMMON/config.yml   (under the snap, no extra permissions needed)
  - $XDG_CONFIG_HOME/ragrev/config.yml  (~/.config/ragrev/config.yml elsewhere)

Values in the global config seed newly created per-repo configs.  The fields
repo, base_branch, and reviewers are repo-specific and are not stored globally.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		useGlobal, _ := cmd.Flags().GetBool("global")

		// Determine whether we are inside a git repo.
		dir, dirErr := ragrevDir()

		// Use global config when: --global is set, or we are outside a repo.
		if useGlobal || dirErr != nil {
			gDir := globalDir()
			fmt.Fprintf(os.Stderr, "Opening %s\n", config.GlobalPath(gDir))
			return config.EditGlobal(gDir)
		}

		// Inside a repo: edit per-repo config.
		cfg, warnings, err := config.UpdateSeeded(dir, globalDir())
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
	configCmd.Flags().Bool("global", false, "Edit the global (per-user) config instead of the per-repo config")
}
