package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	fdb "github.com/jxman78/wiki-brain/internal/foundation/db"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/source"
	"github.com/jxman78/wiki-brain/internal/unit"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load("config/config.yml")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	testDir := "test"
	outDir := "test/testdata"

	if len(os.Args) > 1 {
		testDir = os.Args[1]
	}
	if len(os.Args) > 2 {
		outDir = os.Args[2]
	}

	if err := os.MkdirAll(filepath.Join(outDir, "markdown"), 0755); err != nil {
		log.Fatalf("create output dir: %v", err)
	}

	dbPath := filepath.Join(outDir, "seed.db")

	db, err := fdb.Open(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	idxPath := filepath.Join(outDir, "searchindex")
	idxMgr, err := index.NewManager(idxPath)
	if err != nil {
		log.Fatalf("create index: %v", err)
	}
	promptsDir, _ := filepath.Abs("config/prompts")
	llmClient, err := llm.NewOpenAIClient(&cfg.LLM, promptsDir)
	if err != nil {
		log.Fatalf("create LLM client: %v", err)
	}

	// Load preset domains/concepts
	presetPath, _ := filepath.Abs("preset/domains.json")
	if err := foundation.LoadPresetData(db, presetPath); err != nil {
		log.Fatalf("load preset data: %v", err)
	}
	var domainCount, conceptCount int
	db.QueryRow("SELECT COUNT(*) FROM domains").Scan(&domainCount)
	db.QueryRow("SELECT COUNT(*) FROM concepts").Scan(&conceptCount)
	fmt.Printf("Preset loaded: %d domains, %d concepts\n", domainCount, conceptCount)

	sourceStore := source.NewStore(db)
	unitStore := unit.NewStore(db)
	q := queue.New(100)

	svc := unit.NewService(unitStore, sourceStore, llmClient, idxMgr.Units, idxMgr.Points, q, cfg)

	files, err := filepath.Glob(filepath.Join(testDir, "*.md"))
	if err != nil {
		log.Fatalf("glob: %v", err)
	}

	fmt.Printf("Found %d markdown files in %s\n", len(files), testDir)
	fmt.Printf("Output: %s\n\n", outDir)

	totalStart := time.Now()

	for i, f := range files {
		fileName := filepath.Base(f)
		sourceID := strings.TrimSuffix(fileName, ".md")

		// Skip already-processed sources
		var exists int
		db.QueryRow("SELECT COUNT(*) FROM sources WHERE source_id = ?", sourceID).Scan(&exists)
		if exists > 0 {
			fmt.Printf("[%d/%d] Skipping %s (already exists)\n", i+1, len(files), fileName)
			continue
		}

		fmt.Printf("[%d/%d] Processing %s ...\n", i+1, len(files), fileName)
		fileStart := time.Now()

		mdBytes, err := os.ReadFile(f)
		if err != nil {
			fmt.Printf("  ERROR read file: %v\n", err)
			continue
		}
		content := string(mdBytes)
		mdLines := strings.Split(content, "\n")

		mdDest := filepath.Join(outDir, "markdown", fileName)
		if err := os.WriteFile(mdDest, mdBytes, 0644); err != nil {
			fmt.Printf("  ERROR copy file: %v\n", err)
			continue
		}

		absMdDest, _ := filepath.Abs(mdDest)
		_, err = db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status, word_count)
			VALUES (?, ?, 'markdown', ?, ?, ?, 'completed', ?)`,
			sourceID, strings.TrimSuffix(fileName, ".md"), fileName, f, absMdDest, source.RuneCount(content))
		if err != nil {
			fmt.Printf("  ERROR insert source: %v\n", err)
			continue
		}

		// Step 1: Structural outlines
		outlines := source.ExtractStructuralOutlines(sourceID, content)
		fmt.Printf("  structural outlines: %d\n", len(outlines))

		// Step 2: Check semantic trigger + refine if needed
		trigger := source.CheckSemanticTrigger(outlines, content, "markdown", cfg.Source.SegmentMaxChars)
		if trigger.Triggered {
			fmt.Printf("  semantic trigger: %v\n", trigger.Reasons)

			if hasOversizeLeaves(outlines, mdLines, cfg.Source.SegmentMaxChars) {
				mc := cfg.LLM.ModelForPurpose("extraction")
				newNodes, err := source.RefineLeafNodes(context.Background(), llmClient, sourceID, content, outlines, mc, cfg.Source.SegmentMaxChars)
				if err != nil {
					fmt.Printf("  WARN leaf refinement failed: %v\n", err)
				} else if len(newNodes) > 0 {
					outlines = append(outlines, newNodes...)
					fmt.Printf("  refined leaves: +%d nodes\n", len(newNodes))
				}
			}
		}

		if err := sourceStore.InsertOutlines(outlines); err != nil {
			fmt.Printf("  ERROR insert outlines: %v\n", err)
			continue
		}

		// Step 3: Generate source summary (simple fallback: use top-level outline titles)
		summary := buildSimpleSummary(outlines)
		sourceStore.UpdateSummary(sourceID, summary)

		// Step 4: Domain matching
		domainID := matchSourceDomain(context.Background(), llmClient, sourceStore, sourceID, summary, strings.TrimSuffix(fileName, ".md"))
		if domainID != "" {
			fmt.Printf("  domain: %s\n", domainID)
		}

		// Step 5: Unit extraction (includes concept matching at the end)
		err = svc.Extract(context.Background(), sourceID)
		if err != nil {
			fmt.Printf("  ERROR unit extraction: %v\n", err)
			continue
		}

		units, _ := unitStore.GetUnitsBySourceID(sourceID)
		completedCount := 0
		failedCount := 0
		totalPoints := 0
		for _, u := range units {
			if u.Status == "completed" {
				completedCount++
			} else {
				failedCount++
			}
			pts, _ := unitStore.GetPointsByUnitID(u.UnitID)
			totalPoints += len(pts)
		}

		elapsed := time.Since(fileStart)
		fmt.Printf("  result: %d KU (%d completed, %d failed), %d KP, %.1fs\n",
			len(units), completedCount, failedCount, totalPoints, elapsed.Seconds())
	}

	// Close indexes from processing phase, then EnsureHealthy rebuilds if needed
	idxMgr.Close()
	fmt.Println("\nVerifying index health...")
	idxMgr2, err := index.EnsureHealthy(idxPath, db, func(sourceID string) ([]string, error) {
		var mdPath string
		if err := db.QueryRow("SELECT markdown_path FROM sources WHERE source_id = ?", sourceID).Scan(&mdPath); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(mdPath)
		if err != nil {
			return nil, err
		}
		return strings.Split(string(data), "\n"), nil
	})
	if err != nil {
		fmt.Printf("  ERROR: %v\n", err)
	} else {
		idxMgr2.Close()
	}

	printStats(db)
	fmt.Printf("\nTotal time: %.1fs\n", time.Since(totalStart).Seconds())
	fmt.Printf("Database: %s\n", dbPath)
	fmt.Printf("Index: %s\n", idxPath)
}

func matchSourceDomain(ctx context.Context, client llm.LLMClient, store *source.Store, sourceID, summary, title string) string {
	domains, err := store.ListDomains()
	if err != nil || len(domains) == 0 {
		return ""
	}

	var domainList strings.Builder
	for _, d := range domains {
		fmt.Fprintf(&domainList, "[%s] %s：%s\n", d.DomainID, d.Name, d.Description)
	}

	output, err := client.CompleteJSON(ctx, "source_domain_match.md", map[string]string{
		"title":       title,
		"summary":     summary,
		"domain_list": domainList.String(),
	}, "extraction")
	if err != nil {
		slog.Warn("domain match failed", "source_id", sourceID, "error", err)
		return ""
	}

	var result struct {
		DomainID *string `json:"domain_id"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return ""
	}

	if result.DomainID != nil && *result.DomainID != "" {
		store.UpdateDomainID(sourceID, result.DomainID)
		return *result.DomainID
	}
	return ""
}

