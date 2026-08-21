PRAGMA foreign_keys = ON;

BEGIN IMMEDIATE;

CREATE TABLE schema_metadata (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    initialized_at TEXT NOT NULL
) STRICT;

INSERT INTO schema_metadata (singleton, schema_version, initialized_at)
VALUES (1, 1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

CREATE TABLE memory_spaces (
    space_id TEXT PRIMARY KEY,
    parent_space_id TEXT REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    space_type TEXT NOT NULL CHECK (space_type IN ('user', 'agent', 'application', 'project', 'task', 'session')),
    external_id TEXT NOT NULL,
    display_name TEXT NOT NULL,
    created_at TEXT NOT NULL,
    CHECK (
        (space_type = 'user' AND parent_space_id IS NULL) OR
        (space_type <> 'user' AND parent_space_id IS NOT NULL)
    ),
    UNIQUE (parent_space_id, space_type, external_id)
) STRICT;

CREATE UNIQUE INDEX one_user_root
ON memory_spaces(space_type)
WHERE space_type = 'user';

CREATE INDEX memory_spaces_parent_idx
ON memory_spaces(parent_space_id, space_type, external_id);

CREATE TABLE raw_contents (
    content_id TEXT PRIMARY KEY,
    content_kind TEXT NOT NULL CHECK (content_kind IN ('text', 'json', 'reference')),
    content_text TEXT,
    reference_uri TEXT,
    media_type TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    declared_size INTEGER CHECK (declared_size IS NULL OR declared_size >= 0),
    declared_hash TEXT,
    created_at TEXT NOT NULL,
    CHECK (
        (content_kind IN ('text', 'json') AND content_text IS NOT NULL AND reference_uri IS NULL) OR
        (content_kind = 'reference' AND content_text IS NULL AND reference_uri IS NOT NULL)
    ),
    CHECK (content_kind <> 'json' OR json_valid(content_text))
) STRICT;

CREATE TABLE raw_events (
    event_id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    content_id TEXT NOT NULL REFERENCES raw_contents(content_id) ON DELETE RESTRICT,
    source_type TEXT NOT NULL CHECK (source_type IN ('message', 'conversation', 'tool_call', 'tool_result', 'file_reference', 'collector_event', 'manual')),
    source_name TEXT NOT NULL,
    source_event_id TEXT,
    caller_id TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    received_at TEXT NOT NULL,
    processing_status TEXT NOT NULL DEFAULT 'stored' CHECK (processing_status IN ('stored', 'processing', 'processed', 'failed')),
    metadata_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata_json)),
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX raw_events_space_time_idx
ON raw_events(space_id, occurred_at DESC, event_id DESC);

CREATE INDEX raw_events_source_time_idx
ON raw_events(source_name, occurred_at DESC, event_id DESC);

CREATE INDEX raw_events_operation_idx
ON raw_events(operation_id);

