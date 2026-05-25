// Package generate orchestrates the knowledge base generation pipeline.
package generate

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mohqas/ragrev/internal/config"
	ghclient "github.com/mohqas/ragrev/internal/github"
	"github.com/mohqas/ragrev/internal/vectordb"
)

// Params holds the resolved parameters for the generate command.
// It combines config file settings with runtime overrides.
type Params struct {
	// DBPath is the directory for the vector store.
	DBPath string
	// CollectionName is the name of the chromem-go collection.
	CollectionName string
	// Config is the shared configuration.
	Config config.Config
}

// Run executes the knowledge base generation.
// Progress is written to w.
func Run(ctx context.Context, params Params, w io.Writer) error {
	cfg := params.Config

	if cfg.Repo == "" {
		return fmt.Errorf("repo is required")
	}

	// Open or create vector store (chromem-go + TF-IDF)
	store, err := vectordb.Open(params.DBPath, params.CollectionName)
	if err != nil {
		return fmt.Errorf("opening vector store: %w", err)
	}

	fmt.Fprintf(w, "Existing documents in DB: %d\n", store.Count())

	// Connect to GitHub
	client, err := ghclient.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating github client: %w", err)
	}
	if err := client.SetRepo(cfg.Repo); err != nil {
		return fmt.Errorf("setting repo: %w", err)
	}

	// nil/empty Authors means "all authors"
	var trusted map[string]bool
	if len(cfg.Authors) > 0 {
		trusted = make(map[string]bool, len(cfg.Authors))
		for _, a := range cfg.Authors {
			trusted[a] = true
		}
	}

	fmt.Fprintf(w, "Scraping merged PRs from %s (base: %s)...\n", client.RepoFullName(), cfg.BaseBranch)
	if len(trusted) > 0 {
		fmt.Fprintf(w, "Filtering by authors: %v\n", cfg.Authors)
	} else {
		fmt.Fprintf(w, "Including comments from all authors\n")
	}

	// Change to a permanent file name rather than temporary cache
	rawFile := filepath.Join(params.DBPath, "threads.jsonl")
	var historicalThreads []ghclient.Thread
	historicalIDs := make(map[string]bool)

	// Load historical data
	if data, err := os.ReadFile(rawFile); err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var t ghclient.Thread
			if err := json.Unmarshal([]byte(line), &t); err == nil {
				historicalThreads = append(historicalThreads, t)
				historicalIDs[t.ID] = true
			}
		}
		if len(historicalThreads) > 0 {
			fmt.Fprintf(w, "Loaded %d existing threads from local history file.\n", len(historicalThreads))
		}
	}

	threadsFile, err := os.OpenFile(rawFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening local history file: %w", err)
	}

	fmt.Fprintf(w, "\nStep 1: Scraping and appending NEW conversations to local history log...\n")

	var newAddedCount int
	err = client.ProcessReviewThreads(
		trusted,
		cfg.BaseBranch,
		cfg.MinCommentLen,
		historicalIDs, // pass historical IDs so we skip fetching/inserting them twice
		func(prNumber int, added int) {
			time.Sleep(time.Duration(cfg.RateDelay))
		},
		func(threads []ghclient.Thread) error {
			var batchAdded int
			for _, t := range threads {
				if !historicalIDs[t.ID] {
					historicalThreads = append(historicalThreads, t)
					historicalIDs[t.ID] = true
					newAddedCount++
					batchAdded++

					data, _ := json.Marshal(t)
					threadsFile.Write(data)
					threadsFile.WriteString("\n")
				}
			}

			if batchAdded > 0 {
				fmt.Fprintf(w, "  Appended %d new threads to local history (example URL: %s)\n", batchAdded, threads[0].PRURL)
			}
			return nil
		},
	)
	if err != nil {
		threadsFile.Close()
		return fmt.Errorf("fetching review threads: %w", err)
	}
	threadsFile.Close() // done writing new data

	if newAddedCount == 0 {
		fmt.Fprintf(w, "Nothing new to add. DB is up to date.\n")
		return nil
	}

	fmt.Fprintf(w, "\nStep 2: Rebuilding global TF-IDF vector vocabulary from ALL historical data...\n")
	// Drop old collection mathematically so lengths are refreshed from scratch
	if err := store.Reset(ctx); err != nil {
		return fmt.Errorf("resetting vector store: %w", err)
	}

	idx := store.Index()
	for _, t := range historicalThreads {
		idx.AddDocument(t.DiffHunk)
	}

	fmt.Fprintf(w, "Step 3: Vectorizing and inserting all data into freshly calibrated DB...\n")
	batchSize := 500
	totalCommitted := 0

	for i := 0; i < len(historicalThreads); i += batchSize {
		end := i + batchSize
		if end > len(historicalThreads) {
			end = len(historicalThreads)
		}
		batch := historicalThreads[i:end]

		ids := make([]string, len(batch))
		contents := make([]string, len(batch))
		metadatas := make([]map[string]string, len(batch))
		for j, t := range batch {
			ids[j] = t.ID
			contents[j] = t.DiffHunk
			metadatas[j] = map[string]string{
				"authors":  strings.Join(t.Authors(), ","),
				"comments": t.CommentsJSON(),
				"path":     t.Path,
				"url":      t.PRURL,
			}
		}

		if err := store.AddDocuments(ctx, ids, contents, metadatas); err != nil {
			return fmt.Errorf("adding documents: %w", err)
		}
		totalCommitted += len(batch)
		fmt.Fprintf(w, "  Committed %d/%d threads to Vector DB\n", totalCommitted, len(historicalThreads))
	}

	fmt.Fprintf(w, "\nSuccess! Knowledge base saved to %s\n", params.DBPath)
	fmt.Fprintf(w, "Total documents in DB: %d\n", store.Count())
	if len(cfg.Authors) > 0 {
		fmt.Fprintf(w, "The reviewer will follow the style of: %v\n", cfg.Authors)
	}

	return nil
}
