package wiki

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/blevesearch/bleve/v2"
	"github.com/jxman78/wiki-brain/internal/activation"
	"github.com/jxman78/wiki-brain/internal/foundation"
	"github.com/jxman78/wiki-brain/internal/foundation/config"
	"github.com/jxman78/wiki-brain/internal/foundation/index"
	"github.com/jxman78/wiki-brain/internal/foundation/llm"
)

func seedDomain(t *testing.T, db *sql.DB, domainID, name string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO domains (domain_id, name) VALUES (?, ?)`, domainID, name); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
}

func seedConcept(t *testing.T, db *sql.DB, conceptID, domainID, name string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO concepts (concept_id, domain_id, name, description) VALUES (?, ?, ?, ?)`,
		conceptID, domainID, name, name+" 的描述"); err != nil {
		t.Fatalf("seed concept: %v", err)
	}
}

func seedSource(t *testing.T, db *sql.DB, sourceID, markdownPath string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sources (source_id, title, format, file_name, original_path, markdown_path, status)
		VALUES (?, 'test source', 'markdown', 'test.md', '/test.md', ?, 'done')`, sourceID, markdownPath); err != nil {
		t.Fatalf("seed source: %v", err)
	}
}

func seedKU(t *testing.T, db *sql.DB, unitID, sourceID, conceptID, center string, lineStart, lineEnd int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO knowledge_units (unit_id, source_id, concept_id, center, line_start, line_end, status, prompt_version)
		VALUES (?, ?, ?, ?, ?, ?, 'done', 'v1')`, unitID, sourceID, conceptID, center, lineStart, lineEnd); err != nil {
		t.Fatalf("seed KU: %v", err)
	}
}

func seedKP(t *testing.T, db *sql.DB, pointID, unitID, sourceID, content string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO knowledge_points (point_id, unit_id, source_id, content, point_type)
		VALUES (?, ?, ?, ?, 'fact')`, pointID, unitID, sourceID, content); err != nil {
		t.Fatalf("seed KP: %v", err)
	}
}

func seedLinkCandidate(t *testing.T, db *sql.DB, candidateID, questionTerms, pointID string, confidentCount int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO link_candidates (candidate_id, question_terms, point_id, confident_count, hit_count)
		VALUES (?, ?, ?, ?, ?)`, candidateID, questionTerms, pointID, confidentCount, confidentCount); err != nil {
		t.Fatalf("seed link candidate: %v", err)
	}
}

// setupTestService seeds one concept (c1) with two qualifying KPs (p1 in
// u1/lines 1-5, p2 in u2/lines 6-10), each with a verified ActivationLink,
// and opens a real Bleve wiki index so Publish/TryDirectAnswer can be
// exercised end-to-end.
func setupTestService(t *testing.T) (*Service, *llm.FakeClient, *sql.DB, bleve.Index) {
	t.Helper()
	db := foundation.NewTestDB(t)

	seedDomain(t, db, "d1", "Domain One")
	seedConcept(t, db, "c1", "d1", "Concept One")

	tmpDir := foundation.NewTestDir(t)
	mdPath := filepath.Join(tmpDir, "source.md")
	mdContent := "Line 1\nLine 2\nLine 3\nLine 4\nLine 5\nLine 6\nLine 7\nLine 8\nLine 9\nLine 10"
	if err := os.WriteFile(mdPath, []byte(mdContent), 0644); err != nil {
		t.Fatal(err)
	}
	seedSource(t, db, "s1", mdPath)

	seedKU(t, db, "u1", "s1", "c1", "Topic A", 1, 5)
	seedKU(t, db, "u2", "s1", "c1", "Topic B", 6, 10)
	seedKP(t, db, "p1", "u1", "s1", "point one content")
	seedKP(t, db, "p2", "u2", "s1", "point two content")
	seedLinkCandidate(t, db, "lc1", "t1", "p1", 10)
	seedLinkCandidate(t, db, "lc2", "t2", "p2", 8)
	// Both qualifying KPs need a verified ActivationLink (docs/design/
	// wiki-compilation.md "反复激活、多次验证、持续采用不是命中次数") — tests
	// that care about the verified/unverified distinction seed their own extra
	// point instead of relying on p1/p2 lacking one.
	seedVerifiedLink(t, db, "link-p1", "p1")
	seedVerifiedLink(t, db, "link-p2", "p2")

	idxDir := filepath.Join(tmpDir, "index")
	idxMgr, err := index.NewManager(idxDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idxMgr.Close() })

	fake := llm.NewFakeClient()
	// Default analysis response covering both qualifying KPs — Compile/
	// Recompile always run the analysis step first (docs/impl/v1/wiki.md
	// 步骤 2/3); tests exercising a different concept's own points override
	// this before compiling.
	fake.SetResponse("wiki_analyze.md", llm.FakeResponse{Output: validAnalyzeOutput})
	store := NewStore(db)
	cfg := config.WikiConfig{CompileMaxChars: 12000, RecompileNewKPMin: 2}
	svc := NewService(store, fake, idxMgr.Wiki, cfg)

	return svc, fake, db, idxMgr.Wiki
}