CREATE TABLE memories (
    memory_id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    memory_type TEXT NOT NULL CHECK (memory_type IN ('fact', 'preference', 'constraint', 'decision', 'task', 'event', 'project_context', 'agent_experience')),
    current_revision_id TEXT NOT NULL,
    current_state TEXT NOT NULL CHECK (current_state IN ('candidate', 'active', 'contested', 'archived', 'expired', 'retracted', 'deleted')),
    frozen INTEGER NOT NULL DEFAULT 0 CHECK (frozen IN (0, 1)),
    sensitivity TEXT NOT NULL DEFAULT 'normal' CHECK (sensitivity IN ('normal', 'sensitive')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (memory_id, current_revision_id)
        REFERENCES memory_revisions(memory_id, revision_id)
        ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE memory_revisions (
    revision_id TEXT PRIMARY KEY,
    memory_id TEXT NOT NULL REFERENCES memories(memory_id) ON DELETE RESTRICT,
    parent_revision_id TEXT,
    restored_from_revision_id TEXT,
    content TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    valid_from TEXT,
    valid_until TEXT,
    confidence REAL CHECK (confidence IS NULL OR (confidence >= 0.0 AND confidence <= 1.0)),
    created_by TEXT NOT NULL,
    processing_version TEXT NOT NULL,
    processor_identity TEXT,
    created_at TEXT NOT NULL,
    CHECK (valid_until IS NULL OR valid_from IS NULL OR valid_until > valid_from),
    UNIQUE (memory_id, revision_id),
    FOREIGN KEY (memory_id, parent_revision_id)
        REFERENCES memory_revisions(memory_id, revision_id)
        ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (memory_id, restored_from_revision_id)
        REFERENCES memory_revisions(memory_id, revision_id)
        ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE INDEX memory_revisions_memory_time_idx
ON memory_revisions(memory_id, created_at DESC, revision_id DESC);

CREATE TRIGGER memory_revisions_no_update
BEFORE UPDATE ON memory_revisions
BEGIN
    SELECT RAISE(ABORT, 'memory revisions are immutable');
END;

CREATE TRIGGER memory_revisions_no_delete
BEFORE DELETE ON memory_revisions
BEGIN
    SELECT RAISE(ABORT, 'memory revisions are immutable');
END;

CREATE TABLE memory_state_events (
    state_event_id TEXT PRIMARY KEY,
    memory_id TEXT NOT NULL REFERENCES memories(memory_id) ON DELETE RESTRICT,
    revision_id TEXT NOT NULL REFERENCES memory_revisions(revision_id) ON DELETE RESTRICT,
    operation_id TEXT NOT NULL,
    from_state TEXT CHECK (from_state IS NULL OR from_state IN ('candidate', 'active', 'contested', 'archived', 'expired', 'retracted', 'deleted')),
    to_state TEXT NOT NULL CHECK (to_state IN ('candidate', 'active', 'contested', 'archived', 'expired', 'retracted', 'deleted')),
    from_frozen INTEGER CHECK (from_frozen IS NULL OR from_frozen IN (0, 1)),
    to_frozen INTEGER NOT NULL CHECK (to_frozen IN (0, 1)),
    reason TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY (memory_id, revision_id)
        REFERENCES memory_revisions(memory_id, revision_id)
        ON DELETE RESTRICT
) STRICT;

CREATE INDEX memory_state_events_memory_time_idx
ON memory_state_events(memory_id, created_at DESC, state_event_id DESC);

CREATE TRIGGER memory_state_events_no_update
BEFORE UPDATE ON memory_state_events
BEGIN
    SELECT RAISE(ABORT, 'memory state events are append-only');
END;

CREATE TRIGGER memory_state_events_no_delete
BEFORE DELETE ON memory_state_events
BEGIN
    SELECT RAISE(ABORT, 'memory state events are append-only');
END;

CREATE TABLE memory_evidence (
    evidence_id TEXT PRIMARY KEY,
    revision_id TEXT NOT NULL REFERENCES memory_revisions(revision_id) ON DELETE RESTRICT,
    event_id TEXT NOT NULL REFERENCES raw_events(event_id) ON DELETE RESTRICT,
    evidence_type TEXT NOT NULL CHECK (evidence_type IN ('direct_statement', 'tool_result', 'manual_reference')),
    start_offset INTEGER CHECK (start_offset IS NULL OR start_offset >= 0),
    end_offset INTEGER CHECK (end_offset IS NULL OR end_offset >= 0),
    created_at TEXT NOT NULL,
    CHECK ((start_offset IS NULL AND end_offset IS NULL) OR (start_offset IS NOT NULL AND end_offset IS NOT NULL AND end_offset > start_offset)),
    UNIQUE (revision_id, event_id, start_offset, end_offset)
) STRICT;

CREATE INDEX memory_evidence_event_idx
ON memory_evidence(event_id, revision_id);

CREATE UNIQUE INDEX memory_evidence_once
ON memory_evidence(revision_id, event_id, COALESCE(start_offset, -1), COALESCE(end_offset, -1));

CREATE TABLE memory_projections (
    projection_id INTEGER PRIMARY KEY,
    memory_id TEXT NOT NULL UNIQUE REFERENCES memories(memory_id) ON DELETE RESTRICT,
    revision_id TEXT NOT NULL REFERENCES memory_revisions(revision_id) ON DELETE RESTRICT,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    memory_type TEXT NOT NULL,
    current_state TEXT NOT NULL,
    searchable INTEGER NOT NULL CHECK (searchable IN (0, 1)),
    display_content TEXT NOT NULL,
    valid_from TEXT,
    valid_until TEXT,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (memory_id, revision_id)
        REFERENCES memory_revisions(memory_id, revision_id)
        ON DELETE RESTRICT
) STRICT;

CREATE INDEX memory_projections_scope_idx
ON memory_projections(space_id, searchable, memory_type, current_state, memory_id);

CREATE VIRTUAL TABLE memory_fts USING fts5(
    memory_id UNINDEXED,
    revision_id UNINDEXED,
    content,
    tokenize = 'unicode61'
);

CREATE TABLE jobs (
    job_id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL,
    job_type TEXT NOT NULL CHECK (job_type IN ('extract_memory', 'rebuild_fts')),
    target_type TEXT NOT NULL CHECK (target_type IN ('raw_event', 'index')),
    target_id TEXT NOT NULL,
    processing_version TEXT NOT NULL,
    processor_identity TEXT,
    status TEXT NOT NULL CHECK (status IN ('pending', 'waiting', 'running', 'retrying', 'succeeded', 'failed', 'cancelled', 'expired')),
    priority INTEGER NOT NULL DEFAULT 0,
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    max_attempts INTEGER NOT NULL CHECK (max_attempts > 0),
    next_run_at TEXT NOT NULL,
    lease_owner TEXT,
    lease_until TEXT,
    lease_generation INTEGER NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
    cancel_requested_at TEXT,
    result_code TEXT,
    result_json TEXT CHECK (result_json IS NULL OR json_valid(result_json)),
    last_error_code TEXT,
    last_error_message TEXT,
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    expires_at TEXT,
    CHECK (
        (status = 'running' AND lease_owner IS NOT NULL AND lease_until IS NOT NULL) OR
        (status <> 'running' AND lease_owner IS NULL AND lease_until IS NULL)
    )
) STRICT;

CREATE UNIQUE INDEX jobs_effective_identity
ON jobs(job_type, target_type, target_id, processing_version)
WHERE status IN ('pending', 'waiting', 'running', 'retrying');

CREATE INDEX jobs_claim_idx
ON jobs(status, next_run_at, priority DESC, created_at, job_id);

CREATE INDEX jobs_lease_idx
ON jobs(status, lease_until)
WHERE status = 'running';

CREATE TABLE job_attempts (
    attempt_id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES jobs(job_id) ON DELETE RESTRICT,
    lease_generation INTEGER NOT NULL,
    worker_id TEXT NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    outcome TEXT CHECK (outcome IS NULL OR outcome IN ('succeeded', 'retrying', 'failed', 'cancelled', 'lease_lost')),
    result_code TEXT,
    error_code TEXT,
    error_message TEXT,
    UNIQUE (job_id, lease_generation)
) STRICT;

CREATE TABLE idempotency_records (
    idempotency_id TEXT PRIMARY KEY,
    caller_id TEXT NOT NULL,
    http_method TEXT NOT NULL,
    normalized_route TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    canonicalization_version INTEGER NOT NULL DEFAULT 1,
    operation_id TEXT NOT NULL,
    response_status INTEGER NOT NULL,
    response_body TEXT NOT NULL CHECK (json_valid(response_body)),
    created_at TEXT NOT NULL,
    UNIQUE (caller_id, http_method, normalized_route, idempotency_key)
) STRICT;

CREATE TABLE audit_events (
    audit_id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    caller_id TEXT NOT NULL,
    action TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id TEXT,
    outcome TEXT NOT NULL,
    reason TEXT,
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX audit_operation_idx
ON audit_events(operation_id, created_at, audit_id);

CREATE TABLE index_metadata (
    index_name TEXT PRIMARY KEY,
    index_version INTEGER NOT NULL CHECK (index_version > 0),
    state TEXT NOT NULL CHECK (state IN ('ready', 'degraded', 'rebuilding')),
    source_revision_count INTEGER NOT NULL DEFAULT 0 CHECK (source_revision_count >= 0),
    updated_at TEXT NOT NULL
) STRICT;

INSERT INTO index_metadata (index_name, index_version, state, source_revision_count, updated_at)
VALUES ('memory_fts', 1, 'ready', 0, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

COMMIT;
