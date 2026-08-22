// resemantic-backfill re-runs kp_semantics_extract.md over existing current
// knowledge points, overwriting their source_theme/content_theme/object/
// scope columns with a fresh extraction (rerank.ExtractPromptVersion). It
// exists to backfill points whose semantics were never populated at
// extraction time (gap/retry/coverage-fill paths, or a prompt version bump)
// without re-running the whole source's extraction pipeline (segmentation,
// dedup, KPN — none of that is touched here).
//
// Points flagged manually_edited are always skipped, never sent to the LLM —
// a human correction must not be silently discarded by a re-run.
//
// The server must be STOPPED first: this opens the bleve indexes, which
// take an exclusive lock (same requirement as cmd/dedup-report -apply).
//
// -resummarize additionally re-runs source_summary.md over each source's
// current markdown before the knowledge-point pass, overwriting
// sources.summary — knowledge-point semantics extraction reads this summary
// as background context (see docs/impl/v1/semantics-curation.md), so a
// source whose summary predates the source_summary.md 2026-08-21 change
// (300-char snippet → full document) needs its summary refreshed first for
// the knowledge-point backfill to actually benefit from it.
//
// Usage:
//
//	go run ./cmd/resemantic-backfill -config config/config.yml [-source <source_id>] [-dry-run] [-resummarize]
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
	var dryRun, resummarize bool
	flag.StringVar(&configPath, "config", "", "配置文件路径")
	flag.StringVar(&sourceID, "source", "", "只处理这一个 source_id（默认全部 completed 状态的 source）")
	flag.BoolVar(&dryRun, "dry-run", false, "只统计将处理多少个 unit，不实际调用 LLM 或写库")
	flag.BoolVar(&resummarize, "resummarize", false, "先重新生成每个 source 的摘要（sources.summary），再做知识点语义回填")
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
			points, err := unitStore.GetPointsBySourceID(src.SourceID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "source %s 查询 points 失败: %v\n", src.SourceID, err)
				continue
			}
			manual, current := 0, 0
			for _, kp := range points {
				if kp.ManuallyEdited {
					manual++
				} else if kp.SemanticsPromptVersion == rerank.ExtractPromptVersion {
					current++
				}
			}
			pending := len(points) - manual - current
			fmt.Printf("%s (%s): %d 个 current point，其中 %d 个 manually_edited 将跳过，%d 个已是最新版本，%d 个待重抽取\n",
				src.Title, src.SourceID, len(points), manual, current, pending)
			total += pending
		}
		fmt.Printf("\n共 %d 个 source，预计重抽取 %d 个 knowledge point（prompt_version 目标 %s）。\n", len(sources), total, rerank.ExtractPromptVersion)
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
	sourceSvc := source.NewService(sourceStore, nil, llmRouter, llmRouter, idxMgr.Outlines, queue.New(1), cfg, ".")

	ctx := context.Background()
	if resummarize {
		for i, src := range sources {
			if err := sourceSvc.RegenerateSummary(ctx, src.SourceID); err != nil {
				fmt.Fprintf(os.Stderr, "source %s (%s) 重新生成摘要失败: %v\n", src.Title, src.SourceID, err)
				continue
			}
			refreshed, err := sourceStore.GetByID(src.SourceID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "source %s (%s) 读回摘要失败: %v\n", src.Title, src.SourceID, err)
				continue
			}
			sources[i] = *refreshed
			fmt.Printf("%s (%s): 摘要已更新\n", src.Title, src.SourceID)
		}
		fmt.Println()
	}

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
