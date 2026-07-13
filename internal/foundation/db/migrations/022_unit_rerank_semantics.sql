CREATE TABLE unit_rerank_semantics (
    unit_id TEXT PRIMARY KEY REFERENCES knowledge_units(unit_id) ON DELETE CASCADE,
    source_theme TEXT NOT NULL,
    content_theme TEXT NOT NULL,
    intent TEXT NOT NULL,
    object TEXT NOT NULL,
    scope TEXT NOT NULL,
    key_facts_json TEXT NOT NULL CHECK (
        json_valid(key_facts_json) AND json_type(key_facts_json) = 'array'
    ),
    prompt_version TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
