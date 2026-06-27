CREATE TABLE IF NOT EXISTS sessions (
    session_id      TEXT PRIMARY KEY,
    title           TEXT NOT NULL DEFAULT '',
    state_snapshot  TEXT NOT NULL DEFAULT '{}',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS session_turns (
    turn_id      TEXT PRIMARY KEY,
    session_id   TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    turn_index   INTEGER NOT NULL,
    user_input   TEXT NOT NULL,
    action       TEXT NOT NULL,
    answer_id    TEXT,
    clarify_msg  TEXT,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_session_turns_session_id ON session_turns(session_id);
