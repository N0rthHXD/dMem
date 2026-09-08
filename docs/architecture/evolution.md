# Entities, Relations, Conflicts, And Evolution

dMem keeps model suggestions separate from reviewed authority. Entity,
relation, and evolution proposals are immutable specifications with explicit
review state, provenance, processing identity, and idempotency behavior.

## Entities

Entities are scoped authority with canonical names, typed aliases, sensitivity,
state, and source provenance. Mentions bind an entity to a concrete memory
revision or raw event and validate source range and scope. Alias normalization
can suggest identity but does not merge entities automatically. The public
entity merge route is unsupported.

## Relations

Relations are independent authority between concrete revisions. Supported types
are `supports`, `contradicts`, `supersedes`, `derived_from`, and `related_to`.
Entity mentions are separate authority. Relation assertions require evidence or explicit human confirmation, reject
self-relations, and enforce endpoint scope. Retrieval follows at most one hop;
historical endpoints can explain history but do not make stale revision text a
current result.

## Entity Facts

Entity facts (`entity_facts`) are distinct from revision-level relations: they
describe typed relationships among resolved entities, such as
`Alice --participates_in--> Alpha` or `Alpha --uses_database--> PostgreSQL`.
A fact has a subject entity, a controlled predicate, and either an object entity
or a typed literal (exactly one), plus source memory/revision provenance,
confidence, state, and valid time. Predicates come from a controlled registry
(`participates_in`, `uses_database`, `owned_by`, `located_in`, `depends_on`).

Facts are append-only authority: confirming a newer fact supersedes older active
facts with the same subject and predicate without deleting their history, so a
default query returns only currently-valid facts while a historical
`reference_time` can surface the fact valid at that time.

## Evolution

Evolution proposals cover duplicates, supplements, replacements, and conflicts;
new-memory candidates, entity mentions, and relations use their own proposal
resources. Confirmation checks every expected target revision and frozen state before committing. A supplement creates an
immutable child revision. Replacement must explicitly choose a new revision of
the same memory or a new memory connected by `supersedes`.

No confidence threshold automatically confirms a proposal. Reviews, resulting
revisions, evidence, relations, state changes, audit records, and follow-up jobs
are committed atomically or not at all.

## Conflict Cases

A confirmed conflict preserves both sides, creates a `contradicts` assertion,
and opens a conflict case. Contested memories remain recallable by default and
carry conflict references. Resolution is explicit and supports `keep_both`,
`dismiss`, `revise_one`, `supersede`, and `reconcile`.

Resolution validates all expected participant revisions. It appends resolution
history and never edits historical revisions or silently deletes a rejected
assertion. Automatic conflict adjudication and unbounded graph traversal are not
implemented.
