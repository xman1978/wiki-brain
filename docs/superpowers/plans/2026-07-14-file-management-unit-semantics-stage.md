# File Management Unit Semantics Stage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show `提取知识单元语义` as a real-time and refresh-safe processing stage in the file management timeline.

**Architecture:** Add persisted unit substage fields to `sources`, expose them through source APIs, emit a new SSE progress event at the rerank semantic boundary, and teach the frontend timeline to split the current `知识单元构建` step into build and semantic stages. SSE drives live transitions while persisted API state remains authoritative.

**Tech Stack:** Go, SQLite migrations, net/http SSE, vanilla JavaScript in `web/index.html`, Go tests.

## Global Constraints

- Display label must be exactly `提取知识单元语义`.
- Existing `知识单元构建` means split/extract/gap-fill/dedup.
- New stage means rerank semantic extraction and publication, and remains active until the full unit pipeline finishes.
- Use both SSE progress events and persisted state.
- Persisted API state wins over SSE on poll/reload.
- Do not alter retrieval logic, rerank logic, rerank prompt behavior, batching, or concurrency.
- Shell commands in this repo must be prefixed with `rtk`.
- Manual file edits must use `apply_patch`.

---

## File Structure

- `internal/foundation/db/migrations/022_units_stage.sql`: add `units_stage` and `units_built_at`.
- `internal/source/store.go`: persist, read, reset, transition, and swap unit stage fields.
- `internal/source/store_test.go`: backend persistence tests.
- `internal/source/handler.go`: expose unit stage fields from list/get APIs.
- `internal/foundation/progress/progress.go`: add `StepUnitSemantics`.
- `internal/unit/service.go`: set the semantic boundary callback and emit semantic progress.
- `internal/unit/predup.go`: accept a callback that fires after candidate pooling and before rerank semantic extraction.
- `cmd/server/main.go`: start unit extraction in `building` state.
- `web/index.html`: add the timeline step and map persisted/SSE state to UI states.

---

### Task 1: Persist Unit Substage State

**Files:**
- Create: `internal/foundation/db/migrations/022_units_stage.sql`
- Modify: `internal/source/store.go`
- Test: `internal/source/store_test.go`

**Interfaces:**
- Produces: `Source.UnitsStage string`, `Source.UnitsBuiltAt sql.NullTime`
- Produces: `(*Store).StartUnitsProcessing(sourceID string) error`
- Produces: `(*Store).MarkUnitsSemanticsStarted(sourceID string) error`
- Changes: `(*Store).UpdateUnitsStatus(sourceID, unitsStatus string) error` clears terminal timestamp for non-terminal states and keeps completed/failed terminal behavior.

- [ ] **Step 1: Write failing store tests**

Add these tests to `internal/source/store_test.go`:

```go
func TestStoreUnitStageLifecycle(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	src := &Source{
		SourceID:     "src-stage-1",
		Title:        "Doc",
		Format:       "md",
		FileName:     "doc.md",
		OriginalPath: "/tmp/doc.md",
		MarkdownPath: "/tmp/doc.md",
		Status:       "completed",
	}
	if err := store.Create(src); err != nil {
		t.Fatalf("create source: %v", err)
	}

	got, err := store.GetByID(src.SourceID)
	if err != nil {
		t.Fatalf("get initial source: %v", err)
	}
	if got.UnitsStage != "pending" {
		t.Fatalf("initial units_stage = %q, want pending", got.UnitsStage)
	}
	if got.UnitsBuiltAt.Valid {
		t.Fatalf("initial units_built_at should be NULL")
	}

	if err := store.StartUnitsProcessing(src.SourceID); err != nil {
		t.Fatalf("start units processing: %v", err)
	}
	got, err = store.GetByID(src.SourceID)
	if err != nil {
		t.Fatalf("get building source: %v", err)
	}
	if got.UnitsStatus != "processing" || got.UnitsStage != "building" {
		t.Fatalf("after start units_status=%q units_stage=%q, want processing/building", got.UnitsStatus, got.UnitsStage)
	}
	if got.UnitsBuiltAt.Valid || got.UnitsCompletedAt.Valid {
		t.Fatalf("start should clear units_built_at and units_completed_at")
	}

	if err := store.MarkUnitsSemanticsStarted(src.SourceID); err != nil {
		t.Fatalf("mark semantics started: %v", err)
	}
	got, err = store.GetByID(src.SourceID)
	if err != nil {
		t.Fatalf("get semantics source: %v", err)
	}
	if got.UnitsStage != "semantics" {
		t.Fatalf("units_stage = %q, want semantics", got.UnitsStage)
	}
	if !got.UnitsBuiltAt.Valid {
		t.Fatalf("units_built_at should be set")
	}

	if err := store.UpdateUnitsStatus(src.SourceID, "completed"); err != nil {
		t.Fatalf("complete units: %v", err)
	}
	got, err = store.GetByID(src.SourceID)
	if err != nil {
		t.Fatalf("get completed source: %v", err)
	}
	if got.UnitsStatus != "completed" || got.UnitsStage != "semantics" {
		t.Fatalf("after complete units_status=%q units_stage=%q, want completed/semantics", got.UnitsStatus, got.UnitsStage)
	}
	if !got.UnitsCompletedAt.Valid {
		t.Fatalf("units_completed_at should be set")
	}
}
```

