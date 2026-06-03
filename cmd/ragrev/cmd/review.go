package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/canonical/ragrev/internal/gitutil"
	"github.com/canonical/ragrev/internal/logger"
	"github.com/canonical/ragrev/internal/review"
	"github.com/spf13/cobra"
)

var reviewCmd = &cobra.Command{
	Use:   "review [PR number]",
	Short: "Perform an AI code review on a PR",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		prNumber, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("invalid PR number: %s", args[0])
		}

		// Load config from .git/ragrev/config.yml
		cfg, dir, err := loadConfig()
		if err != nil {
			return err
		}

		// Apply CLI flag overrides
		if repo, _ := cmd.Flags().GetString("repo"); repo != "" {
			cfg.Repo = repo
		}
		if reviewers, _ := cmd.Flags().GetString("reviewers"); reviewers != "" {
			reviewerSlice := strings.Split(reviewers, ",")
			for i := range reviewerSlice {
				reviewerSlice[i] = strings.TrimSpace(reviewerSlice[i])
			}
			cfg.Reviewers = reviewerSlice
		}
		if backend, _ := cmd.Flags().GetString("backend"); backend != "" {
			cfg.Backend = backend
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

		useAgents, _ := cmd.Flags().GetBool("use-agents-instructions")
		submit, _ := cmd.Flags().GetBool("submit")

		params := review.Params{
			DBPath:             dbPath,
			CollectionName:     collection,
			PRNumber:           prNumber,
			Config:             cfg,
			UseAgentsOverrides: useAgents,
			Submit:             submit,
		}

		verbose, _ := cmd.Flags().GetBool("verbose")
		debug, _ := cmd.Flags().GetBool("debug")
		log := logger.New(os.Stdout, verbose, debug)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		return review.Run(ctx, params, log)
	},
}

func init() {
	rootCmd.AddCommand(reviewCmd)

	reviewCmd.Flags().String("db-path", "", "Path to the vector store directory (default: .git/ragrev)")
	reviewCmd.Flags().String("collection", "", "Vector store collection name (default: repo name)")
	reviewCmd.Flags().String("repo", "", "GitHub repository owner/repo (override config)")
	reviewCmd.Flags().String("reviewers", "", "Comma-separated reviewer usernames to base the review on (override config)")
	reviewCmd.Flags().Bool("use-agents-instructions", true, "Include instructions from AGENTS.md if it exists in the repo root")
	reviewCmd.Flags().String("backend", "", "LLM backend to use (e.g. github, gemini, ollama) (override config)")
	reviewCmd.Flags().Bool("submit", false, "Submit the review findings as a pending PR review on GitHub")
	reviewCmd.Flags().BoolP("verbose", "v", false, "Enable verbose output")
	reviewCmd.Flags().Bool("debug", false, "Enable debug output (implies -v)")
}
