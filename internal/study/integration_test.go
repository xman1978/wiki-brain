package study

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	fdb "github.com/jxman78/wiki-brain/internal/foundation/db"
)

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// traceData mirrors the trace_report.txt content
type traceData struct {
	questionID    string
	question      string
	answerID      string
	traceID       string
	quality       string
	path          string
	questionHash  string
	questionTerms string
	pointIDs      []string
	hasGapEvent   bool
}

func allTraces() []traceData {
	return []traceData{
		{
			questionID: "q01", question: "什么是绩效管理？什么是绩效考核？两者有什么区别？",
			answerID: "a784c463-2a0b-4bf5-99af-2fcdcb769a5d", traceID: "bd1f49e5-605a-4eef-90b0-061195f0f831",
			quality: "confident", path: "short",
			questionHash: "1265afecf7dc7b97cc769ce888ccc25a", questionTerms: "两者 区别 管理 绩效 绩效考核",
			pointIDs: []string{"e43aeca9-4804-4fde-86e5-65783c69a164"},
		},
		{
			questionID: "q02", question: "培训积分由哪几部分组成？各部分的含义是什么？",
			answerID: "3adf5fac-595e-4f77-90b1-a790e517a471", traceID: "b389bfef-892f-438d-aa01-6a895266b87d",
			quality: "confident", path: "short",
			questionHash: "fc0c27cee64cfb83aabc81853c09eb8d", questionTerms: "各 含义 哪几 培训 由 积分 组成 部分 部分",
			pointIDs: []string{"9ae72a21-1e4a-4a4f-a1e7-d666792895b0", "f2738741-73f1-4bf4-8557-464b634ef881"},
		},
		{
			questionID: "q03", question: "无合同立项制度中，成本红线是什么意思？",
			answerID: "74dac5cc-3fb0-4669-93f1-43cd46b7e0f4", traceID: "40d3e63a-7119-46ad-85d4-3d47fc4b1f8b",
			quality: "confident", path: "short",
			questionHash: "a6115ab3446d1b481aeecb4c5d639af6", questionTerms: "中 制度 合同 意思 成本 无 立项 红线",
			pointIDs: []string{"4df01cdd-2854-4739-a182-2f7b02d4562b"},
		},
		{
			questionID: "q04", question: "一个员工从北京出差去武汉，住宿费最高能报销多少？如果两位同部门同性员工一起出差武汉，住宿应如何安排？",
			answerID: "a2c4853b-f5a3-4061-8f3f-f6a806346bd2", traceID: "25e1b108-8c0a-4d61-b050-919ae0a5548a",
			quality: "confident", path: "deep",
			questionHash: "79e081821e5a2c7965ce9d988951068a", questionTerms: "一个 一起 两位 住宿 住宿费 出差 出差 北京 去 同 同性 员工 员工 多少 如果 安排 应 报销 最高 武汉 武汉 能 部门",
			pointIDs: []string{"c144a7de-bc9a-40f1-bc5c-c975c6312d92", "bf54f003-7b14-4b0a-9977-50d1eefc5588"},
		},
		{
			questionID: "q05", question: "某员工2021年签约的合同至今未回款，根据制度，现在这笔回款由谁负责？经历了哪些阶段？",
			answerID: "98f63cba-00f5-4d64-8043-07487c410ab5", traceID: "697ea45e-f3d0-463e-b764-8e242f7f0d36",
			quality: "confident", path: "deep",
			questionHash: "3f5cf470cf35ef4539813aa54f52f78c", questionTerms: "2021 制度 合同 员工 回款 回款 年 未 某 根据 现在 由 签约 经历 至今 谁 负责 这笔 阶段",
			pointIDs: []string{"2707f974-c4c0-443a-b510-22921fca6f1a", "f3160597-8a14-474c-b906-409b77647897", "f0142f1c-dd6f-49c0-ab6f-6d783d895874", "e6bb0890-bf84-4835-8a9b-47843baa0911", "7b6bad0a-217f-4b1f-87f6-3178a79e35ea"},
		},
		{
			questionID: "q06", question: "如果一个项目的决算成本比预算成本超支了20%，该项目的考核评定结果是什么？能拿到项目奖金吗？",
			answerID: "ce60f82d-f82f-4657-a604-66d4d7493ebe", traceID: "a390e41c-570c-48da-b1de-845c1b306b5e",
			quality: "confident", path: "deep",
			questionHash: "bee676f48249beba977cfa02bc8326a9", questionTerms: "20 一个 决算 奖金 如果 成本 成本 拿到 比 目的 结果 考核 能 评定 该项 超支 项目 项目 预算",
			pointIDs: []string{"1c94204a-4eb9-4e26-9da0-0063265ffce9", "bed5622f-41e9-4b47-9e8e-73a67bb47246", "f21adf8f-8a62-4867-a60b-872a41c84b9c", "11d824f3-71b8-4ba2-8838-17ecf851bd25"},
		},
		{
			questionID: "q07", question: "公司差旅费制度中，允许乘坐飞机出差的情况有哪些？",
			answerID: "66d6080a-6e1e-46c0-8272-ce9df5bedcb1", traceID: "58cfb2ca-c99c-42ce-b7e1-e5e4e53c6863",
			quality: "confident", path: "short",
			questionHash: "be3c0bd34d5940cb3c349436571c1614", questionTerms: "中 乘坐 允许 公司 出差 制度 差旅费 情况 飞机",
			pointIDs: []string{"952facad-9548-48cc-8c35-244a4a6b57c3"},
		},
		{
			questionID: "q08", question: "日常费用报销有哪些时间限制？分别是多长？",
			answerID: "f6d64d52-6fb4-4797-bd2a-213765d0eee5", traceID: "5c5614b9-0e04-4b54-b4c7-a45ed267ae33",
			quality: "confident", path: "short",
			questionHash: "7392e25599af15a5acf842c35d8458e1", questionTerms: "分别 多长 报销 日常 时间 费用 限制",
			pointIDs: []string{"8dc2c191-95b8-4fe2-b302-04b747273fb8", "b0feabba-480d-46c1-b7b3-3580ea62c3a3"},
		},
		{
			questionID: "q09", question: "公司年假制度是怎样的？员工入职满一年后有几天年假？",
			answerID: "a6b8c696-4c36-404f-b739-ebdf5d8b490b", traceID: "eb8c5c2a-ca63-482e-8373-dbc4cb49f804",
			quality: "gap", path: "none",
			questionHash: "913b08c5d145734c3400af2aa46035b8", questionTerms: "一年 假 假 入 公司 几天 制度 后 员工 年 年 怎样 满 职",
			pointIDs: []string{}, hasGapEvent: true,
		},
		{
			questionID: "q10", question: "公司使用的项目管理工具是Jira还是禅道？",
			answerID: "4cc1acfb-c19e-4c54-ab8d-6fcaf7b7b32d", traceID: "165bd4da-d8d8-429f-b594-6d14636381af",
			quality: "gap", path: "none",
			questionHash: "6f47650b7286a5107812b2f54fdfc4f8", questionTerms: "jira 使用 公司 工具 禅 还是 道 项目管理",
			pointIDs: []string{}, hasGapEvent: true,
		},
	}
}