Add this assertion to the existing source list test or a new list-focused test:

```go
if list[0].UnitsStage != "semantics" || !list[0].UnitsBuiltAt.Valid {
	t.Fatalf("List()[0] units_stage=%q units_built_at.Valid=%v, want semantics/true", list[0].UnitsStage, list[0].UnitsBuiltAt.Valid)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
rtk go test ./internal/source -run 'TestStoreUnitStageLifecycle|TestStoreUnitsStatusTimestamps' -count=1
```

Expected: FAIL because `Source.UnitsStage`, `Source.UnitsBuiltAt`, `StartUnitsProcessing`, and `MarkUnitsSemanticsStarted` do not exist.

- [ ] **Step 3: Implement persistence**

Create `internal/foundation/db/migrations/022_units_stage.sql`:

```sql
-- Migration 022: unit substage progress for the file management timeline.
-- units_stage splits the existing unit extraction status into build vs
-- rerank semantic phases; units_built_at records the transition time.

ALTER TABLE sources ADD COLUMN units_stage TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE sources ADD COLUMN units_built_at DATETIME;
```

Modify `internal/source/store.go`:

```go
type Source struct {
	SourceID            string
	Title               string
	Format              string
	FileName            string
	OriginalPath        string
	HTMLPath            sql.NullString
	MarkdownPath        string
	Status              string
	UnitsStatus         string
	UnitsStage          string
	ErrorMsg            sql.NullString
	OutlineType         sql.NullString
	Summary             sql.NullString
	DomainID            sql.NullString
	WordCount           sql.NullInt64
	ShadowOf            sql.NullString
	Version             int
	CreatedAt           time.Time
	UpdatedAt           time.Time
	ProcessingStartedAt sql.NullTime
	CompletedAt         sql.NullTime
	UnitsCompletedAt    sql.NullTime
	UnitsBuiltAt        sql.NullTime
}
```

Update all `SELECT` and `Scan` lists in `GetByID`, `List`, and `GetShadowByTarget` to include `units_stage` immediately after `units_status`, and include `units_built_at` after `units_completed_at` when the query already reads unit timestamps.

Add:

```go
func (s *Store) StartUnitsProcessing(sourceID string) error {
	_, err := s.db.Exec(`UPDATE sources
		SET units_status = 'processing',
			units_stage = 'building',
			units_built_at = NULL,
			units_completed_at = NULL,
			updated_at = CURRENT_TIMESTAMP
		WHERE source_id = ?`, sourceID)
	if err != nil {
		return fmt.Errorf("source store: start units processing: %w", err)
	}
	return nil
}

func (s *Store) MarkUnitsSemanticsStarted(sourceID string) error {
	_, err := s.db.Exec(`UPDATE sources
		SET units_stage = 'semantics',
			units_built_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP
		WHERE source_id = ?`, sourceID)
	if err != nil {
		return fmt.Errorf("source store: mark units semantics started: %w", err)
	}
	return nil
}
```

Change `UpdateUnitsStatus` non-terminal branch to clear `units_completed_at`:

```go
_, err := s.db.Exec(`UPDATE sources SET units_status = ?, units_completed_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE source_id = ?`,
	unitsStatus, sourceID)
```

- [ ] **Step 4: Run tests to verify pass**

Run:

```bash
rtk go test ./internal/source -run 'TestStoreUnitStageLifecycle|TestStoreUnitsStatusTimestamps' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add internal/foundation/db/migrations/022_units_stage.sql internal/source/store.go internal/source/store_test.go
rtk git commit -m "feat: persist unit semantic stage"
```

---

### Task 2: Expose and Drive Backend Stage Progress

**Files:**
- Modify: `internal/source/handler.go`
- Modify: `cmd/server/main.go`
- Modify: `internal/foundation/progress/progress.go`
- Modify: `internal/unit/service.go`
- Modify: `internal/unit/predup.go`
- Test: `internal/source/store_test.go`

**Interfaces:**
- Consumes: `(*Store).StartUnitsProcessing(sourceID string) error`
- Consumes: `(*Store).MarkUnitsSemanticsStarted(sourceID string) error`
- Produces: `progress.StepUnitSemantics = "unit_semantics"`
- Produces API fields: `units_stage`, `units_built_at`

