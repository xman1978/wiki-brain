// resemantic-backfill re-runs unit_semantics_extract.md over existing
// current knowledge units, overwriting their unit_rerank_semantics row with
// a fresh extraction (rerank.ExtractPromptVersion). It exists to backfill
// already-imported sources after a semantics-extraction prompt change,
// without re-running the whole source's extraction pipeline (segmentation,
// dedup, KPN — none of that is touched here).
//
// Units flagged manually_edited are always skipped, never sent to the LLM —
// a human correction must not be silently discarded by a re-run.
//
// The server must be STOPPED first: this opens the bleve indexes, which
// take an exclusive lock (same requirement as cmd/dedup-report -apply).
//
// Usage:
//
//	go run ./cmd/resemantic-backfill -config config/config.yml [-source <source_id>] [-dry-run]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	fdb "github.com/jxman78/wiki-brain/internal/foundation/db"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/llmconfig"
	"github.com/jxman78/wiki-brain/internal/rerank"
	"github.com/jxman78/wiki-brain/internal/source"
	"github.com/jxman78/wiki-brain/internal/unit"
)

func main() {
	var configPath, sourceID string
	var dryRun bool
	flag.StringVar(&configPath, "config", "", "配置文件路径")
	flag.StringVar(&sourceID, "source", "", "只处理这一个 source_id（默认全部 completed 状态的 source）")
	flag.BoolVar(&dryRun, "dry-run", false, "只统计将处理多少个 unit，不实际调用 LLM 或写库")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	database, err := fdb.Open(cfg.Database.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	sourceStore := source.NewStore(database)
	unitStore := unit.NewStore(database)

	var sources []source.Source
	if sourceID != "" {
		s, err := sourceStore.GetByID(sourceID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "source %s 不存在: %v\n", sourceID, err)
			os.Exit(1)
		}
		sources = []source.Source{*s}
	} else {
		sources, err = sourceStore.List("completed", "", "", 100000, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "列出 sources 失败: %v\n", err)
			os.Exit(1)
		}
	}

	if dryRun {
		total := 0
		for _, src := range sources {
			units, err := unitStore.GetUnitsBySourceIDFiltered(src.SourceID, unit.LifecycleCurrent)
			if err != nil {
				fmt.Fprintf(os.Stderr, "source %s 查询 units 失败: %v\n", src.SourceID, err)
				continue
			}
			manual := 0
			for _, ku := range units {
				row, err := unitStore.GetRerankSemanticsByUnitID(ku.UnitID)
				if err == nil && row != nil && row.ManuallyEdited {
					manual++
				}
			}
			fmt.Printf("%s (%s): %d 个 current unit，其中 %d 个 manually_edited 将跳过，%d 个待重抽取\n",
				src.Title, src.SourceID, len(units), manual, len(units)-manual)
			total += len(units) - manual
		}
		fmt.Printf("\n共 %d 个 source，预计重抽取 %d 个 unit（prompt_version 目标 %s）。\n", len(sources), total, rerank.ExtractPromptVersion)
		return
	}

	// Index/queue are required by unit.NewService's signature but unused by
	// RegenerateRerankSemantics (it never touches the bleve indexes or the
	// async queue) — opened anyway because NewManager's exclusive lock is
	// also our guard that the server is stopped, same as dedup-report -apply.
	idxMgr, err := index.NewManager(cfg.Index.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开索引失败（服务是否已停止？先 ./run.sh stop）: %v\n", err)
		os.Exit(1)
	}
	defer idxMgr.Close()

	llmRouter := llm.NewRoutingClient("config/prompts")
	llmConfigSvc := llmconfig.NewService(llmconfig.NewStore(database), llmRouter)
	if err := llmConfigSvc.BootstrapFromYAML(cfg.BootstrapLLM); err != nil {
		fmt.Fprintf(os.Stderr, "LLM 配置引导失败: %v\n", err)
		os.Exit(1)
	}
	if err := llmConfigSvc.ReloadRouter(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "加载 LLM 配置失败: %v\n", err)
		os.Exit(1)
	}

	unitSvc := unit.NewService(unitStore, sourceStore, llmRouter, idxMgr.Units, idxMgr.Points, queue.New(1), cfg)

	ctx := context.Background()
	var totalUpdated, totalSkipped, totalIgnored int
	for _, src := range sources {
		res, err := unitSvc.RegenerateRerankSemantics(ctx, src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "source %s (%s) 失败: %v\n", src.Title, src.SourceID, err)
			continue
		}
		if res.Updated == 0 && res.Skipped == 0 && res.Ignored == 0 {
			continue
		}
		fmt.Printf("%s (%s): 更新 %d，跳过(人工修正) %d，已是最新版本 %d，抽取遗漏 %d\n",
			src.Title, src.SourceID, res.Updated, res.Skipped, res.AlreadyCurrent, res.Ignored)
		totalUpdated += res.Updated
		totalSkipped += res.Skipped
		totalIgnored += res.Ignored
	}
	fmt.Printf("\n完成。共更新 %d 个 unit 的语义标注，跳过 %d 个人工修正过的，%d 个抽取未能生成结果。\n",
		totalUpdated, totalSkipped, totalIgnored)
}
