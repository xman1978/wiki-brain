# File Management Unit Semantics Stage Design

## Goal

Add a visible processing stage named `提取知识单元语义` to the file management timeline.

The existing `知识单元构建` stage represents knowledge-unit splitting, extraction, gap-fill, and deduplication. The new stage represents rerank semantic extraction and publication. The UI should show the transition in real time during processing and should recover the same state after refresh or reconnect.

## User-Facing Timeline

The file management page displays these stages:

1. `文件上传`
2. `来源登记`
3. `Markdown 转换`
4. `知识单元构建`
5. `提取知识单元语义`

`知识单元构建` completes when the candidate knowledge-unit pool has been finalized after split/extract/gap-fill/dedup. `提取知识单元语义` starts immediately before rerank semantic extraction begins.

The final stage remains active until the full unit pipeline is complete, including semantic publication and the existing post-publication relation work that currently runs in `unit.Service.Extract`. This prevents the timeline from showing all stages completed while the source badge still says `处理中`.

## Architecture

Use both SSE progress events and persisted state:

- SSE progress events drive live UI transitions while the user is watching the page.
- Persisted state is the source of truth for page refresh, reconnect, polling fallback, and historical display.

This keeps the current progress model intact. SSE is a notification path, not the only state holder.

## Backend State

Extend `sources` with unit substage data:

- `units_stage`: current non-terminal unit substage. Values:
  - `pending`: unit work has not started.
  - `building`: splitting, extraction, gap-fill, and deduplication are running.
  - `semantics`: rerank semantic extraction, publication, and remaining unit pipeline work are running.
- `units_built_at`: timestamp when the system leaves `building` and enters `semantics`.

Terminal success and failure continue to use existing `units_status` and `units_completed_at`:

- `units_status=completed`: both `知识单元构建` and `提取知识单元语义` are complete. The semantic stage completion time uses `units_completed_at`.
- `units_status=failed` while `units_stage=building`: `知识单元构建` is failed and the semantic stage is waiting.
- `units_status=failed` while `units_stage=semantics`: `知识单元构建` is complete and `提取知识单元语义` is failed.

On retry or reupload processing start, reset `units_stage=building`, clear `units_built_at`, set `units_status=processing`, and clear terminal unit timestamps as needed.

For successful shadow reupload, copy or preserve the shadow row's unit stage timestamps when swapping it into the visible target source, so the file management page reflects the new version's completed pipeline.

## Backend Events

Add a progress step for the new phase, for example `unit_semantics`.

When the candidate pool is finalized and before `extractRerankSemantics` starts:

1. Persist `units_stage=semantics` and `units_built_at=CURRENT_TIMESTAMP`.
2. Emit an SSE progress event for `unit_semantics` with status `processing`.

When the unit pipeline succeeds:

1. Persist `units_status=completed`, `units_completed_at=CURRENT_TIMESTAMP`.
2. Emit `unit_semantics` as `done` or rely on the existing final progress close event if that is the current convention.

When the pipeline fails:

1. Persist `units_status=failed`.
2. Emit failure for the current step inferred from `units_stage`.

## Frontend Behavior

The timeline builder reads persisted fields from the source API:

- Existing status timestamps for upload/register/convert.
- `units_stage`, `units_built_at`, `units_status`, and `units_completed_at` for the two unit stages.

SSE updates may optimistically update the in-memory timeline while a source is processing. The existing polling path remains as fallback and reconciliation. If SSE and polling disagree, the persisted API response wins on the next poll.

## Testing

Backend tests should cover:

- New migration columns and default values.
- Starting unit work sets `units_status=processing` and `units_stage=building`.
- Transitioning to semantic extraction sets `units_stage=semantics` and `units_built_at`.
- Failure before the transition marks `知识单元构建` as failed.
- Failure after the transition marks `提取知识单元语义` as failed.
- Successful reupload preserves the new version's stage/timestamp state on the visible source.

Frontend tests or focused DOM-level checks should cover:

- Timeline includes `提取知识单元语义`.
- `building` state shows `知识单元构建` active and semantic stage waiting.
- `semantics` state shows `知识单元构建` complete and semantic stage active.
- `completed` state shows both unit stages complete.
- `failed` state marks the correct unit stage based on `units_stage`.

## Non-Goals

This change does not alter retrieval logic, rerank logic, rerank prompt behavior, batching, or concurrency. It only exposes the existing processing boundary in progress state and UI.