- [ ] **Step 1: Write failing API and swap tests**

Add or update tests in `internal/source/store_test.go` so `SwapShadowIntoTarget` preserves the shadow's completed unit stage:

```go
func TestSwapShadowIntoTargetCopiesUnitStageState(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	target := &Source{
		SourceID:     "target-stage",
		Title:        "Target",
		Format:       "md",
		FileName:     "target.md",
		OriginalPath: "/tmp/target.md",
		MarkdownPath: "/tmp/target.md",
		Status:       "completed",
	}
	shadowOf := sql.NullString{String: target.SourceID, Valid: true}
	shadow := &Source{
		SourceID:     "shadow-stage",
		Title:        "Shadow",
		Format:       "md",
		FileName:     "shadow.md",
		OriginalPath: "/tmp/shadow.md",
		MarkdownPath: "/tmp/shadow.md",
		Status:       "completed",
		ShadowOf:     shadowOf,
	}
	if err := store.Create(target); err != nil {
		t.Fatalf("create target: %v", err)
	}
	if err := store.Create(shadow); err != nil {
		t.Fatalf("create shadow: %v", err)
	}
	if err := store.StartUnitsProcessing(shadow.SourceID); err != nil {
		t.Fatalf("start shadow units: %v", err)
	}
	if err := store.MarkUnitsSemanticsStarted(shadow.SourceID); err != nil {
		t.Fatalf("mark shadow semantics: %v", err)
	}
	if err := store.UpdateUnitsStatus(shadow.SourceID, "completed"); err != nil {
		t.Fatalf("complete shadow units: %v", err)
	}

	if err := store.SwapShadowIntoTarget(shadow.SourceID, target.SourceID, "/tmp/shadow.md", sql.NullString{}); err != nil {
		t.Fatalf("swap shadow: %v", err)
	}
	got, err := store.GetByID(target.SourceID)
	if err != nil {
		t.Fatalf("get target: %v", err)
	}
	if got.UnitsStatus != "completed" || got.UnitsStage != "semantics" {
		t.Fatalf("target units_status=%q units_stage=%q, want completed/semantics", got.UnitsStatus, got.UnitsStage)
	}
	if !got.UnitsBuiltAt.Valid || !got.UnitsCompletedAt.Valid {
		t.Fatalf("target unit timestamps should be preserved/set after swap")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
rtk go test ./internal/source -run 'TestSwapShadowIntoTargetCopiesUnitStageState' -count=1
```

Expected: FAIL because swap does not copy `units_stage` and `units_built_at` from the shadow.

- [ ] **Step 3: Implement backend stage driving**

In `internal/foundation/progress/progress.go`, add:

```go
StepUnitSemantics = "unit_semantics"
```

In `cmd/server/main.go`, replace:

```go
if err := sourceStore.UpdateUnitsStatus(task.SourceID, "processing"); err != nil {
```

with:

```go
if err := sourceStore.StartUnitsProcessing(task.SourceID); err != nil {
```

In `internal/unit/predup.go`, change the signature:

```go
func (s *Service) extractSegmentsPreInsertDedup(ctx context.Context, sourceTitle, sourceID string, segments []Segment, mdLines []string, onSegmentDone func(), onBeforeSemantics func() error) error {
```

Before `extractRerankSemantics`, call:

```go
if onBeforeSemantics != nil {
	if err := onBeforeSemantics(); err != nil {
		return err
	}
}
```

In `internal/unit/service.go`, pass the new callback:

```go
semanticsStart := time.Now()
onBeforeSemantics := func() error {
	if err := s.sourceStore.MarkUnitsSemanticsStarted(sourceID); err != nil {
		return err
	}
	s.emit(sourceID, progress.Event{Step: progress.StepUnitSemantics, Status: progress.StatusStarted, Message: "提取知识单元语义"})
	semanticsStart = time.Now()
	return nil
}
if err := s.extractSegmentsPreInsertDedup(ctx, src.Title, sourceID, segments, mdLines, func() {
	done++
	s.emit(sourceID, progress.Event{Step: progress.StepUnitExtract, Status: progress.StatusCompleted, Message: fmt.Sprintf("提取知识单元 (%d/%d)", done, len(segments)), Current: done, Total: len(segments), ElapsedMs: time.Since(extractStart).Milliseconds()})
}, onBeforeSemantics); err != nil {
	return fmt.Errorf("unit: extract and publish: %w", err)
}
s.emit(sourceID, progress.Event{Step: progress.StepUnitSemantics, Status: progress.StatusCompleted, Message: "知识单元语义已发布", ElapsedMs: time.Since(semanticsStart).Milliseconds()})
```

