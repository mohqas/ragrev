package cmd

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/canonical/ragrev/internal/config"
	"github.com/canonical/ragrev/internal/generate"
	"github.com/canonical/ragrev/internal/gitutil"
	"github.com/canonical/ragrev/internal/logger"
	"github.com/spf13/cobra"
)

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Build the review index from PR review comments",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Load config from .git/ragrev/config.yml
		cfg, dir, err := loadConfig()
		if err != nil {
			return err
		}

		// Apply CLI flag overrides
		if repo, _ := cmd.Flags().GetString("repo"); repo != "" {
			cfg.Repo = repo
		}
		if base, _ := cmd.Flags().GetString("base"); base != "" {
			cfg.BaseBranch = base
		}
		if authors, _ := cmd.Flags().GetString("authors"); authors != "" {
			authorSlice := strings.Split(authors, ",")
			for i := range authorSlice {
				authorSlice[i] = strings.TrimSpace(authorSlice[i])
			}
			cfg.Authors = authorSlice
		}
		if minLen, _ := cmd.Flags().GetInt("min-comment-len"); minLen > 0 {
			cfg.MinCommentLen = minLen
		}
		if rateDelay, _ := cmd.Flags().GetDuration("rate-delay"); rateDelay > 0 {
			cfg.RateDelay = config.Duration(rateDelay)
		}

		// Resolve DB path and collection
		dbPath, _ := cmd.Flags().GetString("db-path")
		if dbPath == "" {
			dbPath = dir
		}
		collection, _ := cmd.Flags().GetString("collection")
		if collection == "" {
			collection = gitutil.RepoBaseName(cfg.Repo)
		}

		params := generate.Params{
			DBPath:         dbPath,
			CollectionName: collection,
			Config:         cfg,
		}

		verbose, _ := cmd.Flags().GetBool("verbose")
		debug, _ := cmd.Flags().GetBool("debug")
		log := logger.New(os.Stderr, verbose, debug)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		return generate.Run(ctx, params, log)
	},
}

func init() {
	rootCmd.AddCommand(generateCmd)

	generateCmd.Flags().String("db-path", "", "Path to the vector store directory (default: .git/ragrev)")
	generateCmd.Flags().String("collection", "", "Vector store collection name (default: repo name)")
	generateCmd.Flags().String("repo", "", "GitHub repository owner/repo (override config)")
	generateCmd.Flags().String("base", "", "Target branch for PR lookups (override config)")
	generateCmd.Flags().String("authors", "", "Comma-separated trusted author usernames (override config)")
	generateCmd.Flags().Int("min-comment-len", 0, "Minimum comment body length (override config)")
	generateCmd.Flags().Duration("rate-delay", 0, "Delay between PR processing (override config)")
	generateCmd.Flags().BoolP("verbose", "v", false, "Enable verbose output")
	generateCmd.Flags().Bool("debug", false, "Enable debug output (implies -v)")
}
