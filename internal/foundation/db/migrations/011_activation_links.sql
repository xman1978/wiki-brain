-- Migration 011: ActivationLink (docs/impl/v1/activation.md) + learning_results
-- (learning_results is created here, ahead of the Study module that will also
-- write to it, because ActivationLink.TransitionLink must write to it as part
-- of this module's own completion criteria; Study reuses the table as-is).

CREATE TABLE activation_links (
    link_id           TEXT PRIMARY KEY,
    question_terms    TEXT NOT NULL,
    subject_terms     TEXT NOT NULL DEFAULT '',
    intent_terms      TEXT NOT NULL DEFAULT '',
    audience          TEXT NOT NULL DEFAULT '',
    constraint_terms  TEXT NOT NULL DEFAULT '',
    scene             TEXT NOT NULL DEFAULT '',
    goal              TEXT NOT NULL DEFAULT '',
    point_id          TEXT NOT NULL REFERENCES knowledge_points(point_id),
    status            TEXT NOT NULL DEFAULT 'candidate',
    adopt_count       INTEGER NOT NULL DEFAULT 0,
    fail_count        INTEGER NOT NULL DEFAULT 0,
    last_used_at      DATETIME,
    created_from      TEXT NOT NULL DEFAULT '[]',
    status_changed_at DATETIME,
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(question_terms, point_id)
);

CREATE INDEX idx_al_status   ON activation_links(status);
CREATE INDEX idx_al_point_id ON activation_links(point_id);

CREATE TABLE learning_results (
    result_id       TEXT PRIMARY KEY,
    action          TEXT NOT NULL,
    object_type     TEXT NOT NULL,
    object_id       TEXT NOT NULL,
    reason          TEXT NOT NULL,
    event_ids       TEXT NOT NULL DEFAULT '[]',
    status          TEXT NOT NULL DEFAULT 'applied',
    confirmed_by    TEXT,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_lr_object ON learning_results(object_type, object_id);
CREATE INDEX idx_lr_status ON learning_results(status);
