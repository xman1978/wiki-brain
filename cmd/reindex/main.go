// reindex rebuilds every source's Bleve documents (units / points / outlines)
// from their SQLite rows, then removes index documents whose DB row no longer
// exists. 一次性存量修复工具：CompleteShadowSwap 曾经只改写 SQLite 的
// source_id，重传过的 Source 在索引里仍挂着已删除的影子 source_id，Retrieval
// 的 source 过滤会永远丢弃这些命中（例如"打出租车或网约车有没有时间限制"
// 明明是全文检索第一名却查不到）。重建按文档 ID 原地覆盖，可安全重复执行。
//
// The server must be STOPPED first: bleve takes an exclusive lock.
//
// Usage:
//
//	go run ./cmd/reindex -config config/config.yml [-source <source_id>]
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"

	"github.com/blevesearch/bleve/v2"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	fdb "github.com/jxman78/wiki-brain/internal/foundation/db"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/source"
	"github.com/jxman78/wiki-brain/internal/unit"
)

func main() {
	var configPath, onlySource string
	flag.StringVar(&configPath, "config", "", "配置文件路径")
	flag.StringVar(&onlySource, "source", "", "只重建这一个 source_id（默认全部）")
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

	idxMgr, err := index.NewManager(cfg.Index.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开索引失败（服务是否已停止？先 ./run.sh stop）: %v\n", err)
		os.Exit(1)
	}
	defer idxMgr.Close()

	sourceStore := source.NewStore(db)
	unitStore := unit.NewStore(db)
	baseDir, _ := os.Getwd()

	unitSvc := unit.NewService(unitStore, sourceStore, nil, idxMgr.Units, idxMgr.Points, queue.New(1), cfg)
	sourceSvc := source.NewService(sourceStore, nil, nil, llm.StaticPurposeModels{}, idxMgr.Outlines, queue.New(1), cfg, baseDir)

	sources, err := sourceStore.List("", "", "", 100000, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "列出 source 失败: %v\n", err)
		os.Exit(1)
	}

	rebuilt := 0
	for _, src := range sources {
		if onlySource != "" && src.SourceID != onlySource {
			continue
		}
		if err := unitSvc.ReindexSource(src.SourceID); err != nil {
			fmt.Fprintf(os.Stderr, "重建 units/points 失败 %s (%s): %v\n", src.Title, src.SourceID, err)
			os.Exit(1)
		}
		if err := sourceSvc.ReindexOutlines(src.SourceID); err != nil {
			fmt.Fprintf(os.Stderr, "重建 outlines 失败 %s (%s): %v\n", src.Title, src.SourceID, err)
			os.Exit(1)
		}
		unitIDs, _ := sourceStore.GetUnitIDs(src.SourceID)
		pointIDs, _ := sourceStore.GetPointIDs(src.SourceID)
		outlineIDs, _ := sourceStore.GetOutlineIDs(src.SourceID)
		fmt.Printf("重建 %s (%s): units=%d points=%d outlines=%d\n",
			src.Title, src.SourceID, len(unitIDs), len(pointIDs), len(outlineIDs))
		rebuilt++
	}

	if onlySource == "" {
		// 清理幽灵文档：索引里存在、DB 行已不存在的文档（如更早版本遗留）。
		// 有效 ID 集直接查全表——包含进行中影子的行，因此不会误删活影子的文档。
		for _, sweep := range []struct {
			name  string
			idx   bleve.Index
			query string
		}{
			{"units", idxMgr.Units, `SELECT unit_id FROM knowledge_units`},
			{"points", idxMgr.Points, `SELECT point_id FROM knowledge_points`},
			{"outlines", idxMgr.Outlines, `SELECT outline_id FROM source_outlines`},
		} {
			removed, err := sweepGhosts(sweep.idx, db, sweep.query)
			if err != nil {
				fmt.Fprintf(os.Stderr, "清理 %s 幽灵文档失败: %v\n", sweep.name, err)
				os.Exit(1)
			}
			if removed > 0 {
				fmt.Printf("清理 %s 幽灵文档: %d\n", sweep.name, removed)
			}
		}
	}

	fmt.Printf("完成：重建 %d 个 source 的索引文档。\n", rebuilt)
}

// sweepGhosts deletes index documents whose ID no longer exists in the DB
// table behind idQuery.
func sweepGhosts(idx bleve.Index, db *sql.DB, idQuery string) (int, error) {
	valid := make(map[string]bool)
	rows, err := db.Query(idQuery)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		valid[id] = true
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	removed := 0
	batch := idx.NewBatch()
	const pageSize = 1000
	for from := 0; ; from += pageSize {
		req := bleve.NewSearchRequest(bleve.NewMatchAllQuery())
		req.Size = pageSize
		req.From = from
		res, err := idx.Search(req)
		if err != nil {
			return 0, err
		}
		for _, hit := range res.Hits {
			if !valid[hit.ID] {
				batch.Delete(hit.ID)
				removed++
			}
		}
		if len(res.Hits) < pageSize {
			break
		}
	}
	if removed > 0 {
		if err := idx.Batch(batch); err != nil {
			return 0, err
		}
	}
	return removed, nil
}