func seedVerifiedLink(t *testing.T, db *sql.DB, linkID, pointID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO activation_links (link_id, question_terms, subject_terms, point_id, status)
		VALUES (?, ?, ?, ?, 'verified')`, linkID, "q_"+pointID, "s_"+pointID, pointID); err != nil {
		t.Fatalf("seed verified link: %v", err)
	}
}

// setObservedConditions overwrites linkID's observed_conditions column —
// used to test the four-tuple retrieval entry (docs/design/wiki-compilation.md
// "触发问法取材真实观测，检索匹配复用四元组"), which reads this column
// directly rather than going through AppendObservedCondition.
func setObservedConditions(t *testing.T, db *sql.DB, linkID string, conds []activation.ObservedCondition) {
	t.Helper()
	raw, err := json.Marshal(conds)
	if err != nil {
		t.Fatalf("marshal observed conditions: %v", err)
	}
	if _, err := db.Exec(`UPDATE activation_links SET observed_conditions = ? WHERE link_id = ?`, string(raw), linkID); err != nil {
		t.Fatalf("set observed conditions: %v", err)
	}
}

// seedConfidentTrace seeds a minimal answers+traces pair so
// ConfidentQuestionsForPoints has real question text to select
// (docs/design/wiki-compilation.md "触发问法取材真实观测，检索匹配复用四元组").
func seedConfidentTrace(t *testing.T, db *sql.DB, traceID, question string, pointIDs []string) {
	t.Helper()
	answerID := "ans-" + traceID
	if _, err := db.Exec(`INSERT INTO answers (answer_id, question, content, path, prompt_version, model_name)
		VALUES (?, ?, 'a', 'short', 'v1', 'test')`, answerID, question); err != nil {
		t.Fatalf("seed answer: %v", err)
	}
	directPointIDsJSON, err := json.Marshal(pointIDs)
	if err != nil {
		t.Fatalf("marshal direct point ids: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO traces
		(trace_id, answer_id, question, question_hash, question_terms, retrieval_quality, path, direct_point_ids, subject, intent, audience, constraint_text)
		VALUES (?, ?, ?, ?, ?, 'confident', 'fast', ?, '', '', '', '')`,
		traceID, answerID, question, "hash_"+traceID, question, string(directPointIDsJSON)); err != nil {
		t.Fatalf("seed confident trace: %v", err)
	}
}

func newTestActivationSvc(db *sql.DB) *activation.Service {
	store := activation.NewStore(db)
	return activation.NewService(store, activation.NewMatcher(store))
}

const validAnalyzeOutput = `{
	"claims": [
		{"summary": "内容一的核心结论", "cited_point_ids": ["p1"]},
		{"summary": "内容二的核心结论", "cited_point_ids": ["p2"]}
	],
	"tensions": []
}`

const validCompileOutput = `{
	"title": "Concept One 知识页",
	"content": "## 稳定结论\n[p1] 内容一\n[p2] 内容二\n\n## 展开说明\n详细说明。\n\n## 待验证点\n暂无。\n\n## 依赖来源\n见引用。\n",
	"cited_point_ids": ["p1", "p2"]
}`

const missingSectionsCompileOutput = `{
	"title": "Concept One 知识页",
	"content": "## 稳定结论\n[p1] 内容一\n\n## 展开说明\n详细说明。\n",
	"cited_point_ids": ["p1"]
}`

const hallucinatedCiteCompileOutput = `{
	"title": "Concept One 知识页",
	"content": "## 稳定结论\n[p1] 内容一 [p999] 幻觉引用\n\n## 展开说明\n详细说明。\n\n## 待验证点\n暂无。\n\n## 依赖来源\n见引用。\n",
	"cited_point_ids": ["p1", "p999"]
}`
