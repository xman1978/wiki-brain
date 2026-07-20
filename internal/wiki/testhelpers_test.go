package wiki

import (
	"database/sql"
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
// u1/lines 1-5, p2 in u2/lines 6-10), both above qualifyingMinConfident=5,
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

	idxDir := filepath.Join(tmpDir, "index")
	idxMgr, err := index.NewManager(idxDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idxMgr.Close() })

	fake := llm.NewFakeClient()
	store := NewStore(db)
	cfg := config.WikiConfig{CompileMaxChars: 12000, RecompileNewKPMin: 2}
	svc := NewService(store, fake, idxMgr.Wiki, cfg, 5)

	return svc, fake, db, idxMgr.Wiki
}

func seedVerifiedLink(t *testing.T, db *sql.DB, linkID, pointID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO activation_links (link_id, question_terms, subject_terms, point_id, status)
		VALUES (?, ?, ?, ?, 'verified')`, linkID, "q_"+pointID, "s_"+pointID, pointID); err != nil {
		t.Fatalf("seed verified link: %v", err)
	}
}

func newTestActivationSvc(db *sql.DB) *activation.Service {
	store := activation.NewStore(db)
	return activation.NewService(store, activation.NewMatcher(store))
}

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
