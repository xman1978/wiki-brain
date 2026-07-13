// dedup-report scans knowledge units already in the database and reports
// duplicate-candidate pairs per source, using the same multi-path recall
// (unit.CandidatePairs) the extraction pipeline's document-level dedup uses.
// The default mode is read-only and LLM-free: it exists to (1) build the
// human-annotated evaluation baseline (label each reported pair
// duplicate/related/distinct) and (2) audit imported data before/after
// 存量治理.
//
// -apply <pairs-file> switches to write mode (存量治理执行): the file lists
// human-CONFIRMED duplicate pairs, one per line, as two unit ids separated
// by whitespace ('#' starts a comment). Pairs are clustered (union-find, so
// A B + B C merges all three), the earliest-starting unit of each cluster
// survives, and the rest are merged into it via unit.ApplyOfflineMerge —
// unique points reparented, losers marked superseded, nothing hard-deleted.
// The server must be STOPPED first: apply opens the bleve indexes, which
// take an exclusive lock.
//
// Usage:
//
//	go run ./cmd/dedup-report -config config/config.yml [-source <source_id>] [-json]
//	go run ./cmd/dedup-report -config config/config.yml -apply confirmed_pairs.txt
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jxman78/wiki-brain/internal/foundation/config"
	fdb "github.com/jxman78/wiki-brain/internal/foundation/db"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/queue"
	"github.com/jxman78/wiki-brain/internal/source"
	"github.com/jxman78/wiki-brain/internal/unit"
)

type sourceReport struct {
	SourceID       string               `json:"source_id"`
	Title          string               `json:"title"`
	UnitCount      int                  `json:"unit_count"`
	ShortUnitCount int                  `json:"short_unit_count"` // spans <= 2 lines
	PairsByReason  map[string]int       `json:"pairs_by_reason"`
	Candidates     []unit.CandidatePair `json:"candidates"`
}

func main() {
	var configPath, sourceID, applyFile string
	var asJSON bool
	flag.StringVar(&configPath, "config", "", "配置文件路径")
	flag.StringVar(&sourceID, "source", "", "只报告这一个 source_id（默认全部）")
	flag.BoolVar(&asJSON, "json", false, "输出 JSON 而不是 Markdown")
	flag.StringVar(&applyFile, "apply", "", "人工确认的重复对文件（每行两个 unit_id），执行合并；需先停止服务")
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

	sourceStore := source.NewStore(db)
	unitStore := unit.NewStore(db)

	if applyFile != "" {
		if err := applyConfirmedPairs(cfg, sourceStore, unitStore, applyFile); err != nil {
			fmt.Fprintf(os.Stderr, "执行合并失败: %v\n", err)
			os.Exit(1)
		}
		return
	}

	var sources []source.Source
	if sourceID != "" {
		s, err := sourceStore.GetByID(sourceID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "source %s 不存在: %v\n", sourceID, err)
			os.Exit(1)
		}
		sources = []source.Source{*s}
	} else {
		sources, err = sourceStore.List("completed", "", 1000, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "列出 sources 失败: %v\n", err)
			os.Exit(1)
		}
	}

	var reports []sourceReport
	for _, src := range sources {
		r, err := buildReport(unitStore, src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "source %s 报告失败: %v\n", src.SourceID, err)
			continue
		}
		reports = append(reports, r)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(reports); err != nil {
			fmt.Fprintf(os.Stderr, "输出 JSON 失败: %v\n", err)
			os.Exit(1)
		}
		return
	}
	printMarkdown(reports)
}

