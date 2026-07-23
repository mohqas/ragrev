package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mohqas/ragrev/internal/config"
	"github.com/mohqas/ragrev/internal/generate"
	"github.com/mohqas/ragrev/internal/gitutil"
	"github.com/mohqas/ragrev/internal/logger"
	"github.com/mohqas/ragrev/internal/output"
	"github.com/mohqas/ragrev/internal/review"
	"github.com/spf13/cobra"
)

var reviewCmd = &cobra.Command{
	Use:   "review [PR number | commit | commit1:commitN | sha1,sha2,...]",
	Short: "Perform an AI code review on a PR or local commits",
	Long: `Perform an AI code review on a PR or local commits.

If no argument is provided, reviews all unpushed commits.
If an integer is provided, reviews that GitHub PR number.
If a commit SHA is provided, reviews that single commit.
If a range is provided (commit1:commitN), reviews all commits in that range.
If comma-separated SHAs are provided, reviews those specific commits.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var mode review.ReviewMode
		var prNumber int
		var commits []string

		if len(args) == 0 {
			// Default: local mode with auto-detect unpushed commits
			mode = review.ModeLocal
			commits = []string{} // Empty means auto-detect
		} else {
			arg := args[0]

			// Try to parse as PR number (pure integer)
			if n, err := strconv.Atoi(arg); err == nil {
				mode = review.ModePR
				prNumber = n
			} else if strings.Contains(arg, ":") {
				// Range format: commit1:commitN
				mode = review.ModeLocal
				parts := strings.SplitN(arg, ":", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid range format %q, expected start:end", arg)
				}
				expanded, err := gitutil.ExpandCommitRange(parts[0], parts[1])
				if err != nil {
					return fmt.Errorf("expanding commit range: %w", err)
				}
				commits = expanded
			} else if strings.Contains(arg, ",") {
				// Comma-separated list of commits
				mode = review.ModeLocal
				parts := strings.Split(arg, ",")
				for _, p := range parts {
					commits = append(commits, strings.TrimSpace(p))
				}
			} else {
				// Single commit SHA
				mode = review.ModeLocal
				commits = []string{arg}
			}
		}

		// Validate --submit flag
		submit, _ := cmd.Flags().GetBool("submit")
		if submit && mode == review.ModeLocal {
			return fmt.Errorf("--submit is only available when reviewing a PR")
		}

		// Load config from .git/ragrev/config.yml
		cfg, dir, err := loadConfig()
		if err != nil {
			// In local mode, repo is optional
			if mode == review.ModeLocal && strings.Contains(err.Error(), "could not detect repo") {
				// Use global config as base (falls back to Default if no global file).
				cfg = config.LoadGlobalOrDefault(globalDir())
				dir, err = ragrevDir()
				if err != nil {
					return fmt.Errorf("could not find .git directory: %w", err)
				}
			} else {
				return err
			}
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
		applyBackendFlags(cmd, &cfg)

		// Resolve DB path and collection
		dbPath, _ := cmd.Flags().GetString("db-path")
		if dbPath == "" {
			dbPath = dir
		}
		collection, _ := cmd.Flags().GetString("collection")
		if collection == "" {
			if cfg.Repo != "" {
				collection = gitutil.RepoBaseName(cfg.Repo)
			} else {
				// For local mode without repo configured, derive from git dir
				if topLevel, err := gitutil.TopLevelDir(); err == nil {
					collection = filepath.Base(topLevel)
				} else {
					collection = "default"
				}
			}
		}

		useAgents, _ := cmd.Flags().GetBool("use-agents-instructions")

		scopeFlag, _ := cmd.Flags().GetString("scope")

		plainFlag, _ := cmd.Flags().GetBool("plain")
		printer := output.New(os.Stdout, cfg.OutputTheme, plainFlag)

		verbose, _ := cmd.Flags().GetBool("verbose")
		debug, _ := cmd.Flags().GetBool("debug")
		log := logger.New(os.Stdout, verbose, debug)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		// Optionally refresh the vector DB with new PR comments before reviewing.
		if updateIndex, _ := cmd.Flags().GetBool("update-index"); updateIndex {
			if cfg.Repo == "" {
				return fmt.Errorf("--update-index requires a repo; set it in config or use --repo")
			}
			log.Print("Updating review index before review...\n")
			genParams := generate.Params{
				DBPath:         dbPath,
				CollectionName: collection,
				Config:         cfg,
			}
			if err := generate.Run(ctx, genParams, log); err != nil {
				return fmt.Errorf("updating review index: %w", err)
			}
		}

		params := review.Params{
			DBPath:             dbPath,
			CollectionName:     collection,
			Mode:               mode,
			PRNumber:           prNumber,
			Commits:            commits,
			Config:             cfg,
			UseAgentsOverrides: useAgents,
			Submit:             submit,
			Scope:              review.ScopeMode(scopeFlag),
			Printer:            printer,
		}

		return review.Run(ctx, params, log)
	},
}

// knownBackends is the list of backend names that have per-backend flag shortcuts.
var knownBackends = []string{"github", "copilot", "gemini", "ollama", "openrouter"}

// backendFields describes every BackendConfig field that can be overridden via
// a flag, together with the flag suffix and a short description.
var backendFields = []struct {
	suffix string
	desc   string
	isInt  bool
}{
	{"model", "model name", false},
	{"url", "API endpoint URL", false},
	{"token-env", "environment variable holding the API token", false},
	{"token-cmd", "shell command whose stdout is used as the API token", false},
	{"max-prompt-tokens", "maximum prompt size in tokens (0 = per-model default)", true},
}

// applyBackendFlags applies all --backend.* and --backend.<name>.* flags to cfg.
// Per-backend flags also implicitly select that backend unless --backend was
// already given explicitly.
func applyBackendFlags(cmd *cobra.Command, cfg *config.Config) {
	if cfg.Backends == nil {
		cfg.Backends = make(map[string]config.BackendConfig)
	}

	backendFlagChanged := cmd.Flags().Changed("backend")

	// Per-backend flags: --backend.<name>.<field>
	// Selecting a specific backend via one of these also implicitly activates it.
	for _, name := range knownBackends {
		bc := cfg.Backends[name]
		changed := false

		for _, f := range backendFields {
			flagName := "backend." + name + "." + f.suffix
			if !cmd.Flags().Changed(flagName) {
				continue
			}
			changed = true
			if f.isInt {
				v, _ := cmd.Flags().GetInt(flagName)
				bc.MaxPromptTokens = v
			} else {
				v, _ := cmd.Flags().GetString(flagName)
				switch f.suffix {
				case "model":
					bc.Model = v
				case "url":
					bc.URL = v
				case "token-env":
					bc.TokenEnv = v
				case "token-cmd":
					bc.TokenCmd = v
				}
			}
		}

		if changed {
			cfg.Backends[name] = bc
			// Implicitly select this backend if --backend wasn't set explicitly.
			if !backendFlagChanged {
				cfg.Backend = name
				backendFlagChanged = true // first match wins
			}
		}
	}

	// Shortcut flags: --backend.<field> — applied to whichever backend is selected.
	selected := cfg.Backend
	bc := cfg.Backends[selected]
	shortcutChanged := false
	for _, f := range backendFields {
		flagName := "backend." + f.suffix
		if !cmd.Flags().Changed(flagName) {
			continue
		}
		shortcutChanged = true
		if f.isInt {
			v, _ := cmd.Flags().GetInt(flagName)
			bc.MaxPromptTokens = v
		} else {
			v, _ := cmd.Flags().GetString(flagName)
			switch f.suffix {
			case "model":
				bc.Model = v
			case "url":
				bc.URL = v
			case "token-env":
				bc.TokenEnv = v
			case "token-cmd":
				bc.TokenCmd = v
			}
		}
	}
	// Only write the entry back when a shortcut flag actually changed something.
	// Otherwise we'd materialise an empty BackendConfig for a backend name that
	// doesn't exist in the config (e.g. a typo'd --backend value), masking the
	// "backend not found" error with a confusing token-resolution failure.
	if shortcutChanged {
		cfg.Backends[selected] = bc
	}
}

func init() {
	rootCmd.AddCommand(reviewCmd)

	reviewCmd.Flags().String("db-path", "", "Path to the vector store directory (default: .git/ragrev)")
	reviewCmd.Flags().String("collection", "", "Vector store collection name (default: repo name)")
	reviewCmd.Flags().String("repo", "", "GitHub repository owner/repo (override config, required for PR reviews)")
	reviewCmd.Flags().String("reviewers", "", "Comma-separated reviewer usernames to base the review on (override config)")
	reviewCmd.Flags().Bool("use-agents-instructions", true, "Include instructions from AGENTS.md if it exists in the repo root")
	reviewCmd.Flags().String("backend", "", "LLM backend to use (e.g. github, gemini, ollama) (override config)")

	// Shortcut flags for the selected backend.
	// Use --backend.<name>.<field> variants to target a specific backend (also selects it).
	// Available fields: model, url, token-env, token-cmd, max-prompt-tokens.
	reviewCmd.Flags().String("backend.model", "", "Override `model` for the selected backend; use --backend.<name>.model to target a specific one")
	reviewCmd.Flags().String("backend.url", "", "Override API endpoint URL for the selected backend")
	reviewCmd.Flags().String("backend.token-env", "", "Override token env-var for the selected backend")
	reviewCmd.Flags().String("backend.token-cmd", "", "Override token command for the selected backend")
	reviewCmd.Flags().Int("backend.max-prompt-tokens", 0, "Override max prompt tokens for the selected backend")
	// Per-backend flags: --backend.<name>.<field> — hidden to keep help concise.
	for _, name := range knownBackends {
		for _, f := range backendFields {
			flagName := "backend." + name + "." + f.suffix
			if f.isInt {
				reviewCmd.Flags().Int(flagName, 0, "")
			} else {
				reviewCmd.Flags().String(flagName, "", "")
			}
			_ = reviewCmd.Flags().MarkHidden(flagName)
		}
	}

	reviewCmd.Flags().Bool("submit", false, "Submit the review findings as a pending PR review on GitHub (PR mode only)")
	reviewCmd.Flags().Bool("update-index", false, "Update the vector DB with new PR comments before running the review")
	reviewCmd.Flags().String("scope", "", "Review context scope: pr (default, preload entire PR), file (preload one file at a time), or chunk (only the chunk + its file per call)")
	reviewCmd.Flags().String("prompt", "", "Override the built-in review system prompt (override config). WARNING: impacts results — larger output and less accurate comment locations")
	reviewCmd.Flags().BoolP("verbose", "v", false, "Enable verbose output")
	reviewCmd.Flags().Bool("debug", false, "Enable debug output (implies -v)")
	_ = reviewCmd.Flags().MarkHidden("debug")
	reviewCmd.Flags().Bool("plain", false, "Disable colors and rich formatting (auto-enabled when stdout is not a TTY)")
}
