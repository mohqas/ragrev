package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mohqas/ragrev/internal/config"
	"github.com/mohqas/ragrev/internal/generate"
	"github.com/mohqas/ragrev/internal/gitutil"
	"github.com/mohqas/ragrev/internal/logger"
	"github.com/mohqas/ragrev/internal/review"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "generate":
		if err := runGenerate(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "review":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Error: PR number missing\n")
			printUsage()
			os.Exit(1)
		}
		if err := runReview(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "config":
		if err := runConfig(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: ragrev <command> [flags]\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  generate    Build the knowledge base from PR review comments\n")
	fmt.Fprintf(os.Stderr, "  review      Perform an AI code review on a PR\n")
	fmt.Fprintf(os.Stderr, "  config      Open the config file in your editor ($EDITOR)\n\n")
	fmt.Fprintf(os.Stderr, "Run inside a git repo with a GitHub remote.\n")
	fmt.Fprintf(os.Stderr, "Settings are saved to .git/ragrev/config.yml — edit with: ragrev config\n")
}

// ragrevDir returns the .git/ragrev path, used for both config and DB storage.
func ragrevDir() (string, error) {
	return gitutil.DBPath()
}

// loadConfig loads the config file from .git/ragrev/config.yml,
// auto-detecting repo if not already set, and saves back if it
// filled in any auto-detected values.
func loadConfig() (config.Config, string, error) {
	dir, err := ragrevDir()
	if err != nil {
		return config.Config{}, "", fmt.Errorf("could not find .git directory: %w", err)
	}

	cfg, _, err := config.EnsureExists(dir)
	if err != nil {
		return cfg, dir, err
	}

	changed := false

	// Auto-detect repo if not set in config
	if cfg.Repo == "" {
		repo, err := gitutil.DetectRepo()
		if err != nil {
			return cfg, dir, fmt.Errorf("could not detect repo (set it in config or use --repo): %w", err)
		}
		cfg.Repo = repo
		changed = true
	}

	if changed {
		_ = config.Save(dir, cfg)
	}

	return cfg, dir, nil
}

func runConfig() error {
	dir, err := ragrevDir()
	if err != nil {
		return fmt.Errorf("could not find .git directory: %w", err)
	}

	// Auto-detect and save repo on first run
	cfg, _, err := config.EnsureExists(dir)
	if err != nil {
		return err
	}
	if cfg.Repo == "" {
		if repo, err := gitutil.DetectRepo(); err == nil {
			cfg.Repo = repo
			_ = config.Save(dir, cfg)
		}
	}

	fmt.Fprintf(os.Stderr, "Opening %s\n", config.Path(dir))
	return config.Edit(dir)
}

func runGenerate(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)

	// Flags — all optional, override config file values
	var (
		flagRepo       string
		flagBase       string
		flagAuthors    string
		flagMinLen     int
		flagRateDelay  time.Duration
		flagDBPath     string
		flagCollection string
		flagVerbose    bool
		flagDebug      bool
	)

	fs.StringVar(&flagDBPath, "db-path", "", "Path to the vector store directory (default: .git/ragrev)")
	fs.StringVar(&flagCollection, "collection", "", "Vector store collection name (default: repo name)")
	fs.StringVar(&flagRepo, "repo", "", "GitHub repository owner/repo (override config)")
	fs.StringVar(&flagBase, "base", "", "Target branch for PR lookups (override config)")
	fs.StringVar(&flagAuthors, "authors", "", "Comma-separated trusted author usernames (override config)")
	fs.IntVar(&flagMinLen, "min-comment-len", 0, "Minimum comment body length (override config)")
	fs.DurationVar(&flagRateDelay, "rate-delay", 0, "Delay between PR processing (override config)")
	fs.BoolVar(&flagVerbose, "v", false, "Enable verbose output")
	fs.BoolVar(&flagDebug, "debug", false, "Enable debug output (implies -v)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Load config from .git/ragrev/config.yml
	cfg, dir, err := loadConfig()
	if err != nil {
		return err
	}

	// Apply CLI flag overrides
	if flagRepo != "" {
		cfg.Repo = flagRepo
	}
	if flagBase != "" {
		cfg.BaseBranch = flagBase
	}
	if flagAuthors != "" {
		authors := strings.Split(flagAuthors, ",")
		for i := range authors {
			authors[i] = strings.TrimSpace(authors[i])
		}
		cfg.Authors = authors
	}
	if flagMinLen > 0 {
		cfg.MinCommentLen = flagMinLen
	}
	if flagRateDelay > 0 {
		cfg.RateDelay = config.Duration(flagRateDelay)
	}

	// Resolve DB path and collection
	dbPath := flagDBPath
	if dbPath == "" {
		dbPath = dir
	}
	collection := flagCollection
	if collection == "" {
		collection = gitutil.RepoBaseName(cfg.Repo)
	}

	params := generate.Params{
		DBPath:         dbPath,
		CollectionName: collection,
		Config:         cfg,
	}

	log := logger.New(os.Stderr, flagVerbose, flagDebug)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	return generate.Run(ctx, params, log)
}

func runReview(args []string) error {
	fs := flag.NewFlagSet("review", flag.ExitOnError)

	var (
		flagRepo       string
		flagReviewers  string
		flagDBPath     string
		flagCollection string
		flagAgents     bool
		flagBackend    string
		flagSubmit     bool
		flagVerbose    bool
		flagDebug      bool
	)

	fs.StringVar(&flagDBPath, "db-path", "", "Path to the vector store directory (default: .git/ragrev)")
	fs.StringVar(&flagCollection, "collection", "", "Vector store collection name (default: repo name)")
	fs.StringVar(&flagRepo, "repo", "", "GitHub repository owner/repo (override config)")
	fs.StringVar(&flagReviewers, "reviewers", "", "Comma-separated reviewer usernames to base the review on (override config)")
	fs.BoolVar(&flagAgents, "use-agents-instructions", true, "Include instructions from AGENTS.md if it exists in the repo root")
	fs.StringVar(&flagBackend, "backend", "", "LLM backend to use (e.g. github, gemini, ollama) (override config)")
	fs.BoolVar(&flagSubmit, "submit", false, "Submit the review findings as a pending PR review on GitHub")
	fs.BoolVar(&flagVerbose, "v", false, "Enable verbose output")
	fs.BoolVar(&flagDebug, "debug", false, "Enable debug output (implies -v)")

	// Parse PR number from args[0] and flags from args[1:]
	// Handle case where flags appear before PR number or after
	if len(args) == 0 {
		return fmt.Errorf("expected PR number")
	}

	// We treat the first non-flag argument as the PR number.
	// Since flag.Parse stops at the first non-flag arg, we extract the number manually.
	var prStr string
	var parsedArgs []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && prStr == "" {
			prStr = a
		} else {
			parsedArgs = append(parsedArgs, a)
		}
	}

	if prStr == "" {
		return fmt.Errorf("PR number missing")
	}

	var prNumber int
	if _, err := fmt.Sscanf(prStr, "%d", &prNumber); err != nil {
		return fmt.Errorf("invalid PR number: %s", prStr)
	}

	if err := fs.Parse(parsedArgs); err != nil {
		return err
	}

	// Load config from .git/ragrev/config.yml
	cfg, dir, err := loadConfig()
	if err != nil {
		return err
	}

	// Apply CLI flag overrides
	if flagRepo != "" {
		cfg.Repo = flagRepo
	}
	if flagReviewers != "" {
		reviewers := strings.Split(flagReviewers, ",")
		for i := range reviewers {
			reviewers[i] = strings.TrimSpace(reviewers[i])
		}
		cfg.Reviewers = reviewers
	}
	if flagBackend != "" {
		cfg.Backend = flagBackend
	}

	// Resolve DB path and collection
	dbPath := flagDBPath
	if dbPath == "" {
		dbPath = dir
	}
	collection := flagCollection
	if collection == "" {
		collection = gitutil.RepoBaseName(cfg.Repo)
	}

	params := review.Params{
		DBPath:             dbPath,
		CollectionName:     collection,
		PRNumber:           prNumber,
		Config:             cfg,
		UseAgentsOverrides: flagAgents,
		Submit:             flagSubmit,
	}

	log := logger.New(os.Stdout, flagVerbose, flagDebug)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	return review.Run(ctx, params, log)
}
