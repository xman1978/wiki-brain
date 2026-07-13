# Precomputed Rerank Semantics Design

## Goal

Move evidence semantic extraction out of the online question-answering path and into unit ingestion. Online rerank keeps the existing question-to-evidence judge behavior, but reads persisted unit semantics and only calls `rerank_judge.md`.

The expected result is roughly half as many sequential model calls on the online rerank critical path without changing recall, RRF ordering, or the `direct` / `supporting` / `irrelevant` decision rules.

## Scope

This change includes:

- Persisting one semantic extraction record for every searchable knowledge unit.
- Extracting semantics after unit gap filling and deduplication, before publishing the new unit generation.
- Batching ingestion extraction by source-text length and running batches concurrently.
- Loading persisted semantics during rerank, batching the structured judge input by serialized length, and running judge batches concurrently.
- Rejecting incomplete or stale semantic data instead of performing an online extraction fallback.
- Replacing the current shared rerank batch settings with separate extraction and judge settings.

This change does not include:

- An offline backfill command. Existing documents will be uploaded again before release.
- A query-time lazy extraction fallback.
- Changes to domain filtering, source filtering, outline/FTS recall, RRF merge, KPN expansion, or answer generation.
- Replacing the LLM judge with a cross-encoder or smaller model.

## Persistence

Add a `unit_rerank_semantics` table with a one-to-one relationship to `knowledge_units`:

```sql
CREATE TABLE unit_rerank_semantics (
    unit_id         TEXT PRIMARY KEY,
    source_theme    TEXT NOT NULL,
    content_theme   TEXT NOT NULL,
    intent          TEXT NOT NULL,
    object          TEXT NOT NULL,
    scope           TEXT NOT NULL,
    key_facts_json  TEXT NOT NULL,
    prompt_version  TEXT NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (unit_id) REFERENCES knowledge_units(unit_id) ON DELETE CASCADE
);
```

`key_facts_json` must contain a valid JSON array of strings. Store methods validate it when writing and decoding. `prompt_version` is the frontmatter version of the ingestion extraction prompt and identifies stale records after future prompt changes.

No migration-time data backfill runs. Until documents are uploaded again, legacy units have no semantic row and are considered incomplete by online rerank.

## Ingestion Flow

Semantic extraction runs against the final in-memory `unitCandidate` pool, after gap filling and deduplication have finalized unit IDs and line ranges.

1. Read each unit's final source-text range and the source title.
2. Split units into batches whose combined source-text length does not exceed `rerank_extract_batch_max_chars`. A single oversized unit is placed in its own batch.
3. Run at most `rerank_extract_concurrency` `unit_semantics_extract.md` calls concurrently.
4. Validate that every requested `unit_id` appears exactly once and that no unknown or duplicate IDs are returned.
5. If any batch fails or returns invalid data, cancel the remaining work and return an extraction error before superseding the old generation or writing new rows.
6. Once all batches succeed, perform the generation swap and new row insertion in a document-level database transaction: supersede the previous current units and points, insert the new units and points, and insert every semantic row.
7. Commit the transaction, then update Bleve for superseded and newly current units and points.

The extraction prompt contains no question, subject, intent, audience, or constraint fields. It receives only stable unit data: `unit_id`, source title, and source text. Its output fields remain `source_theme`, `content_theme`, `intent`, `object`, `scope`, and `key_facts`.

A source is not published partially. Semantic extraction failure leaves the previous generation current. Database transaction failure rolls back the complete generation change. Index publication happens only after the database commit; existing reindex behavior remains the recovery path for an index write failure.

## Online Rerank Flow

Recall and RRF continue to produce the same ordered candidate list and query-time candidate IDs (`c1`, `c2`, and so on).

1. Batch-load semantic rows for all candidate unit IDs in one store operation.
2. Require one semantic row per candidate and require its `prompt_version` to match the current supported extraction prompt version.
3. Combine each persisted semantic record with the query-time `candidate_id` and current source title to form the existing `rerankJudgeCandidate` shape.
4. Serialize candidates compactly and split batches according to `rerank_judge_batch_max_chars`. Batch accounting includes the complete serialized candidate object rather than the original unit text.
5. Run at most `rerank_judge_concurrency` `rerank_judge.md` calls concurrently.
6. Validate and merge roles by `candidate_id`, preserving the original candidate order and existing role handling.

Online rerank never calls the semantic extraction prompt. Missing, duplicate, undecodable, or stale semantic records produce a data-integrity error containing the affected unit IDs. The error is logged and returned; there is no silent candidate drop and no query-time extraction fallback.

## Configuration

Replace the current generic rerank batching fields with independently tunable ingestion and online settings:

```yaml
retrieval:
  rerank_extract_batch_max_chars: 4000
  rerank_extract_concurrency: 2
  rerank_judge_batch_max_chars: 4000
  rerank_judge_concurrency: 4
```

Positive configured values are used directly. Unset or non-positive values use the same defaults shown above. Character budgets are retained for this change because they match the current implementation and user requirement; token-aware budgeting remains a separate optimization.

## Error Handling

- Extraction request, schema, ID validation, or persistence failure aborts the new generation before publication.
- A context cancellation stops outstanding extraction or judge batches and returns the context error wrapped with the phase and batch information.
- Online semantic lookup reports all missing or stale unit IDs together so an upload problem is diagnosable in one request.
- Judge failure keeps the existing all-or-error behavior; partial batch roles are not returned.
- The LLM client's existing retry and JSON repair behavior remains unchanged.

## Testing

Store and migration tests cover semantic row insertion, bulk lookup, JSON decoding, prompt version persistence, transaction rollback, and cascade deletion.

Unit ingestion tests cover:

- extraction after final deduplicated ranges are known;
- length-based batching and configured concurrency;
- exact unit-ID validation;
- successful atomic publication of units, points, and semantics;
- extraction failure leaving the previous generation current and the new generation absent;
- transaction failure producing no partial generation or index publication.

Retrieval tests cover:

- rerank issuing judge calls without any extraction call;
- compact structured-input length batching and judge concurrency;
- role merge preserving candidate IDs and order;
- missing and stale semantics returning explicit data-integrity errors;
- unchanged filtering of `irrelevant` and retention of `direct` and `supporting` candidates.

The complete Go test suite must pass after implementation.

## Release Procedure

1. Deploy the schema and code in an environment where queries are not yet served.
2. Re-upload every source so all current units are produced by the new ingestion pipeline.
3. Verify there are no current knowledge units without a matching `unit_rerank_semantics` row and no prompt-version mismatches.
4. Enable question answering traffic.

