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

CREATE TABLE source_streams (
    source_stream_id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    source_type TEXT NOT NULL CHECK (source_type IN ('message', 'conversation', 'tool_call', 'tool_result', 'file_reference', 'collector_event', 'manual')),
    source_name TEXT NOT NULL,
    source_stream_key TEXT NOT NULL CHECK (length(source_stream_key) > 0),
    created_at TEXT NOT NULL,
    UNIQUE (space_id, source_stream_key)
) STRICT;

CREATE INDEX source_streams_space_idx
ON source_streams(space_id, source_type, source_stream_key);

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
    source_stream_id TEXT REFERENCES source_streams(source_stream_id) ON DELETE RESTRICT,
    sequence INTEGER CHECK (sequence IS NULL OR sequence >= 0),
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

CREATE UNIQUE INDEX raw_events_stream_sequence
ON raw_events(source_stream_id, sequence)
WHERE source_stream_id IS NOT NULL;

CREATE INDEX raw_events_stream_order_idx
ON raw_events(source_stream_id, sequence, occurred_at, event_id)
WHERE source_stream_id IS NOT NULL;

CREATE TABLE memories (
    memory_id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    current_revision_id TEXT NOT NULL,
    current_state TEXT NOT NULL CHECK (current_state IN ('active', 'contested', 'superseded', 'archived', 'expired', 'retracted', 'deleted')),
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
    valid_from TEXT,
    valid_until TEXT,
    confidence REAL NOT NULL CHECK (confidence >= 0.0 AND confidence <= 1.0),
    kind TEXT NOT NULL CHECK (kind IN ('fact', 'event', 'preference', 'plan', 'procedure', 'scene', 'topic', 'timeline')),
    display_content TEXT NOT NULL,
    canonical_content TEXT NOT NULL,
    canonical_hash TEXT NOT NULL,
    event_type TEXT,
    event_time_start TEXT,
    event_time_end TEXT,
    time_precision TEXT CHECK (time_precision IS NULL OR time_precision IN ('exact', 'day', 'month', 'year', 'approximate', 'unknown')),
    time_source TEXT,
    source_speaker TEXT,
    language TEXT,
    sensitivity TEXT NOT NULL DEFAULT 'normal' CHECK (sensitivity IN ('normal', 'sensitive')),
    specificity REAL NOT NULL CHECK (specificity >= 0.0 AND specificity <= 1.0),
    created_by TEXT NOT NULL,
    processing_version TEXT NOT NULL,
    processor_identity TEXT,
    created_at TEXT NOT NULL,
    CHECK (valid_until IS NULL OR valid_from IS NULL OR valid_until > valid_from),
    CHECK (event_time_end IS NULL OR event_time_start IS NULL OR event_time_end > event_time_start),
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

CREATE TABLE revision_subject_entities (
    revision_id TEXT NOT NULL REFERENCES memory_revisions(revision_id) ON DELETE RESTRICT,
    entity_order INTEGER NOT NULL CHECK (entity_order >= 0),
    entity_id TEXT NOT NULL,
    PRIMARY KEY (revision_id, entity_order),
    UNIQUE (revision_id, entity_id)
) WITHOUT ROWID, STRICT;

CREATE TABLE revision_object_entities (
    revision_id TEXT NOT NULL REFERENCES memory_revisions(revision_id) ON DELETE RESTRICT,
    entity_order INTEGER NOT NULL CHECK (entity_order >= 0),
    entity_id TEXT NOT NULL,
    PRIMARY KEY (revision_id, entity_order),
    UNIQUE (revision_id, entity_id)
) WITHOUT ROWID, STRICT;

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
    from_state TEXT CHECK (from_state IS NULL OR from_state IN ('active', 'contested', 'superseded', 'archived', 'expired', 'retracted', 'deleted')),
    to_state TEXT NOT NULL CHECK (to_state IN ('active', 'contested', 'superseded', 'archived', 'expired', 'retracted', 'deleted')),
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
    role TEXT NOT NULL DEFAULT 'direct' CHECK (role IN ('direct', 'derived', 'temporal_anchor', 'context')),
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

CREATE TRIGGER memory_evidence_no_update
BEFORE UPDATE ON memory_evidence
BEGIN
    SELECT RAISE(ABORT, 'memory evidence is append-only');
END;

CREATE TRIGGER memory_evidence_no_delete
BEFORE DELETE ON memory_evidence
BEGIN
    SELECT RAISE(ABORT, 'memory evidence is append-only');
END;

CREATE TABLE revision_attributes (
    revision_id TEXT NOT NULL REFERENCES memory_revisions(revision_id) ON DELETE RESTRICT,
    attribute_key TEXT NOT NULL CHECK (length(attribute_key) > 0),
    attribute_value TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (revision_id, attribute_key)
) WITHOUT ROWID, STRICT;

CREATE INDEX revision_attributes_key_idx
ON revision_attributes(attribute_key, attribute_value, revision_id);

CREATE TABLE memory_quality (
    memory_id TEXT NOT NULL,
    revision_id TEXT NOT NULL,
    evidence_strength REAL NOT NULL CHECK (evidence_strength >= 0.0 AND evidence_strength <= 1.0),
    extraction_confidence REAL NOT NULL CHECK (extraction_confidence >= 0.0 AND extraction_confidence <= 1.0),
    specificity REAL NOT NULL CHECK (specificity >= 0.0 AND specificity <= 1.0),
    temporal_precision REAL NOT NULL CHECK (temporal_precision >= 0.0 AND temporal_precision <= 1.0),
    source_diversity REAL NOT NULL CHECK (source_diversity >= 0.0 AND source_diversity <= 1.0),
    review_status REAL NOT NULL CHECK (review_status >= 0.0 AND review_status <= 1.0),
    contradiction_penalty REAL NOT NULL CHECK (contradiction_penalty >= 0.0 AND contradiction_penalty <= 1.0),
    duplicate_density REAL NOT NULL CHECK (duplicate_density >= 0.0 AND duplicate_density <= 1.0),
    quality REAL NOT NULL CHECK (quality >= 0.0 AND quality <= 1.0),
    generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0),
    algorithm_version TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (memory_id, revision_id),
    FOREIGN KEY (memory_id, revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) WITHOUT ROWID, STRICT;

CREATE INDEX memory_quality_generation_idx
ON memory_quality(generation, quality, memory_id);

CREATE TABLE duplicate_clusters (
    cluster_id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    representative_memory_id TEXT NOT NULL,
    representative_revision_id TEXT NOT NULL,
    generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0),
    member_count INTEGER NOT NULL DEFAULT 0 CHECK (member_count >= 0),
    algorithm_version TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (representative_memory_id, representative_revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX duplicate_clusters_space_idx
ON duplicate_clusters(space_id, generation, cluster_id);

CREATE TABLE duplicate_cluster_members (
    cluster_id TEXT NOT NULL REFERENCES duplicate_clusters(cluster_id) ON DELETE RESTRICT,
    memory_id TEXT NOT NULL,
    revision_id TEXT NOT NULL,
    reason_code TEXT NOT NULL CHECK (reason_code IN ('same_logical_memory', 'shared_primary_evidence', 'equivalent_canonical_tuple', 'semantic_time_compatible', 'aggregate_provenance')),
    similarity REAL CHECK (similarity IS NULL OR (similarity >= 0.0 AND similarity <= 1.0)),
    created_at TEXT NOT NULL,
    PRIMARY KEY (cluster_id, memory_id),
    FOREIGN KEY (memory_id, revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) WITHOUT ROWID, STRICT;

CREATE TABLE candidates (
    candidate_id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES raw_events(event_id) ON DELETE RESTRICT,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    memory_type TEXT NOT NULL CHECK (memory_type IN ('fact', 'event', 'preference', 'plan', 'procedure', 'scene', 'topic', 'timeline')),
    content TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    valid_from TEXT,
    valid_until TEXT,
    confidence REAL CHECK (confidence IS NULL OR (confidence >= 0.0 AND confidence <= 1.0)),
    sensitivity TEXT NOT NULL DEFAULT 'normal' CHECK (sensitivity IN ('normal', 'sensitive')),
    suggestion_kind TEXT NOT NULL CHECK (suggestion_kind IN ('new', 'duplicate', 'supplement', 'replacement', 'conflict')),
    processor_identity TEXT NOT NULL,
    provider_identity TEXT,
    model_identity TEXT,
    processing_version TEXT NOT NULL,
    specification_hash TEXT NOT NULL,
    review_state TEXT NOT NULL DEFAULT 'pending' CHECK (review_state IN ('pending', 'confirmed', 'rejected')),
    confirmed_memory_id TEXT,
    confirmed_revision_id TEXT,
    created_at TEXT NOT NULL,
    reviewed_at TEXT,
    CHECK (valid_until IS NULL OR valid_from IS NULL OR valid_until > valid_from),
    CHECK (
        (review_state = 'pending' AND reviewed_at IS NULL AND confirmed_memory_id IS NULL AND confirmed_revision_id IS NULL) OR
        (review_state = 'rejected' AND reviewed_at IS NOT NULL AND confirmed_memory_id IS NULL AND confirmed_revision_id IS NULL) OR
        (review_state = 'confirmed' AND reviewed_at IS NOT NULL AND confirmed_memory_id IS NOT NULL AND confirmed_revision_id IS NOT NULL)
    ),
    UNIQUE (event_id, processing_version, specification_hash),
    FOREIGN KEY (confirmed_memory_id, confirmed_revision_id)
        REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX candidates_review_queue_idx
ON candidates(space_id, review_state, created_at DESC, candidate_id DESC);

CREATE INDEX candidates_processor_idx
ON candidates(processor_identity, processing_version, created_at DESC, candidate_id DESC);

CREATE TRIGGER candidates_immutable_fields
BEFORE UPDATE ON candidates
WHEN OLD.event_id IS NOT NEW.event_id
  OR OLD.space_id IS NOT NEW.space_id
  OR OLD.memory_type IS NOT NEW.memory_type
  OR OLD.content IS NOT NEW.content
  OR OLD.content_hash IS NOT NEW.content_hash
  OR OLD.valid_from IS NOT NEW.valid_from
  OR OLD.valid_until IS NOT NEW.valid_until
  OR OLD.confidence IS NOT NEW.confidence
  OR OLD.sensitivity IS NOT NEW.sensitivity
  OR OLD.suggestion_kind IS NOT NEW.suggestion_kind
  OR OLD.processor_identity IS NOT NEW.processor_identity
  OR OLD.provider_identity IS NOT NEW.provider_identity
  OR OLD.model_identity IS NOT NEW.model_identity
  OR OLD.processing_version IS NOT NEW.processing_version
  OR OLD.specification_hash IS NOT NEW.specification_hash
  OR OLD.created_at IS NOT NEW.created_at
BEGIN
    SELECT RAISE(ABORT, 'candidate specification is immutable');
END;

CREATE TRIGGER candidates_terminal_state
BEFORE UPDATE ON candidates
WHEN OLD.review_state <> 'pending'
  OR NEW.review_state NOT IN ('confirmed', 'rejected')
BEGIN
    SELECT RAISE(ABORT, 'candidate review state is terminal');
END;

CREATE TRIGGER candidates_no_delete
BEFORE DELETE ON candidates
BEGIN
    SELECT RAISE(ABORT, 'candidates are immutable');
END;

CREATE TABLE candidate_evidence (
    evidence_id TEXT PRIMARY KEY,
    candidate_id TEXT NOT NULL REFERENCES candidates(candidate_id) ON DELETE RESTRICT,
    event_id TEXT NOT NULL REFERENCES raw_events(event_id) ON DELETE RESTRICT,
    evidence_type TEXT NOT NULL CHECK (evidence_type IN ('direct_statement', 'tool_result', 'manual_reference')),
    role TEXT NOT NULL DEFAULT 'direct' CHECK (role IN ('direct', 'derived', 'temporal_anchor', 'context')),
    start_offset INTEGER CHECK (start_offset IS NULL OR start_offset >= 0),
    end_offset INTEGER CHECK (end_offset IS NULL OR end_offset >= 0),
    created_at TEXT NOT NULL,
    CHECK ((start_offset IS NULL AND end_offset IS NULL) OR (start_offset IS NOT NULL AND end_offset IS NOT NULL AND end_offset > start_offset))
) STRICT;

CREATE UNIQUE INDEX candidate_evidence_once
ON candidate_evidence(candidate_id, event_id, COALESCE(start_offset, -1), COALESCE(end_offset, -1));

CREATE TRIGGER candidate_evidence_no_update
BEFORE UPDATE ON candidate_evidence
BEGIN
    SELECT RAISE(ABORT, 'candidate evidence is immutable');
END;

CREATE TRIGGER candidate_evidence_no_delete
BEFORE DELETE ON candidate_evidence
BEGIN
    SELECT RAISE(ABORT, 'candidate evidence is immutable');
END;

CREATE TABLE candidate_related_memories (
    candidate_id TEXT NOT NULL REFERENCES candidates(candidate_id) ON DELETE RESTRICT,
    memory_id TEXT NOT NULL REFERENCES memories(memory_id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    PRIMARY KEY (candidate_id, memory_id)
) WITHOUT ROWID, STRICT;

CREATE TRIGGER candidate_related_memories_no_update
BEFORE UPDATE ON candidate_related_memories
BEGIN
    SELECT RAISE(ABORT, 'candidate related memories are immutable');
END;

CREATE TRIGGER candidate_related_memories_no_delete
BEFORE DELETE ON candidate_related_memories
BEGIN
    SELECT RAISE(ABORT, 'candidate related memories are immutable');
END;

CREATE TABLE candidate_reviews (
    review_id TEXT PRIMARY KEY,
    candidate_id TEXT NOT NULL REFERENCES candidates(candidate_id) ON DELETE RESTRICT,
    operation_id TEXT NOT NULL,
    from_state TEXT NOT NULL CHECK (from_state = 'pending'),
    to_state TEXT NOT NULL CHECK (to_state IN ('confirmed', 'rejected')),
    reason_code TEXT NOT NULL,
    note TEXT,
    reviewer_id TEXT NOT NULL,
    memory_id TEXT REFERENCES memories(memory_id) ON DELETE RESTRICT,
    revision_id TEXT REFERENCES memory_revisions(revision_id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    CHECK ((to_state = 'confirmed' AND memory_id IS NOT NULL AND revision_id IS NOT NULL) OR (to_state = 'rejected' AND memory_id IS NULL AND revision_id IS NULL)),
    FOREIGN KEY (memory_id, revision_id)
        REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX candidate_reviews_candidate_time_idx
ON candidate_reviews(candidate_id, created_at DESC, review_id DESC);

CREATE TRIGGER candidate_reviews_no_update
BEFORE UPDATE ON candidate_reviews
BEGIN
    SELECT RAISE(ABORT, 'candidate reviews are append-only');
END;

CREATE TRIGGER candidate_reviews_no_delete
BEFORE DELETE ON candidate_reviews
BEGIN
    SELECT RAISE(ABORT, 'candidate reviews are append-only');
END;

CREATE TABLE corpus_metadata (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0),
    updated_at TEXT NOT NULL
) STRICT;

INSERT INTO corpus_metadata (singleton, generation, updated_at)
VALUES (1, 1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

CREATE TABLE entities (
    entity_id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    entity_type TEXT NOT NULL CHECK (entity_type IN ('person', 'organization', 'place', 'project', 'product', 'event', 'concept', 'other')),
    canonical_name TEXT NOT NULL CHECK (length(canonical_name) > 0),
    normalized_name TEXT NOT NULL CHECK (length(normalized_name) > 0),
    normalization_version TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('active', 'retired')),
    sensitivity TEXT NOT NULL DEFAULT 'normal' CHECK (sensitivity IN ('normal', 'sensitive')),
    created_by TEXT NOT NULL,
    processing_version TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (space_id, entity_id)
) STRICT;

CREATE INDEX entities_scope_name_idx
ON entities(space_id, state, normalized_name, entity_id);

CREATE TABLE entity_events (
    entity_event_id TEXT PRIMARY KEY,
    entity_id TEXT NOT NULL REFERENCES entities(entity_id) ON DELETE RESTRICT,
    operation_id TEXT NOT NULL,
    event_type TEXT NOT NULL CHECK (event_type IN ('created', 'canonical_name_changed', 'state_changed')),
    from_canonical_name TEXT,
    to_canonical_name TEXT,
    from_state TEXT CHECK (from_state IS NULL OR from_state IN ('active', 'retired')),
    to_state TEXT CHECK (to_state IS NULL OR to_state IN ('active', 'retired')),
    normalization_version TEXT,
    reason TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    CHECK (
        (event_type = 'created' AND from_canonical_name IS NULL AND to_canonical_name IS NOT NULL AND from_state IS NULL AND to_state = 'active') OR
        (event_type = 'canonical_name_changed' AND from_canonical_name IS NOT NULL AND to_canonical_name IS NOT NULL AND from_state IS NULL AND to_state IS NULL AND normalization_version IS NOT NULL) OR
        (event_type = 'state_changed' AND from_canonical_name IS NULL AND to_canonical_name IS NULL AND from_state IS NOT NULL AND to_state IS NOT NULL AND from_state <> to_state)
    )
) STRICT;

CREATE INDEX entity_events_entity_time_idx
ON entity_events(entity_id, created_at DESC, entity_event_id DESC);

CREATE TRIGGER entity_events_no_update BEFORE UPDATE ON entity_events BEGIN SELECT RAISE(ABORT, 'entity events are append-only'); END;
CREATE TRIGGER entity_events_no_delete BEFORE DELETE ON entity_events BEGIN SELECT RAISE(ABORT, 'entity events are append-only'); END;

CREATE TABLE entity_aliases (
    alias_id TEXT PRIMARY KEY,
    entity_id TEXT NOT NULL REFERENCES entities(entity_id) ON DELETE RESTRICT,
    alias TEXT NOT NULL CHECK (length(alias) > 0),
    normalized_alias TEXT NOT NULL CHECK (length(normalized_alias) > 0),
    normalization_version TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('processor_review', 'human_confirmation', 'import')),
    state TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'retired')),
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (entity_id, normalized_alias, normalization_version)
) STRICT;

CREATE INDEX entity_aliases_lookup_idx
ON entity_aliases(normalization_version, normalized_alias, state, entity_id);

CREATE TRIGGER entity_aliases_immutable_fields
BEFORE UPDATE ON entity_aliases
WHEN OLD.entity_id IS NOT NEW.entity_id OR OLD.alias IS NOT NEW.alias OR OLD.normalized_alias IS NOT NEW.normalized_alias
  OR OLD.normalization_version IS NOT NEW.normalization_version OR OLD.source IS NOT NEW.source
  OR OLD.created_by IS NOT NEW.created_by OR OLD.created_at IS NOT NEW.created_at
BEGIN SELECT RAISE(ABORT, 'entity alias identity is immutable'); END;
CREATE TRIGGER entity_aliases_no_delete BEFORE DELETE ON entity_aliases BEGIN SELECT RAISE(ABORT, 'entity aliases cannot be deleted'); END;

CREATE TABLE entity_alias_events (
    alias_event_id TEXT PRIMARY KEY,
    alias_id TEXT NOT NULL REFERENCES entity_aliases(alias_id) ON DELETE RESTRICT,
    operation_id TEXT NOT NULL,
    from_state TEXT CHECK (from_state IS NULL OR from_state IN ('active', 'retired')),
    to_state TEXT NOT NULL CHECK (to_state IN ('active', 'retired')),
    reason TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    CHECK ((from_state IS NULL AND to_state = 'active') OR (from_state IS NOT NULL AND from_state <> to_state))
) STRICT;

CREATE TRIGGER entity_alias_events_no_update BEFORE UPDATE ON entity_alias_events BEGIN SELECT RAISE(ABORT, 'entity alias events are append-only'); END;
CREATE TRIGGER entity_alias_events_no_delete BEFORE DELETE ON entity_alias_events BEGIN SELECT RAISE(ABORT, 'entity alias events are append-only'); END;

CREATE TABLE entity_mentions (
    mention_id TEXT PRIMARY KEY,
    entity_id TEXT NOT NULL REFERENCES entities(entity_id) ON DELETE RESTRICT,
    memory_id TEXT,
    revision_id TEXT,
    event_id TEXT REFERENCES raw_events(event_id) ON DELETE RESTRICT,
    start_offset INTEGER NOT NULL CHECK (start_offset >= 0),
    end_offset INTEGER NOT NULL CHECK (end_offset > start_offset),
    mention_type TEXT NOT NULL CHECK (mention_type IN ('explicit', 'implicit')),
    source TEXT NOT NULL CHECK (source IN ('processor', 'human_confirmation')),
    surface_text TEXT,
    confidence REAL CHECK (confidence IS NULL OR (confidence >= 0.0 AND confidence <= 1.0)),
    created_by TEXT NOT NULL,
    processing_version TEXT NOT NULL,
    created_at TEXT NOT NULL,
    CHECK ((memory_id IS NOT NULL AND revision_id IS NOT NULL AND event_id IS NULL) OR (memory_id IS NULL AND revision_id IS NULL AND event_id IS NOT NULL)),
    FOREIGN KEY (memory_id, revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX entity_mentions_entity_idx ON entity_mentions(entity_id, created_at DESC, mention_id DESC);
CREATE INDEX entity_mentions_revision_idx ON entity_mentions(memory_id, revision_id, entity_id) WHERE revision_id IS NOT NULL;
CREATE INDEX entity_mentions_event_idx ON entity_mentions(event_id, entity_id) WHERE event_id IS NOT NULL;
CREATE UNIQUE INDEX entity_mentions_revision_once ON entity_mentions(entity_id, memory_id, revision_id, start_offset, end_offset, mention_type) WHERE revision_id IS NOT NULL;
CREATE UNIQUE INDEX entity_mentions_event_once ON entity_mentions(entity_id, event_id, start_offset, end_offset, mention_type) WHERE event_id IS NOT NULL;
CREATE TRIGGER entity_mentions_no_update BEFORE UPDATE ON entity_mentions BEGIN SELECT RAISE(ABORT, 'entity mentions are immutable'); END;
CREATE TRIGGER entity_mentions_no_delete BEFORE DELETE ON entity_mentions BEGIN SELECT RAISE(ABORT, 'entity mentions are immutable'); END;

CREATE TABLE entity_proposals (
    proposal_id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES raw_events(event_id) ON DELETE RESTRICT,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    proposal_kind TEXT NOT NULL CHECK (proposal_kind IN ('entity', 'alias', 'mention')),
    entity_id TEXT REFERENCES entities(entity_id) ON DELETE RESTRICT,
    entity_type TEXT CHECK (entity_type IS NULL OR entity_type IN ('person', 'organization', 'place', 'project', 'product', 'event', 'concept', 'other')),
    canonical_name TEXT,
    alias TEXT,
    normalization_version TEXT,
    mention_memory_id TEXT,
    mention_revision_id TEXT,
    mention_event_id TEXT REFERENCES raw_events(event_id) ON DELETE RESTRICT,
    start_offset INTEGER CHECK (start_offset IS NULL OR start_offset >= 0),
    end_offset INTEGER,
    mention_type TEXT CHECK (mention_type IS NULL OR mention_type IN ('explicit', 'implicit')),
    sensitivity TEXT NOT NULL DEFAULT 'normal' CHECK (sensitivity IN ('normal', 'sensitive')),
    processor_identity TEXT NOT NULL,
    provider_identity TEXT,
    model_identity TEXT,
    processing_version TEXT NOT NULL,
    specification_hash TEXT NOT NULL,
    review_state TEXT NOT NULL DEFAULT 'pending' CHECK (review_state IN ('pending', 'confirmed', 'rejected')),
    result_entity_id TEXT REFERENCES entities(entity_id) ON DELETE RESTRICT,
    result_canonical_alias_id TEXT REFERENCES entity_aliases(alias_id) ON DELETE RESTRICT,
    result_alias_id TEXT REFERENCES entity_aliases(alias_id) ON DELETE RESTRICT,
    result_mention_id TEXT REFERENCES entity_mentions(mention_id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    reviewed_at TEXT,
    CHECK (end_offset IS NULL OR (start_offset IS NOT NULL AND end_offset > start_offset)),
    CHECK ((mention_memory_id IS NULL AND mention_revision_id IS NULL) OR (mention_memory_id IS NOT NULL AND mention_revision_id IS NOT NULL)),
    CHECK ((proposal_kind = 'entity' AND entity_id IS NULL AND entity_type IS NOT NULL AND canonical_name IS NOT NULL AND ((alias IS NULL AND normalization_version IS NULL) OR (alias IS NOT NULL AND normalization_version IS NOT NULL)) AND (((mention_revision_id IS NOT NULL AND mention_event_id IS NULL) OR (mention_revision_id IS NULL AND mention_event_id IS NOT NULL)) AND start_offset IS NOT NULL AND end_offset IS NOT NULL AND mention_type IS NOT NULL OR mention_revision_id IS NULL AND mention_event_id IS NULL AND start_offset IS NULL AND end_offset IS NULL AND mention_type IS NULL)) OR
           (proposal_kind = 'alias' AND entity_id IS NOT NULL AND entity_type IS NULL AND canonical_name IS NULL AND alias IS NOT NULL AND normalization_version IS NOT NULL AND mention_memory_id IS NULL AND mention_revision_id IS NULL AND mention_event_id IS NULL AND start_offset IS NULL AND end_offset IS NULL AND mention_type IS NULL) OR
           (proposal_kind = 'mention' AND entity_id IS NOT NULL AND entity_type IS NULL AND canonical_name IS NULL AND alias IS NULL AND normalization_version IS NULL AND ((mention_revision_id IS NOT NULL AND mention_event_id IS NULL) OR (mention_revision_id IS NULL AND mention_event_id IS NOT NULL)) AND start_offset IS NOT NULL AND end_offset IS NOT NULL AND mention_type IS NOT NULL)),
    CHECK ((review_state = 'pending' AND reviewed_at IS NULL AND result_entity_id IS NULL AND result_canonical_alias_id IS NULL AND result_alias_id IS NULL AND result_mention_id IS NULL) OR
           (review_state = 'rejected' AND reviewed_at IS NOT NULL AND result_entity_id IS NULL AND result_canonical_alias_id IS NULL AND result_alias_id IS NULL AND result_mention_id IS NULL) OR
           (review_state = 'confirmed' AND reviewed_at IS NOT NULL AND result_entity_id IS NOT NULL AND ((proposal_kind = 'entity' AND result_canonical_alias_id IS NOT NULL AND ((alias IS NULL AND result_alias_id IS NULL) OR (alias IS NOT NULL AND result_alias_id IS NOT NULL)) AND ((mention_revision_id IS NULL AND mention_event_id IS NULL AND result_mention_id IS NULL) OR ((mention_revision_id IS NOT NULL OR mention_event_id IS NOT NULL) AND result_mention_id IS NOT NULL))) OR (proposal_kind = 'alias' AND result_canonical_alias_id IS NULL AND result_alias_id IS NOT NULL AND result_mention_id IS NULL) OR (proposal_kind = 'mention' AND result_canonical_alias_id IS NULL AND result_alias_id IS NULL AND result_mention_id IS NOT NULL)))),
    UNIQUE (event_id, processing_version, proposal_kind, specification_hash),
    FOREIGN KEY (mention_memory_id, mention_revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX entity_proposals_review_idx ON entity_proposals(space_id, review_state, created_at DESC, proposal_id DESC);
CREATE TRIGGER entity_proposals_immutable_fields
BEFORE UPDATE ON entity_proposals WHEN OLD.event_id IS NOT NEW.event_id OR OLD.space_id IS NOT NEW.space_id OR OLD.proposal_kind IS NOT NEW.proposal_kind
 OR OLD.entity_id IS NOT NEW.entity_id OR OLD.entity_type IS NOT NEW.entity_type OR OLD.canonical_name IS NOT NEW.canonical_name OR OLD.alias IS NOT NEW.alias
 OR OLD.normalization_version IS NOT NEW.normalization_version OR OLD.mention_memory_id IS NOT NEW.mention_memory_id OR OLD.mention_revision_id IS NOT NEW.mention_revision_id
 OR OLD.mention_event_id IS NOT NEW.mention_event_id OR OLD.start_offset IS NOT NEW.start_offset OR OLD.end_offset IS NOT NEW.end_offset
 OR OLD.mention_type IS NOT NEW.mention_type OR OLD.sensitivity IS NOT NEW.sensitivity OR OLD.processor_identity IS NOT NEW.processor_identity
 OR OLD.provider_identity IS NOT NEW.provider_identity OR OLD.model_identity IS NOT NEW.model_identity OR OLD.processing_version IS NOT NEW.processing_version
 OR OLD.specification_hash IS NOT NEW.specification_hash OR OLD.created_at IS NOT NEW.created_at
BEGIN SELECT RAISE(ABORT, 'entity proposal specification is immutable'); END;
CREATE TRIGGER entity_proposals_terminal_state BEFORE UPDATE ON entity_proposals WHEN OLD.review_state <> 'pending' OR NEW.review_state NOT IN ('confirmed', 'rejected') BEGIN SELECT RAISE(ABORT, 'entity proposal review state is terminal'); END;
CREATE TRIGGER entity_proposals_no_delete BEFORE DELETE ON entity_proposals BEGIN SELECT RAISE(ABORT, 'entity proposals cannot be deleted'); END;

CREATE TABLE entity_proposal_reviews (
    review_id TEXT PRIMARY KEY,
    proposal_id TEXT NOT NULL REFERENCES entity_proposals(proposal_id) ON DELETE RESTRICT,
    operation_id TEXT NOT NULL,
    from_state TEXT NOT NULL CHECK (from_state = 'pending'),
    to_state TEXT NOT NULL CHECK (to_state IN ('confirmed', 'rejected')),
    reason_code TEXT NOT NULL,
    note TEXT,
    reviewer_id TEXT NOT NULL,
    result_entity_id TEXT REFERENCES entities(entity_id) ON DELETE RESTRICT,
    result_canonical_alias_id TEXT REFERENCES entity_aliases(alias_id) ON DELETE RESTRICT,
    result_alias_id TEXT REFERENCES entity_aliases(alias_id) ON DELETE RESTRICT,
    result_mention_id TEXT REFERENCES entity_mentions(mention_id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    CHECK ((to_state = 'confirmed' AND result_entity_id IS NOT NULL AND (result_canonical_alias_id IS NOT NULL OR result_alias_id IS NOT NULL OR result_mention_id IS NOT NULL)) OR (to_state = 'rejected' AND result_entity_id IS NULL AND result_canonical_alias_id IS NULL AND result_alias_id IS NULL AND result_mention_id IS NULL))
) STRICT;
CREATE UNIQUE INDEX entity_proposal_reviews_terminal_once ON entity_proposal_reviews(proposal_id);
CREATE TRIGGER entity_proposal_reviews_no_update BEFORE UPDATE ON entity_proposal_reviews BEGIN SELECT RAISE(ABORT, 'entity proposal reviews are append-only'); END;
CREATE TRIGGER entity_proposal_reviews_no_delete BEFORE DELETE ON entity_proposal_reviews BEGIN SELECT RAISE(ABORT, 'entity proposal reviews are append-only'); END;

CREATE TABLE entity_merge_proposals (
    merge_proposal_id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    source_entity_id TEXT NOT NULL REFERENCES entities(entity_id) ON DELETE RESTRICT,
    target_entity_id TEXT NOT NULL REFERENCES entities(entity_id) ON DELETE RESTRICT,
    reason TEXT NOT NULL,
    processing_version TEXT NOT NULL,
    specification_hash TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state = 'pending'),
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    CHECK (source_entity_id <> target_entity_id),
    UNIQUE (source_entity_id, target_entity_id, processing_version, specification_hash)
) STRICT;
CREATE TRIGGER entity_merge_proposals_no_update BEFORE UPDATE ON entity_merge_proposals BEGIN SELECT RAISE(ABORT, 'entity merge proposals are schema-only and immutable'); END;
CREATE TRIGGER entity_merge_proposals_no_delete BEFORE DELETE ON entity_merge_proposals BEGIN SELECT RAISE(ABORT, 'entity merge proposals are schema-only and immutable'); END;

CREATE TABLE memory_relations (
    relation_id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    relation_type TEXT NOT NULL CHECK (relation_type IN ('supports', 'contradicts', 'supersedes', 'derived_from')),
    source_memory_id TEXT NOT NULL,
    source_revision_id TEXT NOT NULL,
    target_memory_id TEXT NOT NULL,
    target_revision_id TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'dismissed')),
    source TEXT NOT NULL CHECK (source IN ('processor_review', 'human_confirmation', 'conflict_resolution', 'import')),
    confidence REAL CHECK (confidence IS NULL OR (confidence >= 0.0 AND confidence <= 1.0)),
    specification_hash TEXT NOT NULL,
    created_by TEXT NOT NULL,
    processing_version TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (source_memory_id <> target_memory_id OR source_revision_id <> target_revision_id),
    FOREIGN KEY (source_memory_id, source_revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT,
    FOREIGN KEY (target_memory_id, target_revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT,
    UNIQUE (relation_type, source_memory_id, source_revision_id, target_memory_id, target_revision_id, specification_hash)
) STRICT;

CREATE INDEX memory_relations_source_idx ON memory_relations(source_memory_id, source_revision_id, state, relation_type, relation_id);
CREATE INDEX memory_relations_target_idx ON memory_relations(target_memory_id, target_revision_id, state, relation_type, relation_id);
CREATE INDEX memory_relations_space_idx ON memory_relations(space_id, state, relation_type, created_at DESC, relation_id DESC);
CREATE TRIGGER memory_relations_immutable_fields
BEFORE UPDATE ON memory_relations WHEN OLD.space_id IS NOT NEW.space_id OR OLD.relation_type IS NOT NEW.relation_type
 OR OLD.source_memory_id IS NOT NEW.source_memory_id OR OLD.source_revision_id IS NOT NEW.source_revision_id
 OR OLD.target_memory_id IS NOT NEW.target_memory_id OR OLD.target_revision_id IS NOT NEW.target_revision_id
 OR OLD.source IS NOT NEW.source OR OLD.confidence IS NOT NEW.confidence OR OLD.specification_hash IS NOT NEW.specification_hash
 OR OLD.created_by IS NOT NEW.created_by OR OLD.processing_version IS NOT NEW.processing_version OR OLD.created_at IS NOT NEW.created_at
BEGIN SELECT RAISE(ABORT, 'memory relation assertion is immutable'); END;
CREATE TRIGGER memory_relations_no_delete BEFORE DELETE ON memory_relations BEGIN SELECT RAISE(ABORT, 'memory relations cannot be deleted'); END;

CREATE TABLE relation_evidence (
    evidence_id TEXT PRIMARY KEY,
    relation_id TEXT NOT NULL REFERENCES memory_relations(relation_id) ON DELETE RESTRICT,
    evidence_type TEXT NOT NULL CHECK (evidence_type IN ('raw_event_range', 'revision_range', 'human_confirmation')),
    event_id TEXT REFERENCES raw_events(event_id) ON DELETE RESTRICT,
    memory_id TEXT,
    revision_id TEXT,
    start_offset INTEGER CHECK (start_offset IS NULL OR start_offset >= 0),
    end_offset INTEGER,
    note TEXT,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    CHECK (end_offset IS NULL OR (start_offset IS NOT NULL AND end_offset > start_offset)),
    CHECK ((evidence_type = 'raw_event_range' AND event_id IS NOT NULL AND memory_id IS NULL AND revision_id IS NULL AND start_offset IS NOT NULL AND end_offset IS NOT NULL) OR
           (evidence_type = 'revision_range' AND event_id IS NULL AND memory_id IS NOT NULL AND revision_id IS NOT NULL AND start_offset IS NOT NULL AND end_offset IS NOT NULL) OR
           (evidence_type = 'human_confirmation' AND event_id IS NULL AND memory_id IS NULL AND revision_id IS NULL AND start_offset IS NULL AND end_offset IS NULL)),
    FOREIGN KEY (memory_id, revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) STRICT;
CREATE INDEX relation_evidence_relation_idx ON relation_evidence(relation_id, evidence_id);
CREATE TRIGGER relation_evidence_no_update BEFORE UPDATE ON relation_evidence BEGIN SELECT RAISE(ABORT, 'relation evidence is immutable'); END;
CREATE TRIGGER relation_evidence_no_delete BEFORE DELETE ON relation_evidence BEGIN SELECT RAISE(ABORT, 'relation evidence is immutable'); END;

CREATE TABLE relation_state_events (
    state_event_id TEXT PRIMARY KEY,
    relation_id TEXT NOT NULL REFERENCES memory_relations(relation_id) ON DELETE RESTRICT,
    operation_id TEXT NOT NULL,
    from_state TEXT CHECK (from_state IS NULL OR from_state IN ('active', 'dismissed')),
    to_state TEXT NOT NULL CHECK (to_state IN ('active', 'dismissed')),
    reason TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    CHECK ((from_state IS NULL AND to_state = 'active') OR (from_state IS NOT NULL AND from_state <> to_state))
) STRICT;
CREATE INDEX relation_state_events_relation_idx ON relation_state_events(relation_id, created_at DESC, state_event_id DESC);
CREATE TRIGGER relation_state_events_no_update BEFORE UPDATE ON relation_state_events BEGIN SELECT RAISE(ABORT, 'relation state events are append-only'); END;
CREATE TRIGGER relation_state_events_no_delete BEFORE DELETE ON relation_state_events BEGIN SELECT RAISE(ABORT, 'relation state events are append-only'); END;

CREATE TABLE relation_proposals (
    proposal_id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES raw_events(event_id) ON DELETE RESTRICT,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    relation_type TEXT NOT NULL CHECK (relation_type IN ('supports', 'contradicts', 'supersedes', 'derived_from')),
    source_memory_id TEXT NOT NULL,
    source_revision_id TEXT NOT NULL,
    target_memory_id TEXT NOT NULL,
    target_revision_id TEXT NOT NULL,
    confidence REAL CHECK (confidence IS NULL OR (confidence >= 0.0 AND confidence <= 1.0)),
    evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json) AND json_type(evidence_json) = 'array'),
    processor_identity TEXT NOT NULL,
    provider_identity TEXT,
    model_identity TEXT,
    processing_version TEXT NOT NULL,
    specification_hash TEXT NOT NULL,
    review_state TEXT NOT NULL DEFAULT 'pending' CHECK (review_state IN ('pending', 'confirmed', 'rejected')),
    result_relation_id TEXT REFERENCES memory_relations(relation_id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    reviewed_at TEXT,
    CHECK (source_memory_id <> target_memory_id OR source_revision_id <> target_revision_id),
    CHECK ((review_state = 'pending' AND reviewed_at IS NULL AND result_relation_id IS NULL) OR (review_state = 'rejected' AND reviewed_at IS NOT NULL AND result_relation_id IS NULL) OR (review_state = 'confirmed' AND reviewed_at IS NOT NULL AND result_relation_id IS NOT NULL)),
    FOREIGN KEY (source_memory_id, source_revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT,
    FOREIGN KEY (target_memory_id, target_revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT,
    UNIQUE (event_id, processing_version, relation_type, source_memory_id, source_revision_id, target_memory_id, target_revision_id, specification_hash)
) STRICT;
CREATE INDEX relation_proposals_review_idx ON relation_proposals(space_id, review_state, created_at DESC, proposal_id DESC);
CREATE TRIGGER relation_proposals_immutable_fields BEFORE UPDATE ON relation_proposals WHEN OLD.event_id IS NOT NEW.event_id OR OLD.space_id IS NOT NEW.space_id
 OR OLD.relation_type IS NOT NEW.relation_type OR OLD.source_memory_id IS NOT NEW.source_memory_id OR OLD.source_revision_id IS NOT NEW.source_revision_id
 OR OLD.target_memory_id IS NOT NEW.target_memory_id OR OLD.target_revision_id IS NOT NEW.target_revision_id OR OLD.confidence IS NOT NEW.confidence
 OR OLD.evidence_json IS NOT NEW.evidence_json OR OLD.processor_identity IS NOT NEW.processor_identity OR OLD.provider_identity IS NOT NEW.provider_identity
 OR OLD.model_identity IS NOT NEW.model_identity OR OLD.processing_version IS NOT NEW.processing_version OR OLD.specification_hash IS NOT NEW.specification_hash OR OLD.created_at IS NOT NEW.created_at
BEGIN SELECT RAISE(ABORT, 'relation proposal specification is immutable'); END;
CREATE TRIGGER relation_proposals_terminal_state BEFORE UPDATE ON relation_proposals WHEN OLD.review_state <> 'pending' OR NEW.review_state NOT IN ('confirmed', 'rejected') BEGIN SELECT RAISE(ABORT, 'relation proposal review state is terminal'); END;
CREATE TRIGGER relation_proposals_no_delete BEFORE DELETE ON relation_proposals BEGIN SELECT RAISE(ABORT, 'relation proposals cannot be deleted'); END;

CREATE TABLE relation_proposal_reviews (
    review_id TEXT PRIMARY KEY,
    proposal_id TEXT NOT NULL REFERENCES relation_proposals(proposal_id) ON DELETE RESTRICT,
    operation_id TEXT NOT NULL,
    from_state TEXT NOT NULL CHECK (from_state = 'pending'),
    to_state TEXT NOT NULL CHECK (to_state IN ('confirmed', 'rejected')),
    reason_code TEXT NOT NULL,
    note TEXT,
    reviewer_id TEXT NOT NULL,
    relation_id TEXT REFERENCES memory_relations(relation_id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    CHECK ((to_state = 'confirmed' AND relation_id IS NOT NULL) OR (to_state = 'rejected' AND relation_id IS NULL))
) STRICT;
CREATE UNIQUE INDEX relation_proposal_reviews_terminal_once ON relation_proposal_reviews(proposal_id);
CREATE TRIGGER relation_proposal_reviews_no_update BEFORE UPDATE ON relation_proposal_reviews BEGIN SELECT RAISE(ABORT, 'relation proposal reviews are append-only'); END;
CREATE TRIGGER relation_proposal_reviews_no_delete BEFORE DELETE ON relation_proposal_reviews BEGIN SELECT RAISE(ABORT, 'relation proposal reviews are append-only'); END;

CREATE TABLE relation_index_metadata (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    active_version INTEGER NOT NULL CHECK (active_version > 0),
    generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0),
    state TEXT NOT NULL CHECK (state IN ('ready', 'degraded', 'rebuilding', 'stale')),
    source_relation_count INTEGER NOT NULL DEFAULT 0 CHECK (source_relation_count >= 0),
    updated_at TEXT NOT NULL
) STRICT;
INSERT INTO relation_index_metadata(singleton, active_version, state, updated_at) VALUES (1, 1, 'ready', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

CREATE TABLE entity_index_metadata (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    active_version INTEGER NOT NULL CHECK (active_version > 0),
    generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0),
    state TEXT NOT NULL CHECK (state IN ('ready', 'degraded', 'rebuilding', 'stale')),
    source_entity_count INTEGER NOT NULL DEFAULT 0 CHECK (source_entity_count >= 0),
    source_mention_count INTEGER NOT NULL DEFAULT 0 CHECK (source_mention_count >= 0),
    normalization_version TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;
INSERT INTO entity_index_metadata(singleton, active_version, state, normalization_version, updated_at) VALUES (1, 1, 'ready', 'v1', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

CREATE TABLE evolution_proposals (
    proposal_id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES raw_events(event_id) ON DELETE RESTRICT,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    action TEXT NOT NULL CHECK (action IN ('duplicate', 'supplement', 'replacement', 'conflict')),
    proposed_content TEXT,
    proposed_content_hash TEXT,
    valid_from TEXT,
    valid_until TEXT,
    memory_type TEXT CHECK (memory_type IS NULL OR memory_type IN ('fact', 'event', 'preference', 'plan', 'procedure', 'scene', 'topic', 'timeline')),
    sensitivity TEXT NOT NULL DEFAULT 'normal' CHECK (sensitivity IN ('normal', 'sensitive')),
    relation_specs_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(relation_specs_json) AND json_type(relation_specs_json) = 'array'),
    mention_specs_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(mention_specs_json) AND json_type(mention_specs_json) = 'array'),
    processor_identity TEXT NOT NULL,
    provider_identity TEXT,
    model_identity TEXT,
    processing_version TEXT NOT NULL,
    specification_hash TEXT NOT NULL,
    review_state TEXT NOT NULL DEFAULT 'pending' CHECK (review_state IN ('pending', 'confirmed', 'rejected')),
    created_at TEXT NOT NULL,
    reviewed_at TEXT,
    CHECK (valid_until IS NULL OR valid_from IS NULL OR valid_until > valid_from),
    CHECK ((action = 'duplicate' AND proposed_content IS NULL AND proposed_content_hash IS NULL) OR (action <> 'duplicate' AND proposed_content IS NOT NULL AND proposed_content_hash IS NOT NULL)),
    CHECK ((review_state = 'pending' AND reviewed_at IS NULL) OR (review_state <> 'pending' AND reviewed_at IS NOT NULL)),
    UNIQUE (event_id, processing_version, action, specification_hash)
) STRICT;
CREATE INDEX evolution_proposals_review_idx ON evolution_proposals(space_id, review_state, created_at DESC, proposal_id DESC);
CREATE TRIGGER evolution_proposals_immutable_fields BEFORE UPDATE ON evolution_proposals WHEN OLD.event_id IS NOT NEW.event_id OR OLD.space_id IS NOT NEW.space_id
 OR OLD.action IS NOT NEW.action OR OLD.proposed_content IS NOT NEW.proposed_content OR OLD.proposed_content_hash IS NOT NEW.proposed_content_hash
 OR OLD.valid_from IS NOT NEW.valid_from OR OLD.valid_until IS NOT NEW.valid_until OR OLD.memory_type IS NOT NEW.memory_type OR OLD.sensitivity IS NOT NEW.sensitivity
 OR OLD.relation_specs_json IS NOT NEW.relation_specs_json OR OLD.mention_specs_json IS NOT NEW.mention_specs_json OR OLD.processor_identity IS NOT NEW.processor_identity
 OR OLD.provider_identity IS NOT NEW.provider_identity OR OLD.model_identity IS NOT NEW.model_identity OR OLD.processing_version IS NOT NEW.processing_version
 OR OLD.specification_hash IS NOT NEW.specification_hash OR OLD.created_at IS NOT NEW.created_at
BEGIN SELECT RAISE(ABORT, 'evolution proposal specification is immutable'); END;
CREATE TRIGGER evolution_proposals_terminal_state BEFORE UPDATE ON evolution_proposals WHEN OLD.review_state <> 'pending' OR NEW.review_state NOT IN ('confirmed', 'rejected') BEGIN SELECT RAISE(ABORT, 'evolution proposal review state is terminal'); END;
CREATE TRIGGER evolution_proposals_no_delete BEFORE DELETE ON evolution_proposals BEGIN SELECT RAISE(ABORT, 'evolution proposals cannot be deleted'); END;

CREATE TABLE evolution_proposal_targets (
    proposal_id TEXT NOT NULL REFERENCES evolution_proposals(proposal_id) ON DELETE RESTRICT,
    target_order INTEGER NOT NULL CHECK (target_order >= 0),
    memory_id TEXT NOT NULL,
    expected_revision_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (proposal_id, target_order),
    UNIQUE (proposal_id, memory_id),
    FOREIGN KEY (memory_id, expected_revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) WITHOUT ROWID, STRICT;
CREATE TRIGGER evolution_proposal_targets_no_update BEFORE UPDATE ON evolution_proposal_targets BEGIN SELECT RAISE(ABORT, 'evolution proposal targets are immutable'); END;
CREATE TRIGGER evolution_proposal_targets_no_delete BEFORE DELETE ON evolution_proposal_targets BEGIN SELECT RAISE(ABORT, 'evolution proposal targets are immutable'); END;

CREATE TABLE evolution_proposal_evidence (
    evidence_id TEXT PRIMARY KEY,
    proposal_id TEXT NOT NULL REFERENCES evolution_proposals(proposal_id) ON DELETE RESTRICT,
    evidence_type TEXT NOT NULL CHECK (evidence_type IN ('raw_event_range', 'revision_range', 'human_confirmation')),
    event_id TEXT REFERENCES raw_events(event_id) ON DELETE RESTRICT,
    memory_id TEXT,
    revision_id TEXT,
    start_offset INTEGER CHECK (start_offset IS NULL OR start_offset >= 0),
    end_offset INTEGER,
    note TEXT,
    created_at TEXT NOT NULL,
    CHECK (end_offset IS NULL OR (start_offset IS NOT NULL AND end_offset > start_offset)),
    CHECK ((evidence_type = 'raw_event_range' AND event_id IS NOT NULL AND memory_id IS NULL AND revision_id IS NULL AND start_offset IS NOT NULL AND end_offset IS NOT NULL) OR
           (evidence_type = 'revision_range' AND event_id IS NULL AND memory_id IS NOT NULL AND revision_id IS NOT NULL AND start_offset IS NOT NULL AND end_offset IS NOT NULL) OR
           (evidence_type = 'human_confirmation' AND event_id IS NULL AND memory_id IS NULL AND revision_id IS NULL AND start_offset IS NULL AND end_offset IS NULL)),
    FOREIGN KEY (memory_id, revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) STRICT;
CREATE TRIGGER evolution_proposal_evidence_no_update BEFORE UPDATE ON evolution_proposal_evidence BEGIN SELECT RAISE(ABORT, 'evolution proposal evidence is immutable'); END;
CREATE TRIGGER evolution_proposal_evidence_no_delete BEFORE DELETE ON evolution_proposal_evidence BEGIN SELECT RAISE(ABORT, 'evolution proposal evidence is immutable'); END;

CREATE TABLE evolution_proposal_reviews (
    review_id TEXT PRIMARY KEY,
    proposal_id TEXT NOT NULL REFERENCES evolution_proposals(proposal_id) ON DELETE RESTRICT,
    operation_id TEXT NOT NULL,
    from_state TEXT NOT NULL CHECK (from_state = 'pending'),
    to_state TEXT NOT NULL CHECK (to_state IN ('confirmed', 'rejected')),
    replacement_mode TEXT CHECK (replacement_mode IS NULL OR replacement_mode IN ('same_memory_revision', 'new_memory')),
    reason_code TEXT NOT NULL,
    note TEXT,
    reviewer_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    CHECK (to_state = 'confirmed' OR replacement_mode IS NULL)
) STRICT;
CREATE UNIQUE INDEX evolution_proposal_reviews_terminal_once ON evolution_proposal_reviews(proposal_id);
CREATE TRIGGER evolution_proposal_reviews_no_update BEFORE UPDATE ON evolution_proposal_reviews BEGIN SELECT RAISE(ABORT, 'evolution proposal reviews are append-only'); END;
CREATE TRIGGER evolution_proposal_reviews_no_delete BEFORE DELETE ON evolution_proposal_reviews BEGIN SELECT RAISE(ABORT, 'evolution proposal reviews are append-only'); END;

CREATE TABLE evolution_result_refs (
    result_ref_id TEXT PRIMARY KEY,
    review_id TEXT NOT NULL REFERENCES evolution_proposal_reviews(review_id) ON DELETE RESTRICT,
    result_type TEXT NOT NULL CHECK (result_type IN ('memory', 'revision', 'relation', 'entity', 'mention', 'conflict_case')),
    result_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (review_id, result_type, result_id)
) STRICT;
CREATE TRIGGER evolution_result_refs_no_update BEFORE UPDATE ON evolution_result_refs BEGIN SELECT RAISE(ABORT, 'evolution result refs are immutable'); END;
CREATE TRIGGER evolution_result_refs_no_delete BEFORE DELETE ON evolution_result_refs BEGIN SELECT RAISE(ABORT, 'evolution result refs are immutable'); END;

CREATE TABLE conflict_cases (
    case_id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'resolved', 'dismissed')),
    opened_by TEXT NOT NULL,
    processing_version TEXT NOT NULL,
    created_at TEXT NOT NULL,
    closed_at TEXT,
    CHECK ((state = 'open' AND closed_at IS NULL) OR (state <> 'open' AND closed_at IS NOT NULL))
) STRICT;
CREATE INDEX conflict_cases_scope_state_idx ON conflict_cases(space_id, state, created_at DESC, case_id DESC);
CREATE TRIGGER conflict_cases_immutable_fields BEFORE UPDATE ON conflict_cases WHEN OLD.space_id IS NOT NEW.space_id OR OLD.opened_by IS NOT NEW.opened_by OR OLD.processing_version IS NOT NEW.processing_version OR OLD.created_at IS NOT NEW.created_at BEGIN SELECT RAISE(ABORT, 'conflict case identity is immutable'); END;
CREATE TRIGGER conflict_cases_terminal_state BEFORE UPDATE ON conflict_cases WHEN OLD.state <> 'open' OR NEW.state NOT IN ('resolved', 'dismissed') BEGIN SELECT RAISE(ABORT, 'conflict case state is terminal'); END;
CREATE TRIGGER conflict_cases_no_delete BEFORE DELETE ON conflict_cases BEGIN SELECT RAISE(ABORT, 'conflict cases cannot be deleted'); END;

CREATE TABLE conflict_case_participants (
    case_id TEXT NOT NULL REFERENCES conflict_cases(case_id) ON DELETE RESTRICT,
    participant_order INTEGER NOT NULL CHECK (participant_order >= 0),
    memory_id TEXT NOT NULL,
    revision_id TEXT NOT NULL,
    input_state TEXT NOT NULL CHECK (input_state IN ('active', 'contested')),
    input_sensitivity TEXT NOT NULL CHECK (input_sensitivity IN ('normal', 'sensitive')),
    created_at TEXT NOT NULL,
    PRIMARY KEY (case_id, participant_order),
    UNIQUE (case_id, memory_id, revision_id),
    FOREIGN KEY (memory_id, revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) WITHOUT ROWID, STRICT;
CREATE INDEX conflict_case_participants_revision_idx ON conflict_case_participants(memory_id, revision_id, case_id);
CREATE TRIGGER conflict_case_participants_no_update BEFORE UPDATE ON conflict_case_participants BEGIN SELECT RAISE(ABORT, 'conflict case participants are append-only'); END;
CREATE TRIGGER conflict_case_participants_no_delete BEFORE DELETE ON conflict_case_participants BEGIN SELECT RAISE(ABORT, 'conflict case participants are append-only'); END;

CREATE TABLE conflict_case_relations (
    case_id TEXT NOT NULL REFERENCES conflict_cases(case_id) ON DELETE RESTRICT,
    relation_id TEXT NOT NULL REFERENCES memory_relations(relation_id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    PRIMARY KEY (case_id, relation_id)
) WITHOUT ROWID, STRICT;
CREATE TRIGGER conflict_case_relations_one_open_case
BEFORE INSERT ON conflict_case_relations
WHEN EXISTS (
    SELECT 1 FROM conflict_case_relations existing
    JOIN conflict_cases existing_case ON existing_case.case_id = existing.case_id
    WHERE existing.relation_id = NEW.relation_id AND existing_case.state = 'open'
)
BEGIN SELECT RAISE(ABORT, 'contradicts relation already belongs to an open case'); END;
CREATE TRIGGER conflict_case_relations_validate_insert BEFORE INSERT ON conflict_case_relations WHEN (SELECT relation_type FROM memory_relations WHERE relation_id = NEW.relation_id) <> 'contradicts' BEGIN SELECT RAISE(ABORT, 'conflict cases require contradicts relations'); END;
CREATE TRIGGER conflict_case_relations_no_update BEFORE UPDATE ON conflict_case_relations BEGIN SELECT RAISE(ABORT, 'conflict case relations are append-only'); END;
CREATE TRIGGER conflict_case_relations_no_delete BEFORE DELETE ON conflict_case_relations BEGIN SELECT RAISE(ABORT, 'conflict case relations are append-only'); END;

CREATE TABLE conflict_resolution_events (
    resolution_event_id TEXT PRIMARY KEY,
    case_id TEXT NOT NULL REFERENCES conflict_cases(case_id) ON DELETE RESTRICT,
    operation_id TEXT NOT NULL,
    resolution_type TEXT NOT NULL CHECK (resolution_type IN ('keep_both', 'dismiss', 'revise_one', 'supersede', 'reconcile')),
    from_state TEXT NOT NULL CHECK (from_state = 'open'),
    to_state TEXT NOT NULL CHECK (to_state IN ('resolved', 'dismissed')),
    reason TEXT NOT NULL,
    resolved_by TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX conflict_resolution_events_terminal_once ON conflict_resolution_events(case_id);
CREATE TRIGGER conflict_resolution_events_no_update BEFORE UPDATE ON conflict_resolution_events BEGIN SELECT RAISE(ABORT, 'conflict resolution events are append-only'); END;
CREATE TRIGGER conflict_resolution_events_no_delete BEFORE DELETE ON conflict_resolution_events BEGIN SELECT RAISE(ABORT, 'conflict resolution events are append-only'); END;

CREATE TABLE conflict_resolution_result_refs (
    result_ref_id TEXT PRIMARY KEY,
    resolution_event_id TEXT NOT NULL REFERENCES conflict_resolution_events(resolution_event_id) ON DELETE RESTRICT,
    result_type TEXT NOT NULL CHECK (result_type IN ('memory', 'revision', 'evidence', 'projection', 'relation', 'state_event', 'job')),
    result_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (resolution_event_id, result_type, result_id)
) STRICT;
CREATE TRIGGER conflict_resolution_result_refs_no_update BEFORE UPDATE ON conflict_resolution_result_refs BEGIN SELECT RAISE(ABORT, 'conflict resolution refs are immutable'); END;
CREATE TRIGGER conflict_resolution_result_refs_no_delete BEFORE DELETE ON conflict_resolution_result_refs BEGIN SELECT RAISE(ABORT, 'conflict resolution refs are immutable'); END;

CREATE TABLE feedback_events (
    feedback_id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL,
    caller_id TEXT NOT NULL,
    memory_id TEXT NOT NULL,
    revision_id TEXT NOT NULL,
    feedback_type TEXT NOT NULL CHECK (feedback_type IN ('useful', 'irrelevant', 'correct', 'supplement', 'contest')),
    reason_code TEXT NOT NULL CHECK (reason_code IN ('answered_query', 'relevant_context', 'not_relevant', 'fact_incorrect', 'fact_incomplete', 'fact_outdated', 'conflicts_with_evidence', 'duplicate', 'other')),
    note TEXT,
    search_operation_id TEXT,
    query_hash TEXT,
    result_rank INTEGER CHECK (result_rank IS NULL OR result_rank > 0),
    ranking_version TEXT,
    matched_channels_json TEXT CHECK (matched_channels_json IS NULL OR (json_valid(matched_channels_json) AND json_type(matched_channels_json) = 'array')),
    created_at TEXT NOT NULL,
    CHECK ((search_operation_id IS NULL AND query_hash IS NULL AND result_rank IS NULL AND ranking_version IS NULL AND matched_channels_json IS NULL) OR (search_operation_id IS NOT NULL AND query_hash IS NOT NULL AND result_rank IS NOT NULL AND ranking_version IS NOT NULL AND matched_channels_json IS NOT NULL)),
    FOREIGN KEY (memory_id, revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) STRICT;
CREATE INDEX feedback_events_revision_time_idx ON feedback_events(memory_id, revision_id, created_at DESC, feedback_id DESC);
CREATE INDEX feedback_events_caller_time_idx ON feedback_events(caller_id, created_at DESC, feedback_id DESC);
CREATE INDEX feedback_events_type_time_idx ON feedback_events(feedback_type, created_at DESC, feedback_id DESC);
CREATE TRIGGER feedback_events_no_update BEFORE UPDATE ON feedback_events BEGIN SELECT RAISE(ABORT, 'feedback events are append-only'); END;
CREATE TRIGGER feedback_events_no_delete BEFORE DELETE ON feedback_events BEGIN SELECT RAISE(ABORT, 'feedback events are append-only'); END;

CREATE TABLE feedback_aggregates (
    revision_id TEXT NOT NULL REFERENCES memory_revisions(revision_id) ON DELETE RESTRICT,
    algorithm_version TEXT NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    positive_count INTEGER NOT NULL CHECK (positive_count >= 0),
    negative_count INTEGER NOT NULL CHECK (negative_count >= 0),
    bounded_usage_score REAL NOT NULL CHECK (bounded_usage_score >= -1.0 AND bounded_usage_score <= 1.0),
    window_start TEXT NOT NULL,
    window_end TEXT NOT NULL,
    source_watermark TEXT,
    state TEXT NOT NULL CHECK (state IN ('current', 'stale', 'rebuilding', 'degraded')),
    updated_at TEXT NOT NULL,
    CHECK (window_end > window_start),
    PRIMARY KEY (revision_id, algorithm_version)
) WITHOUT ROWID, STRICT;
CREATE INDEX feedback_aggregates_state_idx ON feedback_aggregates(algorithm_version, state, revision_id);

CREATE TABLE feedback_aggregate_metadata (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    algorithm_version TEXT NOT NULL,
    generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0),
    state TEXT NOT NULL CHECK (state IN ('ready', 'degraded', 'rebuilding', 'stale')),
    source_watermark TEXT,
    window_days INTEGER NOT NULL DEFAULT 90 CHECK (window_days = 90),
    updated_at TEXT NOT NULL
) STRICT;
INSERT INTO feedback_aggregate_metadata(singleton, algorithm_version, state, updated_at) VALUES (1, 'v1', 'ready', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

CREATE TABLE aggregate_descriptors (
    aggregate_id TEXT PRIMARY KEY,
    memory_id TEXT NOT NULL UNIQUE REFERENCES memories(memory_id) ON DELETE RESTRICT,
    aggregate_kind TEXT NOT NULL CHECK (aggregate_kind IN ('scene', 'topic', 'timeline')),
    policy_version TEXT NOT NULL,
    processing_version TEXT NOT NULL,
    sensitivity TEXT NOT NULL CHECK (sensitivity IN ('normal', 'sensitive')),
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;
CREATE INDEX aggregate_descriptors_kind_idx ON aggregate_descriptors(aggregate_kind, memory_id);
CREATE TRIGGER aggregate_descriptors_validate_insert BEFORE INSERT ON aggregate_descriptors WHEN (SELECT r.kind FROM memories m JOIN memory_revisions r ON r.revision_id=m.current_revision_id WHERE m.memory_id=NEW.memory_id) IS NOT NEW.aggregate_kind BEGIN SELECT RAISE(ABORT, 'aggregate descriptor kind must match current revision'); END;
CREATE TRIGGER aggregate_descriptors_no_update BEFORE UPDATE ON aggregate_descriptors BEGIN SELECT RAISE(ABORT, 'aggregate descriptors are immutable'); END;
CREATE TRIGGER aggregate_descriptors_no_delete BEFORE DELETE ON aggregate_descriptors BEGIN SELECT RAISE(ABORT, 'aggregate descriptors are immutable'); END;

CREATE TABLE aggregate_proposals (
    proposal_id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    aggregate_kind TEXT NOT NULL CHECK (aggregate_kind IN ('scene', 'topic', 'timeline')),
    content TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    input_fingerprint TEXT NOT NULL,
    valid_from TEXT,
    valid_until TEXT,
    sensitivity TEXT NOT NULL CHECK (sensitivity IN ('normal', 'sensitive')),
    policy_version TEXT NOT NULL,
    processor_identity TEXT NOT NULL,
    provider_identity TEXT,
    model_identity TEXT,
    processing_version TEXT NOT NULL,
    specification_hash TEXT NOT NULL,
    review_state TEXT NOT NULL DEFAULT 'pending' CHECK (review_state IN ('pending', 'confirmed', 'rejected')),
    result_memory_id TEXT REFERENCES memories(memory_id) ON DELETE RESTRICT,
    result_revision_id TEXT REFERENCES memory_revisions(revision_id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    reviewed_at TEXT,
    CHECK (valid_until IS NULL OR valid_from IS NULL OR valid_until > valid_from),
    CHECK ((review_state = 'pending' AND reviewed_at IS NULL AND result_memory_id IS NULL AND result_revision_id IS NULL) OR (review_state = 'rejected' AND reviewed_at IS NOT NULL AND result_memory_id IS NULL AND result_revision_id IS NULL) OR (review_state = 'confirmed' AND reviewed_at IS NOT NULL AND result_memory_id IS NOT NULL AND result_revision_id IS NOT NULL)),
    UNIQUE (space_id, aggregate_kind, input_fingerprint, processing_version, specification_hash),
    FOREIGN KEY (result_memory_id, result_revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) STRICT;
CREATE INDEX aggregate_proposals_review_idx ON aggregate_proposals(space_id, review_state, aggregate_kind, created_at DESC, proposal_id DESC);
CREATE TRIGGER aggregate_proposals_immutable_fields BEFORE UPDATE ON aggregate_proposals WHEN OLD.space_id IS NOT NEW.space_id OR OLD.aggregate_kind IS NOT NEW.aggregate_kind
 OR OLD.content IS NOT NEW.content OR OLD.content_hash IS NOT NEW.content_hash OR OLD.input_fingerprint IS NOT NEW.input_fingerprint OR OLD.valid_from IS NOT NEW.valid_from
 OR OLD.valid_until IS NOT NEW.valid_until OR OLD.sensitivity IS NOT NEW.sensitivity OR OLD.policy_version IS NOT NEW.policy_version OR OLD.processor_identity IS NOT NEW.processor_identity
 OR OLD.provider_identity IS NOT NEW.provider_identity OR OLD.model_identity IS NOT NEW.model_identity OR OLD.processing_version IS NOT NEW.processing_version OR OLD.specification_hash IS NOT NEW.specification_hash OR OLD.created_at IS NOT NEW.created_at
BEGIN SELECT RAISE(ABORT, 'aggregate proposal specification is immutable'); END;
CREATE TRIGGER aggregate_proposals_terminal_state BEFORE UPDATE ON aggregate_proposals WHEN OLD.review_state <> 'pending' OR NEW.review_state NOT IN ('confirmed', 'rejected') BEGIN SELECT RAISE(ABORT, 'aggregate proposal review state is terminal'); END;
CREATE TRIGGER aggregate_proposals_no_delete BEFORE DELETE ON aggregate_proposals BEGIN SELECT RAISE(ABORT, 'aggregate proposals cannot be deleted'); END;

CREATE TABLE aggregate_proposal_inputs (
    proposal_id TEXT NOT NULL REFERENCES aggregate_proposals(proposal_id) ON DELETE RESTRICT,
    input_order INTEGER NOT NULL CHECK (input_order >= 0),
    memory_id TEXT NOT NULL,
    revision_id TEXT NOT NULL,
    input_space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    input_state TEXT NOT NULL CHECK (input_state IN ('active', 'contested')),
    input_sensitivity TEXT NOT NULL CHECK (input_sensitivity IN ('normal', 'sensitive')),
    created_at TEXT NOT NULL,
    PRIMARY KEY (proposal_id, input_order),
    UNIQUE (proposal_id, revision_id),
    FOREIGN KEY (memory_id, revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) WITHOUT ROWID, STRICT;
CREATE TRIGGER aggregate_proposal_inputs_no_update BEFORE UPDATE ON aggregate_proposal_inputs BEGIN SELECT RAISE(ABORT, 'aggregate proposal inputs are immutable'); END;
CREATE TRIGGER aggregate_proposal_inputs_no_delete BEFORE DELETE ON aggregate_proposal_inputs BEGIN SELECT RAISE(ABORT, 'aggregate proposal inputs are immutable'); END;

CREATE TABLE aggregate_proposal_reviews (
    review_id TEXT PRIMARY KEY,
    proposal_id TEXT NOT NULL REFERENCES aggregate_proposals(proposal_id) ON DELETE RESTRICT,
    operation_id TEXT NOT NULL,
    from_state TEXT NOT NULL CHECK (from_state = 'pending'),
    to_state TEXT NOT NULL CHECK (to_state IN ('confirmed', 'rejected')),
    reason_code TEXT NOT NULL,
    note TEXT,
    reviewer_id TEXT NOT NULL,
    memory_id TEXT REFERENCES memories(memory_id) ON DELETE RESTRICT,
    revision_id TEXT REFERENCES memory_revisions(revision_id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    CHECK ((to_state = 'confirmed' AND memory_id IS NOT NULL AND revision_id IS NOT NULL) OR (to_state = 'rejected' AND memory_id IS NULL AND revision_id IS NULL)),
    FOREIGN KEY (memory_id, revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) STRICT;
CREATE UNIQUE INDEX aggregate_proposal_reviews_terminal_once ON aggregate_proposal_reviews(proposal_id);
CREATE TRIGGER aggregate_proposal_reviews_no_update BEFORE UPDATE ON aggregate_proposal_reviews BEGIN SELECT RAISE(ABORT, 'aggregate proposal reviews are append-only'); END;
CREATE TRIGGER aggregate_proposal_reviews_no_delete BEFORE DELETE ON aggregate_proposal_reviews BEGIN SELECT RAISE(ABORT, 'aggregate proposal reviews are append-only'); END;

CREATE TABLE aggregate_freshness (
    aggregate_id TEXT PRIMARY KEY REFERENCES aggregate_descriptors(aggregate_id) ON DELETE RESTRICT,
    revision_id TEXT NOT NULL REFERENCES memory_revisions(revision_id) ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK (state IN ('fresh', 'stale')),
    input_fingerprint TEXT NOT NULL,
    input_snapshot_json TEXT NOT NULL CHECK (json_valid(input_snapshot_json) AND json_type(input_snapshot_json) = 'array'),
    stale_reasons_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(stale_reasons_json) AND json_type(stale_reasons_json) = 'array'),
    generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0),
    checked_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;
CREATE INDEX aggregate_freshness_state_idx ON aggregate_freshness(state, updated_at, aggregate_id);

CREATE TABLE export_operations (
    export_operation_id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL UNIQUE,
    export_id TEXT NOT NULL UNIQUE,
    source_identity TEXT NOT NULL,
    selection_json TEXT NOT NULL CHECK (json_valid(selection_json) AND json_type(selection_json) = 'object'),
    closure_policy TEXT NOT NULL,
    sensitive_policy TEXT NOT NULL CHECK (sensitive_policy IN ('exclude', 'include')),
    reference_time TEXT NOT NULL,
    corpus_generation INTEGER NOT NULL CHECK (corpus_generation > 0),
    state TEXT NOT NULL CHECK (state IN ('planned', 'pending', 'running', 'succeeded', 'failed', 'cancelled')),
    dry_run INTEGER NOT NULL CHECK (dry_run IN (0, 1)),
    manifest_hash TEXT,
    package_checksum TEXT,
    result_json TEXT CHECK (result_json IS NULL OR json_valid(result_json)),
    last_error_code TEXT,
    last_error_message TEXT,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    updated_at TEXT NOT NULL,
    CHECK ((state IN ('succeeded', 'failed', 'cancelled') AND finished_at IS NOT NULL) OR (state NOT IN ('succeeded', 'failed', 'cancelled') AND finished_at IS NULL))
) STRICT;
CREATE INDEX export_operations_state_idx ON export_operations(state, updated_at, export_operation_id);
CREATE TRIGGER export_operations_immutable_fields BEFORE UPDATE ON export_operations WHEN OLD.operation_id IS NOT NEW.operation_id OR OLD.export_id IS NOT NEW.export_id OR OLD.source_identity IS NOT NEW.source_identity OR OLD.selection_json IS NOT NEW.selection_json OR OLD.closure_policy IS NOT NEW.closure_policy OR OLD.sensitive_policy IS NOT NEW.sensitive_policy OR OLD.reference_time IS NOT NEW.reference_time OR OLD.corpus_generation IS NOT NEW.corpus_generation OR OLD.dry_run IS NOT NEW.dry_run OR OLD.created_by IS NOT NEW.created_by OR OLD.created_at IS NOT NEW.created_at BEGIN SELECT RAISE(ABORT, 'export operation request is immutable'); END;
CREATE TRIGGER export_operations_no_delete BEFORE DELETE ON export_operations BEGIN SELECT RAISE(ABORT, 'export operations cannot be deleted'); END;

CREATE TABLE import_sources (
    source_id TEXT PRIMARY KEY,
    source_identity TEXT NOT NULL UNIQUE,
    format_family TEXT NOT NULL CHECK (format_family = 'dmem-logical'),
    format_version INTEGER NOT NULL CHECK (format_version = 1),
    first_export_id TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;
CREATE TRIGGER import_sources_no_update BEFORE UPDATE ON import_sources BEGIN SELECT RAISE(ABORT, 'import sources are immutable'); END;
CREATE TRIGGER import_sources_no_delete BEFORE DELETE ON import_sources BEGIN SELECT RAISE(ABORT, 'import sources are immutable'); END;

CREATE TABLE import_operations (
    import_operation_id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL UNIQUE,
    source_id TEXT NOT NULL REFERENCES import_sources(source_id) ON DELETE RESTRICT,
    export_id TEXT NOT NULL,
    target_space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    package_hash TEXT NOT NULL,
    plan_hash TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('planned', 'pending', 'running', 'waiting', 'succeeded', 'failed', 'cancelled', 'partial')),
    generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
    total_objects INTEGER NOT NULL CHECK (total_objects >= 0),
    committed_objects INTEGER NOT NULL DEFAULT 0 CHECK (committed_objects >= 0 AND committed_objects <= total_objects),
    last_error_code TEXT,
    last_error_message TEXT,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    updated_at TEXT NOT NULL,
    CHECK ((state IN ('succeeded', 'failed', 'cancelled', 'partial') AND finished_at IS NOT NULL) OR (state NOT IN ('succeeded', 'failed', 'cancelled', 'partial') AND finished_at IS NULL)),
    UNIQUE (source_id, export_id, target_space_id)
) STRICT;
CREATE INDEX import_operations_state_idx ON import_operations(state, updated_at, import_operation_id);
CREATE TRIGGER import_operations_immutable_fields BEFORE UPDATE ON import_operations WHEN OLD.operation_id IS NOT NEW.operation_id OR OLD.source_id IS NOT NEW.source_id OR OLD.export_id IS NOT NEW.export_id OR OLD.target_space_id IS NOT NEW.target_space_id OR OLD.package_hash IS NOT NEW.package_hash OR OLD.plan_hash IS NOT NEW.plan_hash OR OLD.total_objects IS NOT NEW.total_objects OR OLD.created_by IS NOT NEW.created_by OR OLD.created_at IS NOT NEW.created_at BEGIN SELECT RAISE(ABORT, 'import operation plan is immutable'); END;
CREATE TRIGGER import_operations_no_delete BEFORE DELETE ON import_operations BEGIN SELECT RAISE(ABORT, 'import operations cannot be deleted'); END;

CREATE TABLE import_space_mappings (
    import_operation_id TEXT NOT NULL REFERENCES import_operations(import_operation_id) ON DELETE RESTRICT,
    source_space_id TEXT NOT NULL,
    local_space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    PRIMARY KEY (import_operation_id, source_space_id)
) WITHOUT ROWID, STRICT;
CREATE TRIGGER import_space_mappings_no_update BEFORE UPDATE ON import_space_mappings BEGIN SELECT RAISE(ABORT, 'import space mappings are immutable'); END;
CREATE TRIGGER import_space_mappings_no_delete BEFORE DELETE ON import_space_mappings BEGIN SELECT RAISE(ABORT, 'import space mappings are immutable'); END;

CREATE TABLE import_object_mappings (
    mapping_id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL REFERENCES import_sources(source_id) ON DELETE RESTRICT,
    source_object_type TEXT NOT NULL CHECK (source_object_type IN ('space', 'raw_content', 'raw_event', 'memory', 'revision', 'memory_evidence', 'memory_state_event', 'entity', 'entity_event', 'entity_alias', 'entity_alias_event', 'entity_mention', 'relation', 'relation_evidence', 'relation_state_event', 'conflict_case', 'conflict_participant', 'conflict_relation', 'conflict_resolution', 'conflict_resolution_ref', 'feedback_event', 'aggregate_descriptor')),
    source_object_id TEXT NOT NULL,
    export_id TEXT NOT NULL,
    local_object_id TEXT NOT NULL,
    object_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (source_id, source_object_type, source_object_id, export_id)
) STRICT;
CREATE INDEX import_object_mappings_local_idx ON import_object_mappings(local_object_id, source_object_type);
CREATE TRIGGER import_object_mappings_no_update BEFORE UPDATE ON import_object_mappings BEGIN SELECT RAISE(ABORT, 'import object mappings are immutable'); END;
CREATE TRIGGER import_object_mappings_no_delete BEFORE DELETE ON import_object_mappings BEGIN SELECT RAISE(ABORT, 'import object mappings are immutable'); END;

CREATE TABLE imported_provenance (
    provenance_id TEXT PRIMARY KEY,
    import_operation_id TEXT NOT NULL REFERENCES import_operations(import_operation_id) ON DELETE RESTRICT,
    mapping_id TEXT NOT NULL REFERENCES import_object_mappings(mapping_id) ON DELETE RESTRICT,
    batch_number INTEGER NOT NULL CHECK (batch_number >= 0),
    imported_by TEXT NOT NULL,
    imported_at TEXT NOT NULL,
    UNIQUE (import_operation_id, mapping_id)
) STRICT;
CREATE INDEX imported_provenance_batch_idx ON imported_provenance(import_operation_id, batch_number, provenance_id);
CREATE TRIGGER imported_provenance_no_update BEFORE UPDATE ON imported_provenance BEGIN SELECT RAISE(ABORT, 'imported provenance is append-only'); END;
CREATE TRIGGER imported_provenance_no_delete BEFORE DELETE ON imported_provenance BEGIN SELECT RAISE(ABORT, 'imported provenance is append-only'); END;

CREATE TABLE import_checkpoints (
    checkpoint_id TEXT PRIMARY KEY,
    import_operation_id TEXT NOT NULL REFERENCES import_operations(import_operation_id) ON DELETE RESTRICT,
    generation INTEGER NOT NULL CHECK (generation > 0),
    batch_number INTEGER NOT NULL CHECK (batch_number >= 0),
    last_member TEXT NOT NULL,
    last_object_id TEXT NOT NULL,
    committed_objects INTEGER NOT NULL CHECK (committed_objects >= 0),
    created_at TEXT NOT NULL,
    UNIQUE (import_operation_id, generation, batch_number)
) STRICT;
CREATE INDEX import_checkpoints_operation_idx ON import_checkpoints(import_operation_id, generation DESC, batch_number DESC);
CREATE TRIGGER import_checkpoints_no_update BEFORE UPDATE ON import_checkpoints BEGIN SELECT RAISE(ABORT, 'import checkpoints are append-only'); END;
CREATE TRIGGER import_checkpoints_no_delete BEFORE DELETE ON import_checkpoints BEGIN SELECT RAISE(ABORT, 'import checkpoints are append-only'); END;

CREATE TABLE memory_projections (
    projection_id INTEGER PRIMARY KEY,
    memory_id TEXT NOT NULL UNIQUE REFERENCES memories(memory_id) ON DELETE RESTRICT,
    revision_id TEXT NOT NULL REFERENCES memory_revisions(revision_id) ON DELETE RESTRICT,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    memory_type TEXT NOT NULL,
    current_state TEXT NOT NULL,
    searchable INTEGER NOT NULL CHECK (searchable IN (0, 1)),
    display_content TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    embedding_state TEXT NOT NULL DEFAULT 'stale' CHECK (embedding_state IN ('stale', 'current')),
    embedding_provider TEXT,
    embedding_model TEXT,
    embedding_dimension INTEGER CHECK (embedding_dimension IS NULL OR embedding_dimension > 0),
    embedding_content_hash TEXT,
    valid_from TEXT,
    valid_until TEXT,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (memory_id, revision_id)
        REFERENCES memory_revisions(memory_id, revision_id)
        ON DELETE RESTRICT
) STRICT;

CREATE TABLE memory_embeddings (
    memory_id TEXT NOT NULL REFERENCES memories(memory_id) ON DELETE RESTRICT,
    revision_id TEXT NOT NULL REFERENCES memory_revisions(revision_id) ON DELETE RESTRICT,
    content_hash TEXT NOT NULL,
    compatibility_identity TEXT NOT NULL,
    configuration_identity TEXT NOT NULL,
    provider_identity TEXT NOT NULL,
    model_identity TEXT NOT NULL,
    dimension INTEGER NOT NULL CHECK (dimension > 0),
    vector_blob BLOB NOT NULL CHECK (typeof(vector_blob) = 'blob' AND vec_length(vector_blob) = dimension),
    processed_at TEXT NOT NULL,
    PRIMARY KEY (memory_id, revision_id, content_hash, compatibility_identity),
    FOREIGN KEY (memory_id, revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) WITHOUT ROWID, STRICT;

CREATE INDEX memory_embeddings_identity_idx
ON memory_embeddings(compatibility_identity, memory_id, revision_id, content_hash);

CREATE INDEX memory_projections_scope_idx
ON memory_projections(space_id, searchable, memory_type, current_state, memory_id);

CREATE VIRTUAL TABLE memory_fts USING fts5(
    memory_id UNINDEXED,
    revision_id UNINDEXED,
    content,
    tokenize = 'trigram'
);

CREATE TABLE jobs (
    job_id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL,
    job_type TEXT NOT NULL CHECK (job_type IN ('extract_memory', 'extract_entity', 'extract_relation', 'extract_graph', 'embed_memory', 'rebuild_fts', 'rebuild_semantic', 'rebuild_entity_index', 'rebuild_relation_index', 'rebuild_graph_index', 'aggregate_feedback', 'rebuild_feedback_aggregates', 'build_aggregate', 'rebuild_aggregate', 'logical_export', 'logical_import')),
    target_type TEXT NOT NULL CHECK (target_type IN ('raw_event', 'memory_revision', 'feedback_revision', 'aggregate', 'index', 'export_operation', 'import_operation')),
    target_id TEXT NOT NULL,
    processing_version TEXT NOT NULL,
    processor_identity TEXT,
    status TEXT NOT NULL CHECK (status IN ('pending', 'waiting', 'running', 'retrying', 'succeeded', 'failed', 'cancelled', 'expired', 'blocked_policy', 'superseded')),
    priority INTEGER NOT NULL DEFAULT 0,
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    max_attempts INTEGER NOT NULL CHECK (max_attempts > 0),
    next_run_at TEXT NOT NULL,
    lease_owner TEXT,
    lease_until TEXT,
    lease_generation INTEGER NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
    cancel_requested_at TEXT,
    capability TEXT CHECK (capability IS NULL OR capability IN ('processing', 'embedding')),
    configuration_generation INTEGER CHECK (configuration_generation IS NULL OR configuration_generation >= 0),
    policy_generation INTEGER CHECK (policy_generation IS NULL OR policy_generation >= 0),
    compatibility_identity TEXT,
    blocked_reason_code TEXT,
    reconciliation_run_id TEXT,
    provider_attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (provider_attempt_count >= 0),
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
    outcome TEXT CHECK (outcome IS NULL OR outcome IN ('succeeded', 'retrying', 'failed', 'cancelled', 'lease_lost', 'blocked_policy', 'stale_generation', 'superseded')),
    configuration_generation INTEGER CHECK (configuration_generation IS NULL OR configuration_generation >= 0),
    policy_generation INTEGER CHECK (policy_generation IS NULL OR policy_generation >= 0),
    effective_decision_id TEXT,
    compatibility_identity TEXT,
    external_call_started_at TEXT,
    omitted_context_count INTEGER NOT NULL DEFAULT 0 CHECK (omitted_context_count >= 0),
    omission_summary TEXT CHECK (omission_summary IS NULL OR json_valid(omission_summary)),
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

CREATE TABLE semantic_index_metadata (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    active_version INTEGER CHECK (active_version IS NULL OR active_version > 0),
    building_version INTEGER CHECK (building_version IS NULL OR building_version > 0),
    state TEXT NOT NULL CHECK (state IN ('absent', 'building', 'ready', 'degraded', 'stale')),
    provider_identity TEXT,
    model_identity TEXT,
    dimension INTEGER CHECK (dimension IS NULL OR dimension > 0),
    compatibility_identity TEXT,
    policy_generation INTEGER CHECK (policy_generation IS NULL OR policy_generation >= 0),
    source_revision_count INTEGER NOT NULL DEFAULT 0 CHECK (source_revision_count >= 0),
    eligible_revision_count INTEGER NOT NULL DEFAULT 0 CHECK (eligible_revision_count >= 0),
    embedded_revision_count INTEGER NOT NULL DEFAULT 0 CHECK (embedded_revision_count >= 0),
    excluded_revision_count INTEGER NOT NULL DEFAULT 0 CHECK (excluded_revision_count >= 0),
    corpus_fingerprint TEXT,
    created_at TEXT,
    activated_at TEXT,
    updated_at TEXT NOT NULL
) STRICT;

INSERT INTO semantic_index_metadata (singleton, state, updated_at)
VALUES (1, 'absent', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

CREATE TABLE configuration_revisions (
    revision_id TEXT PRIMARY KEY,
    source_identity TEXT NOT NULL,
    effective_identity TEXT,
    config_version INTEGER NOT NULL CHECK (config_version = 1),
    loaded_at TEXT NOT NULL,
    redacted_document TEXT CHECK (redacted_document IS NULL OR json_valid(redacted_document))
) STRICT;

CREATE TABLE configuration_activations (
    activation_id TEXT PRIMARY KEY,
    revision_id TEXT REFERENCES configuration_revisions(revision_id) ON DELETE RESTRICT,
    previous_activation_id TEXT,
    trigger TEXT NOT NULL CHECK (trigger IN ('startup', 'api', 'sighup')),
    requested_by TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('activated', 'no_change', 'rejected', 'failed')),
    error_code TEXT,
    diagnostics TEXT CHECK (diagnostics IS NULL OR json_valid(diagnostics)),
    started_at TEXT NOT NULL,
    finished_at TEXT NOT NULL
) STRICT;

CREATE INDEX configuration_activations_revision_idx
ON configuration_activations(revision_id, started_at);

CREATE TABLE effective_configuration_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    activation_id TEXT REFERENCES configuration_activations(activation_id) ON DELETE RESTRICT,
    source_identity TEXT,
    effective_identity TEXT,
    activation_generation INTEGER CHECK (activation_generation IS NULL OR activation_generation > 0),
    activated_at TEXT,
    last_attempt_id TEXT REFERENCES configuration_activations(activation_id) ON DELETE RESTRICT
) STRICT;

CREATE TABLE capability_states (
    capability TEXT PRIMARY KEY CHECK (capability IN ('processing', 'embedding')),
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    configuration_identity TEXT,
    configuration_generation INTEGER CHECK (configuration_generation IS NULL OR configuration_generation >= 0),
    compatibility_identity TEXT,
    policy_identity TEXT,
    policy_generation INTEGER CHECK (policy_generation IS NULL OR policy_generation >= 0),
    activation_id TEXT REFERENCES configuration_activations(activation_id) ON DELETE RESTRICT,
    health_state TEXT NOT NULL CHECK (health_state IN ('disabled', 'ready', 'degraded', 'unavailable')),
    reconciliation_state TEXT NOT NULL CHECK (reconciliation_state IN ('idle', 'queued', 'running', 'failed'))
) STRICT;

CREATE TABLE provider_policies (
    policy_identity TEXT NOT NULL,
    capability TEXT NOT NULL CHECK (capability IN ('processing', 'embedding')),
    default_decision TEXT NOT NULL CHECK (default_decision = 'deny'),
    allowed_content_types TEXT NOT NULL CHECK (json_valid(allowed_content_types)),
    allowed_source_types TEXT NOT NULL CHECK (json_valid(allowed_source_types)),
    allowed_sensitivities TEXT NOT NULL CHECK (json_valid(allowed_sensitivities)),
    query_allowed INTEGER NOT NULL CHECK (query_allowed IN (0, 1)),
    created_at TEXT NOT NULL,
    PRIMARY KEY (policy_identity, capability)
) WITHOUT ROWID, STRICT;

CREATE TABLE policy_assignments (
    assignment_id TEXT PRIMARY KEY,
    policy_identity TEXT NOT NULL,
    capability TEXT NOT NULL CHECK (capability IN ('processing', 'embedding')),
    selector_kind TEXT NOT NULL CHECK (selector_kind IN ('space', 'space_type', 'external_id')),
    space_id TEXT,
    space_type TEXT,
    external_id TEXT,
    effect TEXT NOT NULL CHECK (effect IN ('allow', 'deny')),
    include_descendants INTEGER NOT NULL DEFAULT 0 CHECK (include_descendants IN (0, 1)),
    priority INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (policy_identity, capability) REFERENCES provider_policies(policy_identity, capability) ON DELETE RESTRICT,
    CHECK (
        (selector_kind = 'space' AND space_id IS NOT NULL) OR
        (selector_kind = 'space_type' AND space_type IS NOT NULL) OR
        (selector_kind = 'external_id' AND external_id IS NOT NULL)
    )
) STRICT;

CREATE INDEX policy_assignments_lookup_idx
ON policy_assignments(capability, selector_kind, space_id, space_type, external_id, priority);

CREATE TABLE effective_space_policies (
    capability TEXT NOT NULL CHECK (capability IN ('processing', 'embedding')),
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    policy_generation INTEGER NOT NULL CHECK (policy_generation >= 0),
    decision TEXT NOT NULL CHECK (decision IN ('allow', 'deny')),
    source_assignment_id TEXT,
    source_space_id TEXT,
    inheritance_depth INTEGER CHECK (inheritance_depth IS NULL OR inheritance_depth >= 0),
    reason_code TEXT NOT NULL,
    evaluated_at TEXT NOT NULL,
    PRIMARY KEY (capability, space_id, policy_generation)
) WITHOUT ROWID, STRICT;

CREATE INDEX effective_space_policies_space_idx
ON effective_space_policies(space_id, capability, policy_generation);

CREATE TABLE embedding_eligibility (
    memory_id TEXT NOT NULL REFERENCES memories(memory_id) ON DELETE RESTRICT,
    revision_id TEXT NOT NULL,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    configuration_generation INTEGER CHECK (configuration_generation IS NULL OR configuration_generation >= 0),
    policy_generation INTEGER NOT NULL CHECK (policy_generation >= 0),
    eligible INTEGER NOT NULL CHECK (eligible IN (0, 1)),
    reason_code TEXT NOT NULL,
    evaluated_at TEXT NOT NULL,
    PRIMARY KEY (memory_id, revision_id, policy_generation),
    FOREIGN KEY (memory_id, revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) WITHOUT ROWID, STRICT;

CREATE INDEX embedding_eligibility_generation_idx
ON embedding_eligibility(policy_generation, eligible, memory_id);

CREATE TABLE reconciliation_runs (
    run_id TEXT PRIMARY KEY,
    capability TEXT NOT NULL CHECK (capability IN ('processing', 'embedding')),
    activation_id TEXT NOT NULL REFERENCES configuration_activations(activation_id) ON DELETE RESTRICT,
    change_class TEXT NOT NULL CHECK (change_class IN ('policy_expand', 'policy_narrow', 'compatibility', 'enable', 'disable', 'runtime_limits')),
    from_configuration_generation INTEGER CHECK (from_configuration_generation IS NULL OR from_configuration_generation >= 0),
    to_configuration_generation INTEGER CHECK (to_configuration_generation IS NULL OR to_configuration_generation >= 0),
    from_policy_generation INTEGER CHECK (from_policy_generation IS NULL OR from_policy_generation >= 0),
    to_policy_generation INTEGER CHECK (to_policy_generation IS NULL OR to_policy_generation >= 0),
    target_policy_identity TEXT,
    state TEXT NOT NULL CHECK (state IN ('pending', 'running', 'completed', 'failed')),
    cursor TEXT,
    scan_high_watermark TEXT,
    affected_count INTEGER NOT NULL DEFAULT 0 CHECK (affected_count >= 0),
    scheduled_count INTEGER NOT NULL DEFAULT 0 CHECK (scheduled_count >= 0),
    blocked_count INTEGER NOT NULL DEFAULT 0 CHECK (blocked_count >= 0),
    created_at TEXT NOT NULL,
    started_at TEXT,
    updated_at TEXT,
    finished_at TEXT
) STRICT;

CREATE INDEX reconciliation_runs_state_idx
ON reconciliation_runs(state, capability, created_at);

CREATE TABLE predicates (
    predicate TEXT PRIMARY KEY,
    description TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;

INSERT INTO predicates(predicate, description, created_at) VALUES
    ('created_by', 'entity was created by a person or organization', strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ('depends_on', 'entity depends on another entity', strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ('has_participant_count', 'event has a participant count', strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ('has_status', 'entity has a status', strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ('is_active', 'entity has an active state', strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ('located_at', 'entity is located at a place', strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ('member_of', 'entity is a member of an organization', strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ('owned_by', 'entity is owned by a person or organization', strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ('participates_in', 'entity participates in a project or activity', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

CREATE TABLE entity_facts (
    fact_id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    subject_entity_id TEXT NOT NULL REFERENCES entities(entity_id) ON DELETE RESTRICT,
    predicate TEXT NOT NULL REFERENCES predicates(predicate) ON DELETE RESTRICT,
    object_entity_id TEXT REFERENCES entities(entity_id) ON DELETE RESTRICT,
    object_literal TEXT,
    object_literal_type TEXT CHECK (object_literal_type IS NULL OR object_literal_type IN ('string', 'number', 'boolean')),
    source_memory_id TEXT NOT NULL,
    source_revision_id TEXT NOT NULL,
    confidence REAL CHECK (confidence IS NULL OR (confidence >= 0.0 AND confidence <= 1.0)),
    state TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'dismissed', 'superseded')),
    source TEXT NOT NULL CHECK (source IN ('processor_review', 'human_confirmation', 'auto_review', 'import')),
    specification_hash TEXT NOT NULL,
    created_by TEXT NOT NULL,
    processing_version TEXT NOT NULL,
    valid_from TEXT,
    valid_until TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK ((object_entity_id IS NULL) <> (object_literal IS NULL)),
    CHECK (object_literal IS NULL OR object_literal_type IS NOT NULL),
    CHECK (object_entity_id IS NULL OR subject_entity_id <> object_entity_id),
    CHECK (valid_until IS NULL OR valid_from IS NULL OR valid_until > valid_from),
    FOREIGN KEY (source_memory_id, source_revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT,
    UNIQUE (subject_entity_id, predicate, object_entity_id, object_literal, source_revision_id, specification_hash)
) STRICT;

CREATE INDEX entity_facts_subject_idx ON entity_facts(subject_entity_id, state, predicate, fact_id);
CREATE INDEX entity_facts_object_idx ON entity_facts(object_entity_id, state, predicate, fact_id) WHERE object_entity_id IS NOT NULL;
CREATE INDEX entity_facts_predicate_idx ON entity_facts(predicate, state, subject_entity_id, fact_id);
CREATE INDEX entity_facts_source_revision_idx ON entity_facts(source_memory_id, source_revision_id, state, fact_id);
CREATE INDEX entity_facts_space_idx ON entity_facts(space_id, state, predicate, created_at DESC, fact_id DESC);

CREATE TRIGGER entity_facts_immutable_fields
BEFORE UPDATE ON entity_facts WHEN OLD.space_id IS NOT NEW.space_id OR OLD.subject_entity_id IS NOT NEW.subject_entity_id
 OR OLD.predicate IS NOT NEW.predicate OR OLD.object_entity_id IS NOT NEW.object_entity_id OR OLD.object_literal IS NOT NEW.object_literal
 OR OLD.object_literal_type IS NOT NEW.object_literal_type OR OLD.source_memory_id IS NOT NEW.source_memory_id OR OLD.source_revision_id IS NOT NEW.source_revision_id
 OR OLD.confidence IS NOT NEW.confidence OR OLD.source IS NOT NEW.source OR OLD.specification_hash IS NOT NEW.specification_hash
 OR OLD.created_by IS NOT NEW.created_by OR OLD.processing_version IS NOT NEW.processing_version OR OLD.valid_from IS NOT NEW.valid_from
 OR OLD.valid_until IS NOT NEW.valid_until OR OLD.created_at IS NOT NEW.created_at
BEGIN SELECT RAISE(ABORT, 'entity fact assertion is immutable'); END;
CREATE TRIGGER entity_facts_no_delete BEFORE DELETE ON entity_facts BEGIN SELECT RAISE(ABORT, 'entity facts cannot be deleted'); END;

CREATE TABLE entity_fact_evidence (
    evidence_id TEXT PRIMARY KEY,
    fact_id TEXT NOT NULL REFERENCES entity_facts(fact_id) ON DELETE RESTRICT,
    evidence_type TEXT NOT NULL CHECK (evidence_type IN ('raw_event_range', 'revision_range', 'human_confirmation')),
    event_id TEXT REFERENCES raw_events(event_id) ON DELETE RESTRICT,
    memory_id TEXT,
    revision_id TEXT,
    start_offset INTEGER CHECK (start_offset IS NULL OR start_offset >= 0),
    end_offset INTEGER,
    note TEXT,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    CHECK (end_offset IS NULL OR (start_offset IS NOT NULL AND end_offset > start_offset)),
    CHECK ((evidence_type = 'raw_event_range' AND event_id IS NOT NULL AND memory_id IS NULL AND revision_id IS NULL AND start_offset IS NOT NULL AND end_offset IS NOT NULL) OR
           (evidence_type = 'revision_range' AND event_id IS NULL AND memory_id IS NOT NULL AND revision_id IS NOT NULL AND start_offset IS NOT NULL AND end_offset IS NOT NULL) OR
           (evidence_type = 'human_confirmation' AND event_id IS NULL AND memory_id IS NULL AND revision_id IS NULL AND start_offset IS NULL AND end_offset IS NULL)),
    FOREIGN KEY (memory_id, revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) STRICT;
CREATE INDEX entity_fact_evidence_fact_idx ON entity_fact_evidence(fact_id, evidence_id);
CREATE TRIGGER entity_fact_evidence_no_update BEFORE UPDATE ON entity_fact_evidence BEGIN SELECT RAISE(ABORT, 'entity fact evidence is immutable'); END;
CREATE TRIGGER entity_fact_evidence_no_delete BEFORE DELETE ON entity_fact_evidence BEGIN SELECT RAISE(ABORT, 'entity fact evidence is immutable'); END;

CREATE TABLE entity_fact_state_events (
    state_event_id TEXT PRIMARY KEY,
    fact_id TEXT NOT NULL REFERENCES entity_facts(fact_id) ON DELETE RESTRICT,
    operation_id TEXT NOT NULL,
    from_state TEXT CHECK (from_state IS NULL OR from_state IN ('active', 'dismissed', 'superseded')),
    to_state TEXT NOT NULL CHECK (to_state IN ('active', 'dismissed', 'superseded')),
    reason TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    CHECK ((from_state IS NULL AND to_state = 'active') OR (from_state IS NOT NULL AND from_state <> to_state))
) STRICT;
CREATE INDEX entity_fact_state_events_fact_idx ON entity_fact_state_events(fact_id, created_at DESC, state_event_id DESC);
CREATE TRIGGER entity_fact_state_events_no_update BEFORE UPDATE ON entity_fact_state_events BEGIN SELECT RAISE(ABORT, 'entity fact state events are append-only'); END;
CREATE TRIGGER entity_fact_state_events_no_delete BEFORE DELETE ON entity_fact_state_events BEGIN SELECT RAISE(ABORT, 'entity fact state events are append-only'); END;

CREATE TABLE entity_fact_proposals (
    proposal_id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES raw_events(event_id) ON DELETE RESTRICT,
    space_id TEXT NOT NULL REFERENCES memory_spaces(space_id) ON DELETE RESTRICT,
    subject_entity_id TEXT NOT NULL REFERENCES entities(entity_id) ON DELETE RESTRICT,
    predicate TEXT NOT NULL REFERENCES predicates(predicate) ON DELETE RESTRICT,
    object_entity_id TEXT REFERENCES entities(entity_id) ON DELETE RESTRICT,
    object_literal TEXT,
    object_literal_type TEXT CHECK (object_literal_type IS NULL OR object_literal_type IN ('string', 'number', 'boolean')),
    source_memory_id TEXT NOT NULL,
    source_revision_id TEXT NOT NULL,
    confidence REAL CHECK (confidence IS NULL OR (confidence >= 0.0 AND confidence <= 1.0)),
    valid_from TEXT,
    valid_until TEXT,
    evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json) AND json_type(evidence_json) = 'array'),
    processor_identity TEXT NOT NULL,
    provider_identity TEXT,
    model_identity TEXT,
    processing_version TEXT NOT NULL,
    specification_hash TEXT NOT NULL,
    auto_review_version TEXT,
    review_state TEXT NOT NULL DEFAULT 'pending' CHECK (review_state IN ('pending', 'confirmed', 'rejected')),
    result_fact_id TEXT REFERENCES entity_facts(fact_id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    reviewed_at TEXT,
    CHECK ((object_entity_id IS NULL) <> (object_literal IS NULL)),
    CHECK (object_literal IS NULL OR object_literal_type IS NOT NULL),
    CHECK (object_entity_id IS NULL OR subject_entity_id <> object_entity_id),
    CHECK (valid_until IS NULL OR valid_from IS NULL OR valid_until > valid_from),
    CHECK ((review_state = 'pending' AND reviewed_at IS NULL AND result_fact_id IS NULL) OR (review_state = 'rejected' AND reviewed_at IS NOT NULL AND result_fact_id IS NULL) OR (review_state = 'confirmed' AND reviewed_at IS NOT NULL AND result_fact_id IS NOT NULL)),
    FOREIGN KEY (source_memory_id, source_revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT,
    UNIQUE (event_id, processing_version, specification_hash)
) STRICT;
CREATE INDEX entity_fact_proposals_review_idx ON entity_fact_proposals(space_id, review_state, created_at DESC, proposal_id DESC);
CREATE TRIGGER entity_fact_proposals_immutable_fields BEFORE UPDATE ON entity_fact_proposals WHEN OLD.event_id IS NOT NEW.event_id OR OLD.space_id IS NOT NEW.space_id
 OR OLD.subject_entity_id IS NOT NEW.subject_entity_id OR OLD.predicate IS NOT NEW.predicate OR OLD.object_entity_id IS NOT NEW.object_entity_id
 OR OLD.object_literal IS NOT NEW.object_literal OR OLD.object_literal_type IS NOT NEW.object_literal_type OR OLD.source_memory_id IS NOT NEW.source_memory_id
 OR OLD.source_revision_id IS NOT NEW.source_revision_id OR OLD.confidence IS NOT NEW.confidence OR OLD.valid_from IS NOT NEW.valid_from OR OLD.valid_until IS NOT NEW.valid_until
 OR OLD.evidence_json IS NOT NEW.evidence_json OR OLD.processor_identity IS NOT NEW.processor_identity OR OLD.provider_identity IS NOT NEW.provider_identity
 OR OLD.model_identity IS NOT NEW.model_identity OR OLD.processing_version IS NOT NEW.processing_version OR OLD.specification_hash IS NOT NEW.specification_hash OR OLD.created_at IS NOT NEW.created_at
BEGIN SELECT RAISE(ABORT, 'entity fact proposal specification is immutable'); END;
CREATE TRIGGER entity_fact_proposals_terminal_state BEFORE UPDATE ON entity_fact_proposals WHEN OLD.review_state <> 'pending' OR NEW.review_state NOT IN ('confirmed', 'rejected') BEGIN SELECT RAISE(ABORT, 'entity fact proposal review state is terminal'); END;
CREATE TRIGGER entity_fact_proposals_no_delete BEFORE DELETE ON entity_fact_proposals BEGIN SELECT RAISE(ABORT, 'entity fact proposals cannot be deleted'); END;

CREATE TABLE entity_fact_proposal_reviews (
    review_id TEXT PRIMARY KEY,
    proposal_id TEXT NOT NULL REFERENCES entity_fact_proposals(proposal_id) ON DELETE RESTRICT,
    operation_id TEXT NOT NULL,
    from_state TEXT NOT NULL CHECK (from_state = 'pending'),
    to_state TEXT NOT NULL CHECK (to_state IN ('confirmed', 'rejected')),
    reason_code TEXT NOT NULL,
    note TEXT,
    reviewer_id TEXT NOT NULL,
    fact_id TEXT REFERENCES entity_facts(fact_id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    CHECK ((to_state = 'confirmed' AND fact_id IS NOT NULL) OR (to_state = 'rejected' AND fact_id IS NULL))
) STRICT;
CREATE UNIQUE INDEX entity_fact_proposal_reviews_terminal_once ON entity_fact_proposal_reviews(proposal_id);
CREATE TRIGGER entity_fact_proposal_reviews_no_update BEFORE UPDATE ON entity_fact_proposal_reviews BEGIN SELECT RAISE(ABORT, 'entity fact proposal reviews are append-only'); END;
CREATE TRIGGER entity_fact_proposal_reviews_no_delete BEFORE DELETE ON entity_fact_proposal_reviews BEGIN SELECT RAISE(ABORT, 'entity fact proposal reviews are append-only'); END;

CREATE TABLE graph_index_metadata (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    active_version INTEGER NOT NULL CHECK (active_version > 0),
    generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0),
    state TEXT NOT NULL CHECK (state IN ('ready', 'degraded', 'rebuilding', 'stale')),
    source_fact_count INTEGER NOT NULL DEFAULT 0 CHECK (source_fact_count >= 0),
    source_entity_count INTEGER NOT NULL DEFAULT 0 CHECK (source_entity_count >= 0),
    source_relation_count INTEGER NOT NULL DEFAULT 0 CHECK (source_relation_count >= 0),
    updated_at TEXT NOT NULL
) STRICT;
INSERT INTO graph_index_metadata(singleton, active_version, state, updated_at) VALUES (1, 1, 'ready', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

CREATE TABLE predicate_definitions (
    predicate TEXT PRIMARY KEY,
    version TEXT NOT NULL,
    subject_types_json TEXT NOT NULL CHECK (json_valid(subject_types_json) AND json_type(subject_types_json) = 'array'),
    object_entity_types_json TEXT NOT NULL CHECK (json_valid(object_entity_types_json) AND json_type(object_entity_types_json) = 'array'),
    object_literal_type TEXT CHECK (object_literal_type IS NULL OR object_literal_type IN ('string', 'number', 'boolean')),
    direction TEXT NOT NULL CHECK (direction IN ('directed', 'symmetric')),
    inverse_predicate TEXT REFERENCES predicate_definitions(predicate) ON DELETE RESTRICT,
    cardinality TEXT NOT NULL CHECK (cardinality IN ('one', 'many')),
    transitivity TEXT NOT NULL CHECK (transitivity IN ('none', 'permitted')),
    temporal_policy TEXT NOT NULL CHECK (temporal_policy IN ('static', 'valid_time', 'event_time')),
    allowed_path_positions_json TEXT NOT NULL CHECK (json_valid(allowed_path_positions_json) AND json_type(allowed_path_positions_json) = 'array'),
    auto_confirm TEXT NOT NULL CHECK (auto_confirm IN ('never', 'exact_match', 'threshold')),
    created_at TEXT NOT NULL,
    CHECK ((json_array_length(object_entity_types_json) = 0) <> (object_literal_type IS NULL)),
    CHECK (inverse_predicate IS NULL OR inverse_predicate <> predicate)
) STRICT;

CREATE TABLE event_participants (
    event_id TEXT NOT NULL REFERENCES raw_events(event_id) ON DELETE RESTRICT,
    entity_id TEXT NOT NULL REFERENCES entities(entity_id) ON DELETE RESTRICT,
    participant_role TEXT NOT NULL CHECK (length(participant_role) > 0),
    confidence REAL NOT NULL CHECK (confidence >= 0.0 AND confidence <= 1.0),
    created_at TEXT NOT NULL,
    PRIMARY KEY (event_id, entity_id, participant_role)
) WITHOUT ROWID, STRICT;

CREATE INDEX event_participants_entity_idx
ON event_participants(entity_id, event_id, participant_role);

CREATE TABLE event_temporal_bindings (
    binding_id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES raw_events(event_id) ON DELETE RESTRICT,
    binding_kind TEXT NOT NULL CHECK (binding_kind IN ('occurred_at', 'occurred_during')),
    place_entity_id TEXT REFERENCES entities(entity_id) ON DELETE RESTRICT,
    interval_start TEXT,
    interval_end TEXT,
    precision TEXT NOT NULL CHECK (precision IN ('exact', 'day', 'month', 'year', 'approximate', 'unknown')),
    confidence REAL NOT NULL CHECK (confidence >= 0.0 AND confidence <= 1.0),
    created_at TEXT NOT NULL,
    CHECK ((binding_kind = 'occurred_at' AND place_entity_id IS NOT NULL AND interval_start IS NULL AND interval_end IS NULL) OR
           (binding_kind = 'occurred_during' AND place_entity_id IS NULL AND interval_start IS NOT NULL)),
    CHECK (interval_end IS NULL OR interval_start IS NULL OR interval_end > interval_start)
) STRICT;

CREATE INDEX event_temporal_bindings_event_idx
ON event_temporal_bindings(event_id, binding_kind, binding_id);

CREATE TABLE query_telemetry (
    telemetry_id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL,
    query_plan_version TEXT NOT NULL,
    query_type TEXT NOT NULL,
    requested_capabilities_json TEXT NOT NULL CHECK (json_valid(requested_capabilities_json) AND json_type(requested_capabilities_json) = 'array'),
    executed_capabilities_json TEXT NOT NULL CHECK (json_valid(executed_capabilities_json) AND json_type(executed_capabilities_json) = 'array'),
    candidate_counts_json TEXT NOT NULL CHECK (json_valid(candidate_counts_json) AND json_type(candidate_counts_json) = 'object'),
    rejection_counts_json TEXT NOT NULL CHECK (json_valid(rejection_counts_json) AND json_type(rejection_counts_json) = 'object'),
    score_summary_json TEXT NOT NULL CHECK (json_valid(score_summary_json) AND json_type(score_summary_json) = 'object'),
    graph_counts_json TEXT NOT NULL CHECK (json_valid(graph_counts_json) AND json_type(graph_counts_json) = 'object'),
    duplicate_cluster_count INTEGER NOT NULL CHECK (duplicate_cluster_count >= 0),
    context_tokens INTEGER NOT NULL CHECK (context_tokens >= 0),
    context_chars INTEGER NOT NULL CHECK (context_chars >= 0),
    ranking_identity TEXT NOT NULL,
    context_identity TEXT NOT NULL,
    degradation_reasons_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(degradation_reasons_json) AND json_type(degradation_reasons_json) = 'array'),
    phase_latency_ms_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(phase_latency_ms_json) AND json_type(phase_latency_ms_json) = 'object'),
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX query_telemetry_operation_idx
ON query_telemetry(operation_id, created_at, telemetry_id);

CREATE TABLE context_decisions (
    decision_id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL,
    memory_id TEXT NOT NULL,
    revision_id TEXT NOT NULL,
    result_rank INTEGER CHECK (result_rank IS NULL OR result_rank > 0),
    selection_state TEXT NOT NULL CHECK (selection_state IN ('selected', 'rejected')),
    selection_reason TEXT NOT NULL,
    context_identity TEXT NOT NULL,
    estimated_tokens INTEGER NOT NULL CHECK (estimated_tokens >= 0),
    duplicate_cluster_id TEXT REFERENCES duplicate_clusters(cluster_id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    FOREIGN KEY (memory_id, revision_id) REFERENCES memory_revisions(memory_id, revision_id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX context_decisions_operation_idx
ON context_decisions(operation_id, selection_state, created_at, decision_id);

CREATE TABLE algorithm_identity_versions (
    identity_kind TEXT NOT NULL CHECK (identity_kind IN (
        'processing', 'consolidation', 'projection', 'embedding_document', 'embedding_query',
        'semantic_calibration', 'keyword_compiler', 'entity_normalization', 'predicate_registry',
        'graph_extraction', 'graph_scoring', 'query_planning', 'ranking_features', 'ranking_weights',
        'context_selection', 'feedback_aggregation', 'metric_schema')),
    identity TEXT NOT NULL CHECK (length(identity) > 0),
    version TEXT NOT NULL CHECK (length(version) > 0),
    generation INTEGER NOT NULL CHECK (generation > 0),
    state TEXT NOT NULL CHECK (state IN ('active', 'building', 'degraded', 'superseded')),
    created_at TEXT NOT NULL,
    activated_at TEXT,
    PRIMARY KEY (identity_kind, identity)
) WITHOUT ROWID, STRICT;

CREATE INDEX algorithm_identity_versions_state_idx
ON algorithm_identity_versions(identity_kind, state, generation, identity);

CREATE TABLE active_generation_metadata (
    scope TEXT NOT NULL CHECK (scope IN ('membership', 'ranking', 'graph', 'context', 'evaluation')),
    identity_kind TEXT NOT NULL CHECK (identity_kind IN (
        'processing', 'consolidation', 'projection', 'embedding_document', 'embedding_query',
        'semantic_calibration', 'keyword_compiler', 'entity_normalization', 'predicate_registry',
        'graph_extraction', 'graph_scoring', 'query_planning', 'ranking_features', 'ranking_weights',
        'context_selection', 'feedback_aggregation', 'metric_schema')),
    identity TEXT NOT NULL,
    generation INTEGER NOT NULL CHECK (generation > 0),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (scope, identity_kind),
    FOREIGN KEY (identity_kind, identity)
        REFERENCES algorithm_identity_versions(identity_kind, identity)
        ON DELETE RESTRICT
) WITHOUT ROWID, STRICT;

CREATE INDEX active_generation_metadata_identity_idx
ON active_generation_metadata(identity_kind, identity, generation);

COMMIT;
