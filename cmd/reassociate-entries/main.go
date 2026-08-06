// reassociate-entries is a one-off tool: after entries/knowledge_units.entry_id
// have been wiped and preset/domains.json reloaded, re-run the two existing
// per-source matching steps (MatchEntries, CrossSourceKPN) against every
// current source so KPs pick up entries under the new Entity x Concept fact
// classification. It does not auto-confirm the entry_candidates that
// CrossSourceKPN / MatchEntries leave pending — those still need a human via
// the existing confirm API, per docs/impl/v1/concept-evolution.md.
//
// The server must be STOPPED first (shares the same SQLite/Bleve files).
//
// Usage:
//
//	go run ./cmd/reassociate-entries -config config/config.yml
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jxman78/wiki-brain/internal/entry"
	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	fdb "github.com/jxman78/wiki-brain/internal/foundation/db"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/llmconfig"
	"github.com/jxman78/wiki-brain/internal/source"
	"github.com/jxman78/wiki-brain/internal/unit"
)

func main() {
	var configPath, presetPath, onlySource string
	flag.StringVar(&configPath, "config", "config/config.yml", "配置文件路径")
	flag.StringVar(&presetPath, "preset", "preset/domains.json", "preset domains.json 路径")
	flag.StringVar(&onlySource, "source", "", "只处理这一个 source_id（默认全部，不会重新加载 preset 之外的内容）")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	db, err := fdb.Open(cfg.Database.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	absPreset, _ := filepath.Abs(presetPath)
	if err := foundation.LoadPresetData(db, absPreset); err != nil {
		fmt.Fprintf(os.Stderr, "加载 preset 失败: %v\n", err)
		os.Exit(1)
	}
	var domainCount, entryCount int
	db.QueryRow("SELECT COUNT(*) FROM domains").Scan(&domainCount)
	db.QueryRow("SELECT COUNT(*) FROM entries").Scan(&entryCount)
	fmt.Printf("Preset loaded: %d domains, %d entries\n", domainCount, entryCount)

	idxMgr, err := index.NewManager(cfg.Index.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开索引失败（服务是否已停止？先 ./run.sh stop）: %v\n", err)
		os.Exit(1)
	}
	defer idxMgr.Close()

	promptsDir, _ := filepath.Abs("config/prompts")
	llmRouter, err := llmconfig.NewRoutingFromBootstrap(cfg.BootstrapLLM, promptsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建 LLM client 失败: %v\n", err)
		os.Exit(1)
	}

	sourceStore := source.NewStore(db)
	unitStore := unit.NewStore(db)
	unitSvc := unit.NewService(unitStore, sourceStore, llmRouter, idxMgr.Units, idxMgr.Points, nil, cfg)

	entryStore := entry.NewStore(db)
	entrySvc := entry.NewService(entryStore, entry.Config{
		AddEventMin:       cfg.Study.EntryAddEventMin,
		AddDistinctMin:    cfg.Study.EntryAddDistinctMin,
		AddOverlapMin:     cfg.Study.EntryAddOverlapMin,
		MergeCooccurMin:   cfg.Study.EntryMergeCooccurMin,
		MergeOverlapMin:   cfg.Study.EntryMergeOverlapMin,
		CandidateIdleDays: cfg.Study.EntryCandidateIdleDays,
		EventWindowDays:   cfg.Study.EntryEventWindowDays,
	}, nil)
	unitSvc.SetEntryNotifier(entrySvc)

	sources, err := sourceStore.List("", "", 100000, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "列出 source 失败: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	matched, kpnErrors, newCandidates := 0, 0, 0
	for _, src := range sources {
		if src.ShadowOf.Valid {
			continue // shadow rows are transient, never surfaced
		}
		if onlySource != "" && src.SourceID != onlySource {
			continue
		}
		if !src.DomainID.Valid || src.DomainID.String == "" {
			fmt.Printf("跳过 %s (%s): 无 domain_id\n", src.Title, src.SourceID)
			continue
		}

		unitSvc.MatchEntries(ctx, src.SourceID, src.DomainID.String)
		matched++

		result, err := unitSvc.CrossSourceKPN(ctx, src.SourceID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "CrossSourceKPN 失败 %s (%s): %v\n", src.Title, src.SourceID, err)
			kpnErrors++
			continue
		}
		if result != nil {
			newCandidates += result.EntryCandidatesTouched
		}
		fmt.Printf("处理完成 %s (%s)\n", src.Title, src.SourceID)
	}

	var pendingCount int
	db.QueryRow("SELECT COUNT(*) FROM entry_candidates WHERE status = 'pending_confirm'").Scan(&pendingCount)
	var matchedKU int
	db.QueryRow("SELECT COUNT(*) FROM knowledge_units WHERE entry_id IS NOT NULL").Scan(&matchedKU)

	fmt.Printf("\n完成：处理 %d 个 source（%d 个 CrossSourceKPN 失败）。\n", matched, kpnErrors)
	fmt.Printf("KU 已匹配到 entry：%d\n", matchedKU)
	fmt.Printf("待人工确认的 entry_candidates：%d（新产生 %d）\n", pendingCount, newCandidates)
}
