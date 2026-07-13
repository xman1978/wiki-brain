# Precomputed Rerank Semantics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist query-independent unit semantics during ingestion so online rerank performs only length-batched, concurrent judge calls.

**Architecture:** A shared `internal/rerank` value type defines the persisted semantic contract. Unit ingestion extracts semantics from the final deduplicated candidate pool before an atomic document-generation publish; retrieval bulk-loads those rows, restores query-time candidate IDs, and sends compact structured batches to the unchanged judge prompt.

**Tech Stack:** Go, SQLite migrations and transactions, Bleve, existing OpenAI-compatible `LLMClient`, Go `testing`.

## Global Constraints

- Existing documents are re-uploaded before release; do not add a backfill command or lazy online extraction.
- A new generation is not published unless every final unit has valid semantics.
- Online rerank never calls an extraction prompt.
- Missing or stale semantics return a data-integrity error containing every affected unit ID.
- Keep domain/source filtering, outline/FTS recall, RRF, KPN, role definitions, and answer generation unchanged.
- Extraction defaults: 4000 source-text characters per batch, concurrency 2.
- Judge defaults: 4000 serialized semantic characters per batch, concurrency 4.
- Run every shell command through `rtk`.

---

## File Map

- Create `internal/foundation/db/migrations/022_unit_rerank_semantics.sql`: one-to-one semantic table.
- Create `internal/rerank/semantics.go`: shared semantic value and current prompt version.
- Create `internal/unit/rerank_semantics.go`: ingestion extraction, batching, validation, and concurrent orchestration.
- Create `config/prompts/unit_semantics_extract.md`: question-independent unit semantic extraction prompt.
- Modify `internal/unit/predup.go`: call semantic extraction after dedup and publish the generation atomically.
- Modify `internal/unit/service.go`: propagate ingestion errors and stop downstream processing.
- Modify `internal/unit/store.go`: document-generation transaction and semantic persistence.
- Modify `internal/unit/predup_test.go`: ingestion success, batching, and failure-preserves-old-generation coverage.
- Modify `internal/retrieval/store.go`: bulk semantic lookup.
- Modify `internal/retrieval/store_test.go`: semantic lookup and invalid JSON coverage.
- Modify `internal/retrieval/service.go`: remove online extraction and batch only judge inputs.
- Modify `internal/retrieval/service_test.go`: judge-only, batching, concurrency, missing/stale semantics coverage.
- Modify `internal/foundation/config/config.go` and `config/config.yml`: separate extraction and judge controls.
- Delete `config/prompts/rerank_extract.md`: replaced by ingestion prompt.
- Modify `docs/impl/mvp/unit.md` and `docs/impl/mvp/retrieval.md`: document the new phase boundary.

---

### Task 1: Persisted Semantic Contract and Bulk Lookup

**Files:**
- Create: `internal/foundation/db/migrations/022_unit_rerank_semantics.sql`
- Create: `internal/rerank/semantics.go`
- Modify: `internal/retrieval/store.go`
- Test: `internal/retrieval/store_test.go`

**Interfaces:**
- Produces: `rerank.Semantics`, `rerank.ExtractPromptVersion`, and `(*retrieval.Store).GetUnitRerankSemantics([]string) (map[string]rerank.Semantics, error)`.
- Consumes: existing `foundation.NewTestDB` migration runner and retrieval store database handle.

- [ ] **Step 1: Write failing store tests**

Add tests that insert a source, unit, and semantic row directly, then verify bulk lookup returns decoded facts and omits unrequested rows. Add a second test with malformed `key_facts_json` and require an error naming the unit.

```go
func TestGetUnitRerankSemanticsBulk(t *testing.T) {
	db := foundation.NewTestDB(t)
	insertRetrievalTestSourceAndUnit(t, db, "s1", "u1")
	insertRetrievalTestSourceAndUnit(t, db, "s1", "u2")
	_, err := db.Exec(`INSERT INTO unit_rerank_semantics
		(unit_id, source_theme, content_theme, intent, object, scope, key_facts_json, prompt_version)
		VALUES ('u1', '制度', '报销', '说明限额', '员工', '出差', '["住宿限额500元"]', 'v1')`)
	if err != nil { t.Fatal(err) }

	got, err := NewStore(db).GetUnitRerankSemantics([]string{"u1"})
	if err != nil { t.Fatal(err) }
	if !reflect.DeepEqual(got["u1"].KeyFacts, []string{"住宿限额500元"}) {
		t.Fatalf("key facts = %#v, want 住宿限额500元", got["u1"].KeyFacts)
	}
	if _, ok := got["u2"]; ok { t.Fatal("unrequested unit returned") }
}
```

