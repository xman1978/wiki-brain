CREATE TABLE llm_providers (
    provider_id     TEXT PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    platform        TEXT NOT NULL,
    base_url        TEXT NOT NULL,
    api_key         TEXT NOT NULL DEFAULT '',
    timeout_seconds INTEGER NOT NULL DEFAULT 120,
    max_retries     INTEGER NOT NULL DEFAULT 3,
    models          TEXT NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE llm_purpose_bindings (
    purpose     TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL REFERENCES llm_providers(provider_id) ON DELETE RESTRICT
);
