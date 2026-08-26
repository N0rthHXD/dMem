# Feedback, Ranking, And Aggregation

## Feedback

Feedback is an append-only observation about a concrete memory revision. The
authority record includes caller, operation, type, reason, time, and optional
search context. Search context can identify the search operation, query hash,
result rank, ranking version, and matched channels without storing query text in
ordinary logs.

Supported feedback types are `useful`, `irrelevant`, `correct`, `supplement`,
and `contest`. Corrective feedback can also create an evolution proposal, but
the feedback event and proposal have separate identities and outcomes. Feedback
does not directly archive, retract, delete, or revise a memory.

## Ranking Signal

Raw feedback events are authority. Rebuildable aggregates retain the latest
feedback per caller and revision within a 90-day window, count positive and
negative observations, and clamp the usage score to `[-1, 1]`. This bounded
score is applied after keyword, semantic, entity, and relation retrieval; it
cannot bypass scope, current-revision, lifecycle, time, or budget checks.

Feedback belongs to the referenced revision and is not inherited by a newer
revision. Search responses and cursors carry the feedback aggregate version so
pagination does not mix ranking snapshots.

## Aggregate Memories

dMem supports reviewed `scene` and `topic` aggregate memories over an explicit,
bounded list of input revisions. Creation first produces a proposal. Confirmation
revalidates scope, current inputs, sensitivity, provenance, and processing
identity, then creates a normal immutable memory revision with `derived_from`
relations. Aggregates never replace their source memories.

Input changes mark aggregate freshness stale and enqueue deduplicated rebuild
work. Rebuilt content returns to review instead of replacing confirmed content
automatically. Provenance remains queryable down to input revisions and their
evidence. Profile generation and autonomous cross-space profiling are not
implemented.
