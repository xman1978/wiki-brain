# Historical Evidence Backlink Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Historical evidence backlinks read archived markdown for superseded KUs, show those KUs in source detail, and stop reindexing superseded units against the new file after reupload swap.

**Architecture:** Add `ResolveMarkdownPathForUnit` on the source store/service; use it when slicing KU content for `GET /units/:id` and optional `GET /sources/:id/markdown?unit_id=`. Restrict `ReindexSource` to `lifecycle=current`. Update the web evidence/source UI to consume resolved content and `lifecycle=all` when opening from a cited unit.

**Tech Stack:** Go, SQLite, existing source_versions table, single-file `web/index.html`

**Spec:** `docs/superpowers/specs/2026-07-22-historical-evidence-backlink-design.md`

## Global Constraints

- No new migration unless required; reuse `source_versions` + `lifecycle_changed_at`
- Do not change Shadow Source swap main flow (reparent, supersede, archive, drop shadow)
- `deprecated` resolves to current `sources.markdown_path` (V1 simplification)
- Keep `evidence_snapshot` shape unchanged

---

### Task 1: ResolveMarkdownPathForUnit

**Files:**
- Modify: `internal/source/store.go`
- Modify: `internal/source/service.go` (thin wrapper if needed)
- Test: `internal/source/lifecycle_test.go` or new `internal/source/markdown_resolve_test.go`

**Produces:**
- `func (s *Store) ResolveMarkdownPathForUnit(sourceID, lifecycle string, lifecycleChangedAt sql.NullTime) (string, error)`
- Service helper that loads Source + optional version row and returns absolute or relative path consistent with `GetMarkdown`

- [ ] **Step 1: Write failing test** — superseded unit with `lifecycle_changed_at` before a `source_versions.archived_at` resolves to that version's `markdown_path`; current resolves to `sources.markdown_path`; no match falls back to current path

- [ ] **Step 2: Run test, expect FAIL**

- [ ] **Step 3: Implement ResolveMarkdownPathForUnit**

- [ ] **Step 4: Run test, expect PASS**

- [ ] **Step 5: Commit**

### Task 2: GET /units/:id returns content

**Files:**
- Modify: `internal/unit/service.go` — add content slice helper using source resolve
- Modify: `internal/unit/handler.go` — add `content` to unitDetail
- Test: `internal/unit/handler_test.go` / service test with archived markdown

- [ ] **Step 1: Failing test** — after swap, GET superseded unit content equals archived markdown slice

- [ ] **Step 2: Implement**

- [ ] **Step 3: Pass + commit**

### Task 3: GET /sources/:id/units lifecycle=all + markdown?unit_id=

**Files:**
- Modify: `internal/unit/store.go` — `GetUnitsBySourceIDFiltered` treat `all` / empty appropriately
- Modify: `internal/unit/handler.go` — accept `all`
- Modify: `internal/source/handler.go` + `service.go` — markdown query params
- Tests in handler/store tests

- [ ] **Step 1: Failing tests**

- [ ] **Step 2: Implement**

- [ ] **Step 3: Pass + commit**

### Task 4: ReindexSource only current

**Files:**
- Modify: `internal/unit/service.go` `ReindexSource`
- Test: extend `internal/unit/reupload_lifecycle_test.go` or source lifecycle test — after swap, superseded Bleve body must not equal new-file slice at old lines (or assert ReindexSource only loads current)

- [ ] **Step 1: Failing test**

- [ ] **Step 2: Implement filter to current only**

- [ ] **Step 3: Pass + commit**

### Task 5: Frontend historical evidence backlink

**Files:**
- Modify: `web/index.html` — `toggleUnitContext` use `GET /units/:id` content; `viewSource` accept optional unitId and load `lifecycle=all`; prefer markdown with `unit_id` when available

- [ ] **Step 1: Implement UI changes**

- [ ] **Step 2: Manual smoke / commit**

### Task 6: Verify

- [ ] **Step 1:** `go test ./internal/source/ ./internal/unit/ ./internal/retrieval/ -count=1`

- [ ] **Step 2:** Confirm against spec checklist
