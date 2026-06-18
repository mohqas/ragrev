// Package generate orchestrates the review index generation pipeline.
package generate

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/canonical/ragrev/internal/config"
	ghclient "github.com/canonical/ragrev/internal/github"
	"github.com/canonical/ragrev/internal/logger"
	"github.com/canonical/ragrev/internal/vectordb"
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

// Run executes the review index generation.
// Progress is written via log.
func Run(ctx context.Context, params Params, log *logger.Logger) error {
	cfg := params.Config

	if cfg.Repo == "" {
		return fmt.Errorf("repo is required")
	}

	// Open or create vector store (chromem-go + TF-IDF)
	store, err := vectordb.Open(params.DBPath, params.CollectionName)
	if err != nil {
		return fmt.Errorf("opening vector store: %w", err)
	}

	log.Print("Existing documents in DB: %d\n", store.Count())

	// Connect to GitHub
	client, err := ghclient.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating github client: %w", err)
	}
	client.SetLogger(log)
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

	log.Print("Scraping merged PRs from %s (base: %s)...\n", client.RepoFullName(), cfg.BaseBranch)
	if len(trusted) > 0 {
		log.Print("Filtering by authors: %v\n", cfg.Authors)
	} else {
		log.Print("Including comments from all authors\n")
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
			log.Verbose("Loaded %d existing threads from local history file.\n", len(historicalThreads))
		}
	}

	threadsFile, err := os.OpenFile(rawFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening local history file: %w", err)
	}

	// Load PR-level scan state: which PRs were already processed (to skip
	// cheaply) and the watermark from the last completed run (to stop early).
	scannedPRs, err := loadScannedPRs(params.DBPath)
	if err != nil {
		return fmt.Errorf("loading scanned PRs: %w", err)
	}
	watermark := loadWatermark(params.DBPath)
	if len(scannedPRs) > 0 {
		log.Verbose("Loaded %d previously scanned PRs (watermark: %s).\n",
			len(scannedPRs), formatWatermark(watermark))
	}

	scannedWriter, err := newScannedPRWriter(params.DBPath)
	if err != nil {
		threadsFile.Close()
		return err
	}

	log.Print("\nStep 1: Scraping and appending NEW conversations to local history log...\n")

	var newAddedCount int

	newest, err := client.ProcessReviewThreads(
		trusted,
		cfg.BaseBranch,
		cfg.MinCommentLen,
		historicalIDs, // pass historical IDs so we skip fetching/inserting them twice
		scannedPRs,    // PRs already processed in prior runs
		watermark,     // stop once we reach history covered by a completed run
		func(prNumber int, added int, updatedAt time.Time) error {
			if added == 0 {
				log.Verbose("  PR #%d processed (no new comments)\n", prNumber)
			}
			// Record this PR as scanned so future runs skip it.
			if err := scannedWriter.record(prNumber, updatedAt); err != nil {
				return fmt.Errorf("recording scanned PR #%d: %w", prNumber, err)
			}
			scannedPRs[prNumber] = updatedAt

			time.Sleep(time.Duration(cfg.RateDelay))
			return nil
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
				log.Print("  Appended %d new threads to local history (example URL: %s)\n", batchAdded, threads[0].PRURL)
			}
			return nil
		},
	)
	threadsFile.Close()   // done writing new data
	scannedWriter.Close() // done recording scanned PRs
	if err != nil {
		return fmt.Errorf("fetching review threads: %w", err)
	}

	// Persist the new watermark only after a successful run so interrupted
	// runs don't advance the stop boundary past unscanned history.
	if newest.After(watermark) {
		if err := saveWatermark(params.DBPath, newest); err != nil {
			log.Verbose("warning: could not save scan watermark: %v\n", err)
		}
	}

	// Record that initialization completed successfully for this DB directory,
	// even when there are no PRs/comments to index. This lets `ragrev review`
	// proceed with a generic review instead of demanding `ragrev init`.
	if err := vectordb.MarkInitialized(params.DBPath); err != nil {
		log.Verbose("warning: could not write init marker: %v\n", err)
	}

	if newAddedCount == 0 {
		log.Print("Nothing new to add. DB is up to date.\n")
		return nil
	}

	log.Print("\nStep 2: Rebuilding global TF-IDF vector vocabulary from ALL historical data...\n")
	// Drop old collection mathematically so lengths are refreshed from scratch
	if err := store.Reset(ctx); err != nil {
		return fmt.Errorf("resetting vector store: %w", err)
	}

	idx := store.Index()
	for _, t := range historicalThreads {
		idx.AddDocument(t.DiffHunk)
	}

	log.Print("Step 3: Vectorizing and inserting all data into freshly calibrated DB...\n")
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
		log.Verbose("  Committed %d/%d threads to Vector DB\n", totalCommitted, len(historicalThreads))
	}

	log.Print("\nSuccess! Knowledge base saved to %s\n", params.DBPath)
	log.Print("Total documents in DB: %d\n", store.Count())
	if len(cfg.Authors) > 0 {
		log.Print("The reviewer will follow the style of: %v\n", cfg.Authors)
	}

	return nil
}