func hasOversizeLeaves(outlines []source.Outline, lines []string, maxChars int) bool {
	parentIDs := make(map[string]bool)
	for _, o := range outlines {
		if o.ParentID.Valid {
			parentIDs[o.ParentID.String] = true
		}
	}
	for _, o := range outlines {
		if parentIDs[o.OutlineID] {
			continue
		}
		if o.LineStart < 1 || o.LineEnd > len(lines) {
			continue
		}
		content := strings.Join(lines[o.LineStart-1:o.LineEnd], "\n")
		if source.RuneCount(content) > maxChars {
			return true
		}
	}
	return false
}

func buildSimpleSummary(outlines []source.Outline) string {
	var titles []string
	for _, o := range outlines {
		if o.Level == 1 {
			titles = append(titles, o.Title)
		}
	}
	if len(titles) == 0 {
		for _, o := range outlines {
			if !o.ParentID.Valid {
				titles = append(titles, o.Title)
			}
		}
	}
	if len(titles) > 5 {
		titles = titles[:5]
	}
	return strings.Join(titles, " ")
}

func printStats(db *sql.DB) {
	fmt.Println("\n========== Seed Summary ==========")

	var srcCount int
	db.QueryRow("SELECT COUNT(*) FROM sources").Scan(&srcCount)

	var olCount int
	db.QueryRow("SELECT COUNT(*) FROM source_outlines").Scan(&olCount)

	var kuCount, kuCompleted, kuFailed int
	db.QueryRow("SELECT COUNT(*) FROM knowledge_units").Scan(&kuCount)
	db.QueryRow("SELECT COUNT(*) FROM knowledge_units WHERE status='completed'").Scan(&kuCompleted)
	db.QueryRow("SELECT COUNT(*) FROM knowledge_units WHERE status='extraction_failed'").Scan(&kuFailed)

	var kpCount int
	db.QueryRow("SELECT COUNT(*) FROM knowledge_points").Scan(&kpCount)

	var relCount int
	db.QueryRow("SELECT COUNT(*) FROM knowledge_point_relations").Scan(&relCount)

	var domainMatched int
	db.QueryRow("SELECT COUNT(*) FROM sources WHERE domain_id IS NOT NULL").Scan(&domainMatched)

	var conceptMatched int
	db.QueryRow("SELECT COUNT(*) FROM knowledge_units WHERE concept_id IS NOT NULL").Scan(&conceptMatched)

	fmt.Printf("Sources:    %d (domain matched: %d)\n", srcCount, domainMatched)
	fmt.Printf("Outlines:   %d\n", olCount)
	fmt.Printf("KU total:   %d (completed: %d, failed: %d, concept matched: %d)\n", kuCount, kuCompleted, kuFailed, conceptMatched)
	fmt.Printf("KP total:   %d\n", kpCount)
	fmt.Printf("KPN rels:   %d\n", relCount)
}