- [ ] **Step 2: Run the tests and verify RED**

Run: `rtk go test ./internal/retrieval -run 'TestGetUnitRerankSemantics' -count=1`

Expected: FAIL because the migration/table and store method do not exist.

- [ ] **Step 3: Add the migration and shared type**

Create migration 022 with a primary-key foreign key, JSON validity check, prompt version, and timestamps:

```sql
CREATE TABLE unit_rerank_semantics (
    unit_id TEXT PRIMARY KEY REFERENCES knowledge_units(unit_id) ON DELETE CASCADE,
    source_theme TEXT NOT NULL,
    content_theme TEXT NOT NULL,
    intent TEXT NOT NULL,
    object TEXT NOT NULL,
    scope TEXT NOT NULL,
    key_facts_json TEXT NOT NULL CHECK (json_valid(key_facts_json)),
    prompt_version TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

Create the shared type:

```go
package rerank

const ExtractPromptVersion = "v1"

type Semantics struct {
	UnitID        string
	SourceTheme   string
	ContentTheme  string
	Intent        string
	Object        string
	Scope         string
	KeyFacts      []string
	PromptVersion string
}
```

- [ ] **Step 4: Implement compact bulk lookup**

Build one `IN (...)` query, decode `key_facts_json` into `[]string`, and return a map keyed by unit ID. Empty input returns an empty map without SQL.

```go
func (s *Store) GetUnitRerankSemantics(unitIDs []string) (map[string]rerank.Semantics, error)
```

Wrap query, scan, and JSON errors as `retrieval store: get rerank semantics: ...`; include `unit_id` in decode errors.

- [ ] **Step 5: Run tests and verify GREEN**

Run: `rtk go test ./internal/retrieval -run 'TestGetUnitRerankSemantics' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit the contract**

```bash
rtk git add internal/foundation/db/migrations/022_unit_rerank_semantics.sql internal/rerank/semantics.go internal/retrieval/store.go internal/retrieval/store_test.go
rtk git commit -m "feat: persist unit rerank semantics"
```

---

### Task 2: Ingestion Semantic Extraction

**Files:**
- Create: `internal/unit/rerank_semantics.go`
- Create: `config/prompts/unit_semantics_extract.md`
- Modify: `internal/foundation/config/config.go`
- Modify: `config/config.yml`
- Test: `internal/unit/predup_test.go`

**Interfaces:**
- Consumes: `unitCandidate`, final markdown line ranges, source title, `llm.LLMClient`, and `rerank.Semantics` from Task 1.
- Produces: `(*Service).extractRerankSemantics(context.Context, string, []string, []unitCandidate) (map[string]rerank.Semantics, error)` and extraction configuration getters.

- [ ] **Step 1: Write failing length-batching and concurrency tests**

Add a tracking LLM client that parses unit IDs from the prompt variables, waits on a release channel, and records maximum concurrent `unit_semantics_extract.md` calls. Construct three `unitCandidate` values with final ranges and set a tiny batch budget so each forms its own batch.

```go
func TestExtractRerankSemanticsBatchesByFinalTextAndRunsConcurrently(t *testing.T) {
	svc, tracker := setupRerankSemanticExtractionService(t, 1, 2)
	pool := []unitCandidate{
		{id: "u1", lineStart: 1, lineEnd: 1},
		{id: "u2", lineStart: 2, lineEnd: 2},
		{id: "u3", lineStart: 3, lineEnd: 3},
	}
	got, err := svc.extractRerankSemantics(t.Context(), "差旅制度", []string{"甲", "乙", "丙"}, pool)
	if err != nil { t.Fatal(err) }
	if len(got) != 3 { t.Fatalf("got %d semantics, want 3", len(got)) }
	if tracker.MaxConcurrent() < 2 { t.Fatalf("max concurrency = %d, want >= 2", tracker.MaxConcurrent()) }
}
```

Also test unknown IDs, duplicate IDs, and omitted IDs each return an error.

- [ ] **Step 2: Run tests and verify RED**