In `internal/source/handler.go`, add `UnitsStage` and `UnitsBuiltAt` fields to the list item response and add `units_stage` / `units_built_at` to `getSource`.

In `internal/source/store.go`, update `SwapShadowIntoTarget` to read `units_stage`, `units_built_at`, and `units_completed_at` from the shadow and write them to the target. Keep `units_status='completed'`.

- [ ] **Step 4: Run focused tests**

Run:

```bash
rtk go test ./internal/source ./internal/unit -run 'TestSwapShadowIntoTargetCopiesUnitStageState|TestReupload' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
rtk git add cmd/server/main.go internal/foundation/progress/progress.go internal/source/handler.go internal/source/store.go internal/source/store_test.go internal/unit/service.go internal/unit/predup.go
rtk git commit -m "feat: emit unit semantics progress"
```

---

### Task 3: Update File Management Timeline

**Files:**
- Modify: `web/index.html`

**Interfaces:**
- Consumes API fields: `units_stage`, `units_built_at`, `units_status`, `units_completed_at`
- Consumes SSE step: `unit_semantics`

- [ ] **Step 1: Add the frontend failing check**

Because `web/index.html` has no dedicated JS test harness, use this shell check as the red test before editing:

```bash
rtk rg -n "提取知识单元语义|unit_semantics|units_built_at|units_stage" web/index.html
```

Expected: FAIL/no matches for the new stage label and fields.

- [ ] **Step 2: Implement timeline mapping**

In `web/index.html`, add the new step:

```js
var TL_STEPS = [
  { id:'upload',    label:'文件上传' },
  { id:'register',  label:'来源登记' },
  { id:'convert',   label:'Markdown 转换' },
  { id:'units',     label:'知识单元构建' },
  { id:'semantics', label:'提取知识单元语义' }
];
```

Update `showTimeline` so:

- `units_pending` marks `units` active and `semantics` waiting.
- New phase `semantics_pending` marks `units` done with `units_built_at` and `semantics` active.
- `units_failed` marks `units` failed when `units_stage !== 'semantics'`, otherwise marks `units` done and `semantics` failed.
- `completed` marks both stages done; `units` time uses `units_built_at`, `semantics` time uses `units_completed_at`.

Update `buildStepsFromSource(d)` with the same rules:

```js
} else if (s.id === 'units') {
  if (d.status !== 'completed') { state = 'wait'; }
  else if (d.units_status === 'completed') { state = 'done'; time = fmtTime(d.units_built_at || d.units_completed_at); }
  else if (d.units_status === 'failed' && d.units_stage !== 'semantics') { state = 'fail'; time = fmtTime(d.units_completed_at); }
  else if (d.units_stage === 'semantics') { state = 'done'; time = fmtTime(d.units_built_at); }
  else { state = 'active'; }
} else if (s.id === 'semantics') {
  if (d.status !== 'completed') { state = 'wait'; }
  else if (d.units_status === 'completed') { state = 'done'; time = fmtTime(d.units_completed_at); }
  else if (d.units_status === 'failed' && d.units_stage === 'semantics') { state = 'fail'; time = fmtTime(d.units_completed_at); }
  else if (d.units_stage === 'semantics') { state = 'active'; }
  else { state = 'wait'; }
}
```

Update `pollSourceTimeline`:

```js
if (unitsStatus === 'failed') {
  showTimeline(title, 'units_failed', r.data, uploadStart);
  loadSources();
  return;
}
if (((r.data && r.data.units_stage) || 'pending') === 'semantics') {
  showTimeline(title, 'semantics_pending', r.data, uploadStart);
} else {
  showTimeline(title, 'units_pending', r.data, uploadStart);
}
```

If there is existing SSE upload progress handling in this file, map `unit_semantics` to the same `semantics_pending` UI transition. Do not create a second timeline model; call the same helpers used by polling.

- [ ] **Step 3: Run frontend checks**

Run:

```bash
rtk rg -n "提取知识单元语义|unit_semantics|units_built_at|units_stage" web/index.html
```

Expected: PASS with matches for the label, persisted fields, and SSE step.

Run:

```bash
rtk go test ./internal/source ./internal/unit -count=1
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
rtk git add web/index.html
rtk git commit -m "feat: show unit semantics timeline stage"
```

---

### Task 4: Final Verification

**Files:**
- No planned source edits.

**Interfaces:**
- Verifies all produced interfaces from Tasks 1-3.

- [ ] **Step 1: Run full Go tests**

Run:

```bash
rtk go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 2: Inspect final diff**

Run:

```bash
rtk git status --short
rtk git log --oneline -5
```

Expected: clean working tree except intentional untracked subagent scratch files if any; recent commits include the three feature commits.

- [ ] **Step 3: Commit final verification note only if files changed**

If no files changed, do not create an empty commit.