func buildReport(unitStore *unit.Store, src source.Source) (sourceReport, error) {
	units, err := unitStore.GetUnitsBySourceIDFiltered(src.SourceID, "current")
	if err != nil {
		return sourceReport{}, fmt.Errorf("查询 units: %w", err)
	}

	// The markdown file is optional context: without it source-text overlap
	// simply stays 0 and recall relies on range/center/point signals.
	var mdLines []string
	if src.MarkdownPath != "" {
		if data, err := os.ReadFile(src.MarkdownPath); err == nil {
			mdLines = strings.Split(string(data), "\n")
		}
	}

	report := sourceReport{
		SourceID:      src.SourceID,
		Title:         src.Title,
		PairsByReason: map[string]int{},
	}

	var dedupUnits []unit.DedupUnit
	for _, ku := range units {
		if ku.Status != "completed" {
			continue // extraction_failed marker rows aren't retrievable units
		}
		report.UnitCount++
		if ku.LineEnd-ku.LineStart+1 <= 2 {
			report.ShortUnitCount++
		}

		points, err := unitStore.GetPointsByUnitID(ku.UnitID)
		if err != nil {
			return sourceReport{}, fmt.Errorf("查询 points (%s): %w", ku.UnitID, err)
		}
		var pointTexts []string
		for _, p := range points {
			pointTexts = append(pointTexts, p.Content)
		}

		dedupUnits = append(dedupUnits, unit.DedupUnit{
			UnitID:       ku.UnitID,
			Center:       ku.Center,
			LineStart:    ku.LineStart,
			LineEnd:      ku.LineEnd,
			SegmentIndex: -1, // unknowable offline
			PointsText:   strings.Join(pointTexts, "\n"),
			SourceText:   joinLines(mdLines, ku.LineStart, ku.LineEnd),
		})
	}

	report.Candidates = unit.CandidatePairs(dedupUnits)
	for _, p := range report.Candidates {
		for _, reason := range p.Reasons {
			report.PairsByReason[reason]++
		}
	}
	return report, nil
}

// joinLines returns mdLines[start..end] (1-based inclusive) joined by \n,
// clamped to the file's bounds.
func joinLines(mdLines []string, start, end int) string {
	if len(mdLines) == 0 {
		return ""
	}
	if start < 1 {
		start = 1
	}
	if end > len(mdLines) {
		end = len(mdLines)
	}
	if start > end {
		return ""
	}
	return strings.Join(mdLines[start-1:end], "\n")
}

func printMarkdown(reports []sourceReport) {
	totalPairs := 0
	for _, r := range reports {
		totalPairs += len(r.Candidates)
	}
	fmt.Printf("# 知识单元重复候选报告\n\n共 %d 个 source，%d 个候选对。\n", len(reports), totalPairs)
	fmt.Println("\n人工标注说明：在每个候选对的 `标注:` 后填 duplicate（重复，应合并）/ related（总览与细节、父子、并列，不合并）/ distinct（不相关）。")

	for _, r := range reports {
		fmt.Printf("\n## %s (`%s`)\n\n", r.Title, r.SourceID)
		fmt.Printf("- KU 总数: %d\n- 短 KU（≤2 行）: %d\n", r.UnitCount, r.ShortUnitCount)
		if len(r.PairsByReason) > 0 {
			fmt.Printf("- 命中原因分布: ")
			first := true
			for _, reason := range []string{"range_exact", "range_contains", "range_overlap", "center_identical", "center_substring", "center_similar", "points_similar", "source_overlap"} {
				if n, ok := r.PairsByReason[reason]; ok {
					if !first {
						fmt.Print("，")
					}
					fmt.Printf("%s=%d", reason, n)
					first = false
				}
			}
			fmt.Println()
		}
		if len(r.Candidates) == 0 {
			fmt.Println("\n无候选对。")
			continue
		}
		for i, p := range r.Candidates {
			fmt.Printf("\n### 候选 %d：%s\n\n", i+1, strings.Join(p.Reasons, ", "))
			fmt.Printf("| | A | B |\n|---|---|---|\n")
			fmt.Printf("| unit_id | `%s` | `%s` |\n", p.A.UnitID, p.B.UnitID)
			fmt.Printf("| center | %s | %s |\n", escapePipe(p.A.Center), escapePipe(p.B.Center))
			fmt.Printf("| 行范围 | L%d-L%d | L%d-L%d |\n", p.A.LineStart, p.A.LineEnd, p.B.LineStart, p.B.LineEnd)
			fmt.Printf("\nrange=%s center_sim=%.2f point_sim=%.2f source_sim=%.2f\n", p.RangeRelation, p.CenterSim, p.PointSim, p.SourceSim)
			if p.A.PointsText != "" || p.B.PointsText != "" {
				fmt.Printf("\nA 知识点：\n%s\n\nB 知识点：\n%s\n", indent(p.A.PointsText), indent(p.B.PointsText))
			}
			fmt.Printf("\n标注: \n")
		}
	}
}

