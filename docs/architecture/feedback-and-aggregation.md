# Feedback, Ranking, And Aggregation

## Feedback

Feedback is an append-only observation about a concrete memory revision. The
authority record includes caller, operation, type, reason, time, and optional
search context. Search context can identify the search operation, query hash,
result rank, ranking version, and matched capabilities without storing query text in
ordinary logs.

Supported feedback types are `useful`, `irrelevant`, `correct`, `supplement`,
and `contest`. Corrective feedback can also create an evolution proposal, but
the feedback event and proposal have separate identities and outcomes. Feedback
does not directly archive, retract, delete, or revise a memory.

## Ranking Signal

Raw feedback events are authority. Rebuildable aggregates retain the latest
feedback per caller and revision within a 90-day window, count positive and
negative observations, and clamp the usage score to `[-1, 1]`. This bounded
score is applied after keyword and semantic primary recall and gated graph
feature calculation (graph internally distinguishes entity mentions, entity
facts, bounded paths, and memory relations); it cannot bypass scope,
current-revision, lifecycle, time, or budget checks.

Feedback belongs to the referenced revision and is not inherited by a newer
revision. Search responses and cursors carry the feedback aggregate version so
pagination does not mix ranking snapshots.

## Aggregate Memories

dMem supports reviewed `scene`, `topic`, and `timeline` aggregate memories over
an explicit, bounded, deduplicated list of input revisions. Creation first
produces a proposal. Confirmation revalidates scope, current inputs,
sensitivity, provenance, and processing identity, then creates a normal
immutable memory revision with `derived_from` relations. Aggregates never
replace their source memories, and a stale aggregate is excluded from default
recall rather than returned with a warning; explicit debug inclusion reports the
stale reason and provenance.

Input changes mark aggregate freshness stale and enqueue deduplicated rebuild
work. Rebuilt content returns to review instead of replacing confirmed content
automatically. Provenance remains queryable down to input revisions and their
evidence. Aggregate generation never crosses spaces implicitly.

## Decision Telemetry

Every recall appends one bounded, content-free decision record covering the
query plan, executed capabilities, candidate counts by source, gate rejections,
score summaries, graph seeds/edges/paths/promotions, duplicate clusters, context
budgets, algorithm identities, degradation, and phase latency. Recording the
identities per recall lets an operator distinguish a ready-but-empty stage from a
stale one.
