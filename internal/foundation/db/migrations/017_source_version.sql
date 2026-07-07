-- Migration 017: source version tracking (重传次数 + 历史版本文件下载/预览)

ALTER TABLE sources ADD COLUMN version INTEGER NOT NULL DEFAULT 1;

-- One row per version a reupload superseded — archived alongside the actual
-- files in data/sources/archived/<source_id>/<timestamp>/ (CompleteShadowSwap
-- already moves them there; this table just makes the snapshot queryable and
-- gives it a stable version number instead of only a timestamp directory).
CREATE TABLE source_versions (
    version_id    TEXT PRIMARY KEY,
    source_id     TEXT NOT NULL REFERENCES sources(source_id),
    version       INTEGER NOT NULL,
    file_name     TEXT NOT NULL,
    original_path TEXT NOT NULL,
    html_path     TEXT,
    markdown_path TEXT NOT NULL,
    archived_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_source_versions_source_id ON source_versions(source_id);