Run: `rtk go test ./internal/unit -run 'TestExtractRerankSemantics' -count=1`

Expected: FAIL because the extraction method and config fields do not exist.

- [ ] **Step 3: Add independent extraction and judge config fields**

Replace `RerankBatchMaxChars` and `RerankConcurrency` with:

```go
RerankExtractBatchMaxChars int `yaml:"rerank_extract_batch_max_chars"`
RerankExtractConcurrency   int `yaml:"rerank_extract_concurrency"`
RerankJudgeBatchMaxChars   int `yaml:"rerank_judge_batch_max_chars"`
RerankJudgeConcurrency     int `yaml:"rerank_judge_concurrency"`
```

Set sample values to `4000`, `2`, `4000`, and `4` respectively.

- [ ] **Step 4: Add the ingestion prompt**

Create `unit_semantics_extract.md` version `v1`. Its user section accepts only `source_title` and `units`; its schema requires exactly:

```json
{"results":[{"unit_id":"uuid","source_theme":"...","content_theme":"...","intent":"...","object":"...","scope":"...","key_facts":["..."]}]}
```

Do not include question semantics or role labels in the prompt.

- [ ] **Step 5: Implement extraction batching and validation**

In `internal/unit/rerank_semantics.go`:

```go
const (
	defaultRerankExtractBatchMaxChars = 4000
	defaultRerankExtractConcurrency   = 2
)

func (s *Service) extractRerankSemantics(
	ctx context.Context,
	sourceTitle string,
	mdLines []string,
	pool []unitCandidate,
) (map[string]rerank.Semantics, error)
```

Slice each candidate from `mdLines[lineStart-1:lineEnd]`, split only between units, format stable `[unit_id]` entries, and process batches with `context.WithCancel`, a semaphore, `sync.WaitGroup`, and first-error capture. Validate exact one-to-one unit ID coverage before merging results and stamp `rerank.ExtractPromptVersion` locally rather than trusting model output.

- [ ] **Step 6: Run tests and verify GREEN**