// applyConfirmedPairs reads human-confirmed duplicate pairs, clusters them
// with union-find (A≈B plus B≈C merges all three), and merges each cluster
// into its earliest-starting unit via unit.ApplyOfflineMerge. Requires the
// server stopped — the bleve indexes take an exclusive lock.
func applyConfirmedPairs(cfg *config.Config, sourceStore *source.Store, unitStore *unit.Store, pairsPath string) error {
	f, err := os.Open(pairsPath)
	if err != nil {
		return fmt.Errorf("读取重复对文件: %w", err)
	}
	defer f.Close()

	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] == x {
			return x
		}
		parent[x] = find(parent[x])
		return parent[x]
	}
	union := func(a, b string) {
		if _, ok := parent[a]; !ok {
			parent[a] = a
		}
		if _, ok := parent[b]; !ok {
			parent[b] = b
		}
		parent[find(a)] = find(b)
	}

	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return fmt.Errorf("第 %d 行格式错误（需要两个 unit_id，空白分隔）: %q", lineNo, line)
		}
		union(fields[0], fields[1])
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取重复对文件: %w", err)
	}
	if len(parent) == 0 {
		fmt.Println("文件中没有可执行的重复对。")
		return nil
	}

	clusters := map[string][]string{}
	for id := range parent {
		root := find(id)
		clusters[root] = append(clusters[root], id)
	}

	// The bleve indexes must be writable so superseded units and moved
	// points leave/refresh the search index; NewManager fails fast on the
	// exclusive lock if the server still holds it.
	idxMgr, err := index.NewManager(cfg.Index.Path)
	if err != nil {
		return fmt.Errorf("打开索引失败（服务是否已停止？先 ./run.sh stop）: %w", err)
	}
	defer idxMgr.Close()

	svc := unit.NewService(unitStore, sourceStore, nil, idxMgr.Units, idxMgr.Points, queue.New(1), cfg)

	applied := 0
	for _, members := range clusters {
		if len(members) < 2 {
			continue
		}
		// Survivor = earliest-starting (then longest) unit in the cluster.
		type unitInfo struct {
			id         string
			start, end int
		}
		infos := make([]unitInfo, 0, len(members))
		for _, id := range members {
			ku, err := unitStore.GetUnitByID(id)
			if err != nil {
				return fmt.Errorf("unit %s 不存在: %w", id, err)
			}
			infos = append(infos, unitInfo{id: id, start: ku.LineStart, end: ku.LineEnd})
		}
		survivor := infos[0]
		for _, in := range infos[1:] {
			if in.start < survivor.start || (in.start == survivor.start && in.end > survivor.end) {
				survivor = in
			}
		}
		var merged []string
		for _, in := range infos {
			if in.id != survivor.id {
				merged = append(merged, in.id)
			}
		}

		reason := fmt.Sprintf("merged into %s (dedup-report apply, human-confirmed duplicate)", survivor.id)
		if err := svc.ApplyOfflineMerge(survivor.id, merged, reason); err != nil {
			return fmt.Errorf("合并簇（survivor %s）失败: %w", survivor.id, err)
		}
		fmt.Printf("已合并：%v → %s\n", merged, survivor.id)
		applied++
	}
	fmt.Printf("完成，共合并 %d 个重复簇。\n", applied)
	return nil
}

func escapePipe(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}

func indent(s string) string {
	if s == "" {
		return "> (无)"
	}
	return "> " + strings.ReplaceAll(s, "\n", "\n> ")
}