func TestIntegration_StudyReportFromSeedDB(t *testing.T) {
	seedDBPath := filepath.Join("..", "..", "test", "testdata", "seed.db")
	if _, err := os.Stat(seedDBPath); os.IsNotExist(err) {
		t.Skip("seed.db not found, skipping integration test")
	}

	tmpDir := t.TempDir()
	testDBPath := filepath.Join(tmpDir, "test.db")
	if err := copyFile(seedDBPath, testDBPath); err != nil {
		t.Fatalf("copy seed.db: %v", err)
	}

	db, err := fdb.Open(testDBPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Seed answers, traces, cooccurrence, learning_events from trace_report data
	traces := allTraces()
	for _, td := range traces {
		// Insert answer
		_, err := db.Exec(`INSERT INTO answers (answer_id, question, content, path, prompt_version, model_name)
			VALUES (?, ?, 'test answer content', ?, 'v1', 'test')`,
			td.answerID, td.question, td.path)
		if err != nil {
			t.Fatalf("insert answer %s: %v", td.questionID, err)
		}

		// Insert trace
		pidsJSON, _ := json.Marshal(td.pointIDs)
		_, err = db.Exec(`INSERT INTO traces (trace_id, answer_id, question, question_hash, question_terms, retrieval_quality, path, direct_point_ids)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			td.traceID, td.answerID, td.question, td.questionHash, td.questionTerms,
			td.quality, td.path, string(pidsJSON))
		if err != nil {
			t.Fatalf("insert trace %s: %v", td.questionID, err)
		}

		// Insert cooccurrence for confident traces
		if td.quality == "confident" {
			for _, pid := range td.pointIDs {
				coocID := uuid.New().String()
				_, err = db.Exec(`INSERT INTO question_kp_cooccurrence (cooc_id, question_terms, point_id, hit_count, confident_count, last_seen_at)
					VALUES (?, ?, ?, 1, 1, CURRENT_TIMESTAMP)
					ON CONFLICT(question_terms, point_id) DO UPDATE SET
						hit_count = hit_count + 1,
						confident_count = confident_count + 1,
						last_seen_at = CURRENT_TIMESTAMP`,
					coocID, td.questionTerms, pid)
				if err != nil {
					t.Fatalf("insert cooccurrence %s/%s: %v", td.questionID, pid, err)
				}
			}
		}

		// Insert learning event for gap traces
		if td.hasGapEvent {
			payload, _ := json.Marshal(map[string]string{"question": td.question})
			eventID := uuid.New().String()
			_, err = db.Exec(`INSERT INTO learning_events (event_id, trace_id, event_type, payload) VALUES (?, ?, 'knowledge_gap', ?)`,
				eventID, td.traceID, string(payload))
			if err != nil {
				t.Fatalf("insert learning_event %s: %v", td.questionID, err)
			}
		}
	}

	// Verify seed state
	var traceCount, coocCount, gapEventCount int
	db.QueryRow(`SELECT COUNT(*) FROM traces`).Scan(&traceCount)
	db.QueryRow(`SELECT COUNT(*) FROM question_kp_cooccurrence`).Scan(&coocCount)
	db.QueryRow(`SELECT COUNT(*) FROM learning_events WHERE event_type='knowledge_gap' AND processed=0`).Scan(&gapEventCount)
	t.Logf("Seeded: %d traces, %d cooccurrence entries, %d gap events", traceCount, coocCount, gapEventCount)

	if traceCount != 10 {
		t.Fatalf("expected 10 traces, got %d", traceCount)
	}
	if coocCount != 18 {
		t.Fatalf("expected 18 cooccurrence entries, got %d", coocCount)
	}
	if gapEventCount != 2 {
		t.Fatalf("expected 2 gap events, got %d", gapEventCount)
	}

	// Run Study
	store := NewStore(db)
	cfg := testConfig()
	cfg.CandidateConfidentMin = 1 // lower thresholds for single-pass data
	cfg.CandidateRatioMin = 0.5
	cfg.WikiConfidentMin = 1
	cfg.WikiKPMin = 2
	svc := NewService(store, cfg)

	result, err := svc.Run()
	if err != nil {
		t.Fatalf("Study.Run: %v", err)
	}

	t.Logf("Study result: report_id=%s candidates_flagged=%d gap_events_processed=%d elapsed_ms=%d",
		result.ReportID, result.CandidatesFlagged, result.GapEventsProcessed, result.ElapsedMs)

	// ===== Validate Report =====
	content, err := store.GetReport(result.ReportID)
	if err != nil || content == nil {
		t.Fatal("report not found after Run")
	}

	var report Report
	if err := json.Unmarshal([]byte(*content), &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}

	// Print report for inspection
	prettyJSON, _ := json.MarshalIndent(report, "", "  ")
	t.Logf("Report:\n%s", string(prettyJSON))

	// ===== Summary validation =====
	t.Run("Summary", func(t *testing.T) {
		s := report.Summary
		if s.TotalTraces != 10 {
			t.Errorf("total_traces: expected 10, got %d", s.TotalTraces)
		}
		if s.ConfidentCount != 8 {
			t.Errorf("confident_count: expected 8, got %d", s.ConfidentCount)
		}
		if s.GapCount != 2 {
			t.Errorf("gap_count: expected 2, got %d", s.GapCount)
		}
		expectedRate := 8.0 / 10.0
		if s.ConfidentRate < expectedRate-0.01 || s.ConfidentRate > expectedRate+0.01 {
			t.Errorf("confident_rate: expected %.2f, got %.2f", expectedRate, s.ConfidentRate)
		}
		if s.TotalCooccurrencePairs != 18 {
			t.Errorf("total_cooccurrence_pairs: expected 18, got %d", s.TotalCooccurrencePairs)
		}
		t.Logf("Summary OK: %d traces, %d confident (%.0f%%), %d gap, %d cooccurrence pairs, %d candidates",
			s.TotalTraces, s.ConfidentCount, s.ConfidentRate*100, s.GapCount, s.TotalCooccurrencePairs, s.CandidatesFlagged)
	})

	// ===== Activation Link Candidates validation =====
	t.Run("ActivationLinkCandidates", func(t *testing.T) {
		if result.CandidatesFlagged == 0 {
			t.Error("expected candidates to be flagged from cooccurrence data")
		}
		if len(report.ActivationLinkCandidates) == 0 {
			t.Error("expected activation_link_candidates in report")
			return
		}
		t.Logf("Activation candidates: %d", len(report.ActivationLinkCandidates))

		for _, c := range report.ActivationLinkCandidates {
			// signal_purity = confident_count / hit_count
			expectedPurity := 0.0
			if c.Stats.HitCount > 0 {
				expectedPurity = float64(c.Stats.ConfidentCount) / float64(c.Stats.HitCount)
			}
			if c.Stats.SignalPurity < expectedPurity-0.01 || c.Stats.SignalPurity > expectedPurity+0.01 {
				t.Errorf("candidate %s: signal_purity mismatch: expected %.2f, got %.2f",
					c.PointID, expectedPurity, c.Stats.SignalPurity)
			}

			// point_summary and unit_topic should be populated
			if c.PointSummary == "" {
				t.Errorf("candidate %s: empty point_summary", c.PointID)
			}
			if c.UnitTopic == "" {
				t.Errorf("candidate %s: empty unit_topic", c.PointID)
			}

			// recommendation must be "strong" or "candidate"
			if c.Recommendation != "strong" && c.Recommendation != "candidate" {
				t.Errorf("candidate %s: invalid recommendation %q", c.PointID, c.Recommendation)
			}

			// Validate recommendation logic
			isStrong := c.Stats.SignalPurity >= 0.7 && c.Stats.ActivationBreadth >= 3 && c.Stats.ShortPathRate >= 0.6
			expectedRec := "candidate"
			if isStrong {
				expectedRec = "strong"
			}
			if c.Recommendation != expectedRec {
				t.Errorf("candidate %s: recommendation=%s but expected=%s (purity=%.2f breadth=%d spr=%.2f)",
					c.PointID, c.Recommendation, expectedRec,
					c.Stats.SignalPurity, c.Stats.ActivationBreadth, c.Stats.ShortPathRate)
			}

			t.Logf("  [%s] %s → %s (purity=%.2f breadth=%d spr=%.2f kpn=%v)",
				c.Recommendation, c.QuestionTerms[:min(20, len(c.QuestionTerms))], c.PointID[:8],
				c.Stats.SignalPurity, c.Stats.ActivationBreadth, c.Stats.ShortPathRate, c.Stats.HasKPNNeighbors)
		}
	})

	// ===== Wiki Candidates validation =====
	t.Run("WikiCandidates", func(t *testing.T) {
		t.Logf("Wiki candidates: %d", len(report.WikiCandidates))
		for _, w := range report.WikiCandidates {
			if w.ConceptID == "" {
				t.Error("wiki candidate with empty concept_id")
			}
			if w.ConceptName == "" {
				t.Error("wiki candidate with empty concept_name")
			}
			if w.DomainID == "" {
				t.Error("wiki candidate with empty domain_id")
			}
			if w.Stats.QualifyingKPCount < cfg.WikiKPMin && w.Recommendation == "ready" {
				t.Errorf("wiki %s: ready with only %d qualifying KPs (min=%d)",
					w.ConceptName, w.Stats.QualifyingKPCount, cfg.WikiKPMin)
			}
			if w.Recommendation != "ready" && w.Recommendation != "needs_more_data" {
				t.Errorf("wiki %s: invalid recommendation %q", w.ConceptName, w.Recommendation)
			}

			// Verify recommendation logic
			isReady := w.Stats.QualifyingKPCount >= cfg.WikiKPMin && w.Stats.KPNConnectionCount >= 1
			expectedRec := "needs_more_data"
			if isReady {
				expectedRec = "ready"
			}
			if w.Recommendation != expectedRec {
				t.Errorf("wiki %s: recommendation=%s but expected=%s (kp=%d conn=%d)",
					w.ConceptName, w.Recommendation, expectedRec,
					w.Stats.QualifyingKPCount, w.Stats.KPNConnectionCount)
			}

			t.Logf("  [%s] %s: %d KPs, avg_confident=%.1f, kpn_conn=%d, days_active=%d",
				w.Recommendation, w.ConceptName, w.Stats.QualifyingKPCount,
				w.Stats.AvgConfidentCount, w.Stats.KPNConnectionCount, w.Stats.DaysActive)
		}
	})

	// ===== Knowledge Gaps validation =====
	t.Run("KnowledgeGaps", func(t *testing.T) {
		if result.GapEventsProcessed != 2 {
			t.Errorf("expected 2 gap events processed, got %d", result.GapEventsProcessed)
		}
		if len(report.KnowledgeGaps) != 2 {
			t.Errorf("expected 2 knowledge gap entries, got %d", len(report.KnowledgeGaps))
		}
		for _, g := range report.KnowledgeGaps {
			if g.Question == "" {
				t.Error("gap with empty question")
			}
			if g.Recommendation != "补充材料" {
				t.Errorf("gap recommendation: expected 补充材料, got %s", g.Recommendation)
			}
			t.Logf("  gap: %q (hit=%d)", g.Question, g.HitCount)
		}

		// Verify gap events are now processed
		var unprocessed int
		db.QueryRow(`SELECT COUNT(*) FROM learning_events WHERE event_type='knowledge_gap' AND processed=0`).Scan(&unprocessed)
		if unprocessed != 0 {
			t.Errorf("expected 0 unprocessed gap events after Run, got %d", unprocessed)
		}
	})

	// ===== Report persistence validation =====
	t.Run("ReportPersistence", func(t *testing.T) {
		latest, err := store.GetLatestReport()
		if err != nil || latest == nil {
			t.Fatal("GetLatestReport failed")
		}

		var latestReport Report
		json.Unmarshal([]byte(*latest), &latestReport)
		if latestReport.ReportID != report.ReportID {
			t.Errorf("latest report_id mismatch: %s vs %s", latestReport.ReportID, report.ReportID)
		}

		metas, err := store.ListReports()
		if err != nil {
			t.Fatalf("ListReports: %v", err)
		}
		if len(metas) != 1 {
			t.Errorf("expected 1 report meta, got %d", len(metas))
		}
	})

	// ===== Report completeness check =====
	t.Run("ReportCompleteness", func(t *testing.T) {
		if report.ReportID == "" {
			t.Error("empty report_id")
		}
		if report.GeneratedAt.IsZero() {
			t.Error("zero generated_at")
		}
		if report.PeriodDays != cfg.ReportPeriodDays {
			t.Errorf("period_days: expected %d, got %d", cfg.ReportPeriodDays, report.PeriodDays)
		}

		// Verify overall data flow: 8 confident → cooccurrence → candidates → report
		confidentQuestions := 0
		for _, td := range traces {
			if td.quality == "confident" {
				confidentQuestions++
			}
		}
		if report.Summary.ConfidentCount != confidentQuestions {
			t.Errorf("confident count mismatch: traces=%d report=%d", confidentQuestions, report.Summary.ConfidentCount)
		}
	})

	fmt.Println("=== Study Integration Test Complete ===")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