Run: `rtk go test ./internal/unit -run 'TestExtractRerankSemantics' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit ingestion extraction**

```bash
rtk git add internal/unit/rerank_semantics.go internal/unit/predup_test.go internal/foundation/config/config.go config/config.yml config/prompts/unit_semantics_extract.md
rtk git commit -m "feat: extract rerank semantics during ingestion"
```

---

### Task 3: Atomic Generation Publication

**Files:**
- Modify: `internal/unit/store.go`
- Modify: `internal/unit/predup.go`
- Modify: `internal/unit/service.go`
- Test: `internal/unit/predup_test.go`
- Test: `internal/unit/reupload_lifecycle_test.go`

**Interfaces:**
- Consumes: complete semantics map from Task 2.
- Produces: `(*Service).publishCandidates(...) error`; `extractSegmentsPreInsertDedup(...) error`; no database or index publication before extraction succeeds.

- [ ] **Step 1: Write failing publication safety tests**

Add a test with an existing current unit and force `unit_semantics_extract.md` to fail. Assert `Extract` returns an error, the old unit remains `current`, no new unit exists, and the units index still resolves the old unit.

```go
func TestExtractSemanticFailureLeavesPreviousGenerationCurrent(t *testing.T) {
	svc, fake, db := setupReuploadExtractionFixture(t)
	oldID := insertCurrentIndexedUnit(t, svc, db, "src-1")
	setSuccessfulUnitExtractionFakes(fake)
	fake.SetResponse("unit_semantics_extract.md", llm.FakeResponse{Err: errors.New("semantic extraction failed")})

	err := svc.Extract(t.Context(), "src-1")
	if err == nil || !strings.Contains(err.Error(), "semantic extraction failed") { t.Fatalf("err = %v", err) }
	assertUnitLifecycle(t, db, oldID, LifecycleCurrent)
	assertOnlyUnitIndexed(t, svc.unitsIndex, oldID)
}
```

Add a success test asserting units, points, and semantics appear together. Add a transaction-failure test using an invalid semantic JSON fixture or forced constraint violation and assert no partial new generation/index writes.

- [ ] **Step 2: Run tests and verify RED**

Run: `rtk go test ./internal/unit -run 'TestExtractSemantic|TestPublishCandidates' -count=1`

Expected: FAIL because extraction errors are not propagated and publication is not transactional.

- [ ] **Step 3: Implement a document-level store transaction**

Add a store method used only by the unit service:

```go
func (s *Store) PublishGeneration(
	sourceID string,
	pool []unitCandidate,
	semantics map[string]rerank.Semantics,
) (superseded []KnowledgeUnit, inserted []KnowledgeUnit, points []KnowledgePoint, err error)
```

Within one transaction:

1. Read current units for `sourceID`.
2. Update those units and their points to `superseded` with `lifecycle_changed_at=CURRENT_TIMESTAMP`.
3. Insert every new unit and point.
4. Marshal and insert the matching semantic row for every unit.
5. Require `len(semantics) == len(pool)` and reject any missing or extra unit ID before beginning SQL writes.
6. Commit and return values needed for index updates.

Use transaction-local SQL helpers rather than calling methods that execute on `*sql.DB`.

- [ ] **Step 4: Integrate extraction before publication**

Change `extractSegmentsPreInsertDedup` to return `error`. After the final pool is formed, call `extractRerankSemantics`; only on success call `PublishGeneration`. After commit, rewrite superseded index documents and index the returned new units/points.

Change `Service.Extract` to return immediately on this error, before KPN generation and concept matching:

```go
if err := s.extractSegmentsPreInsertDedup(ctx, src.Title, sourceID, segments, mdLines, onDone); err != nil {
	return fmt.Errorf("unit: extract and publish: %w", err)
}
```

Remove the old `supersedePreviousUnits` plus `insertCandidates` call from this path.

- [ ] **Step 5: Run focused and package tests**

Run: `rtk go test ./internal/unit -run 'TestExtractSemantic|TestPublishCandidates' -count=1`

Expected: PASS.

Run: `rtk go test ./internal/unit -count=1`

Expected: PASS.

- [ ] **Step 6: Commit atomic publication**

```bash
rtk git add internal/unit/store.go internal/unit/predup.go internal/unit/service.go internal/unit/predup_test.go internal/unit/reupload_lifecycle_test.go
rtk git commit -m "feat: publish units with rerank semantics atomically"
```

---

### Task 4: Judge-Only Online Rerank

**Files:**
- Modify: `internal/retrieval/service.go`
- Modify: `internal/retrieval/service_test.go`
- Delete: `config/prompts/rerank_extract.md`

**Interfaces:**
- Consumes: `Store.GetUnitRerankSemantics` from Task 1 and judge config from Task 2.
- Produces: online `rerank` that only invokes `rerank_judge.md` and splits batches by compact serialized semantic length.

- [ ] **Step 1: Replace the existing rerank test with a judge-only failing test**

Seed semantic rows for two candidate units, configure a tracking LLM client to reject any prompt except `rerank_judge.md`, force one semantic per batch with a tiny judge budget, and assert two judge calls run concurrently.

```go
func TestRerankUsesPersistedSemanticsAndRunsJudgeBatchesConcurrently(t *testing.T) {
	svc, tracker, candidates := setupPersistedSemanticRerank(t, 1, 2)
	got, err := svc.rerank(t.Context(), QueryContext{Question: "差旅住宿限额是多少？"}, candidates)
	if err != nil { t.Fatal(err) }
	if tracker.Count("rerank_extract.md") != 0 || tracker.Count("unit_semantics_extract.md") != 0 {
		t.Fatal("online rerank called extraction")
	}
	if tracker.Count("rerank_judge.md") != 2 { t.Fatalf("judge calls = %d, want 2", tracker.Count("rerank_judge.md")) }
	if tracker.MaxConcurrent() < 2 { t.Fatalf("max concurrency = %d, want >= 2", tracker.MaxConcurrent()) }
	if len(got) != 2 { t.Fatalf("kept = %d, want 2", len(got)) }
}
```

Add table tests for missing semantics and prompt-version mismatch. Each error must list all affected unit IDs.

- [ ] **Step 2: Run tests and verify RED**

Run: `rtk go test ./internal/retrieval -run 'TestRerankUsesPersisted|TestRerankRejectsMissing|TestRerankRejectsStale' -count=1`

Expected: FAIL because current rerank still calls `rerank_extract.md`.

- [ ] **Step 3: Build judge inputs directly from stored semantics**

In `rerank`, preserve lifecycle filtering and candidate ID assignment, then bulk-load semantics. Collect missing and stale IDs before returning one deterministic, sorted integrity error.

```go
func buildRerankJudgeCandidate(candidateID, sourceTitle string, sem rerank.Semantics) rerankJudgeCandidate
```

Do not read unit source text in online rerank. Keep source title lookup because it remains part of the judge input.

- [ ] **Step 4: Replace raw-content batching with compact judge batching**

Implement:

```go
func splitRerankJudgeBatches(candidates []rerankJudgeCandidate, maxChars int) [][]rerankJudgeCandidate
func (s *Service) judgeRerankBatches(ctx context.Context, qc QueryContext, candidates []rerankJudgeCandidate) (map[string]string, error)
```

Measure each candidate with compact `json.Marshal(candidate)` and include commas/brackets in the running budget. An oversized candidate forms its own batch. Use `defaultRerankJudgeBatchMaxChars = 4000` and `defaultRerankJudgeConcurrency = 4`. Keep per-batch calls through the existing `judgeExtractedEvidence` validation path, but switch its payload from `json.MarshalIndent` to `json.Marshal`.

Remove `rerankBatch`, extraction result types, `rerankCandidateContent`, source-text formatting, and extraction config getters.

- [ ] **Step 5: Delete the online extraction prompt and run tests**

Delete `config/prompts/rerank_extract.md`; ingestion now owns `unit_semantics_extract.md`.

Run: `rtk go test ./internal/retrieval -count=1`

Expected: PASS, including existing direct/supporting/irrelevant behavior.

- [ ] **Step 6: Commit judge-only rerank**

```bash
rtk git add internal/retrieval/service.go internal/retrieval/service_test.go config/prompts/rerank_extract.md
rtk git commit -m "perf: use persisted semantics for online rerank"
```

---

### Task 5: Documentation and End-to-End Verification

**Files:**
- Modify: `docs/impl/mvp/unit.md`
- Modify: `docs/impl/mvp/retrieval.md`
- Modify: `internal/retrieval/handler_test.go`
- Modify: `internal/retrieval/fastpath_test.go`
- Modify: `internal/answer/fallback_test.go`

**Interfaces:**
- Consumes: completed Tasks 1-4.
- Produces: accurate operational documentation and a fully passing repository.

- [ ] **Step 1: Update implementation documentation**

Document that unit extraction now persists rerank semantics before publication, extraction failures preserve the previous generation, and online retrieval performs only judge calls. Remove statements claiming all raw candidates are extracted during online rerank.

- [ ] **Step 2: Find stale prompt and config references**

Run:

```bash
rtk rg -n 'rerank_extract\.md|RerankBatchMaxChars|RerankConcurrency|rerank_batch_max_chars|rerank_concurrency' --glob '!docs/superpowers/**'
```

Expected: no online extraction/config references. Update test fixtures to insert semantic rows or use the new config names; do not add fallback behavior.

- [ ] **Step 3: Run formatting and focused tests**

Run: `rtk gofmt -w internal/rerank internal/unit internal/retrieval internal/foundation/config`

Run: `rtk go test ./internal/unit ./internal/retrieval ./internal/foundation/db ./internal/foundation/config -count=1`

Expected: PASS.

- [ ] **Step 4: Run the complete suite**

Run: `rtk go test ./... -count=1`

Expected: PASS with no package failures.

- [ ] **Step 5: Verify migration completeness against a fresh database**

Run the existing database tests and a fresh server/test database initialization path. Query:

```sql
SELECT COUNT(*)
FROM knowledge_units u
LEFT JOIN unit_rerank_semantics s ON s.unit_id = u.unit_id
WHERE u.lifecycle = 'current' AND s.unit_id IS NULL;
```

Expected after re-upload in the release environment: `0`.

- [ ] **Step 6: Commit documentation and fixture updates**

```bash
rtk git add docs/impl/mvp/unit.md docs/impl/mvp/retrieval.md internal config
rtk git commit -m "docs: describe precomputed rerank semantics"
```

---

## Completion Checklist

- [ ] Every production behavior was preceded by a focused failing test.
- [ ] Ingestion extraction failure leaves the previous generation current and indexed.
- [ ] Every newly searchable unit has exactly one current-version semantic row.
- [ ] Online rerank performs zero extraction calls.
- [ ] Online judge batches are sized from compact structured JSON and honor configured concurrency.
- [ ] Missing and stale semantic errors list all affected unit IDs.
- [ ] `rtk go test ./... -count=1` passes.
