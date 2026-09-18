# Memory And Retrieval Optimization Plan

<!-- markdownlint-disable MD013 -->

> **Historical record.** This plan documents the design as it was implemented for
> feature 002. The evaluation harness and the old HTTP API/OpenAPI references it
> contains were removed by `specs/003-api-layer-refactor`. The memory, retrieval,
> graph, context, and feedback boundaries it defines remain current; see
> `docs/architecture/memory-gateway-refactor.md` for the current layering.

## Status And Scope

This document is the implementation plan for replacing dMem's current memory
processing and retrieval algorithms. It covers event processing, memory
representation, deduplication and evolution, aggregate construction, keyword and
semantic indexes, knowledge-graph construction, candidate generation, ranking,
context assembly, feedback, observability, and evaluation.

dMem has not been released. This plan deliberately does not preserve database,
API, index, cursor, ranking, checkpoint, or configuration compatibility. The
implementation should replace the current schema and contracts directly, assign
new version identities, and initialize a fresh data directory. It must not add
migration code, dual-read paths, fallback parsers, deprecated fields, or legacy
ranking behavior.

The following architectural boundaries remain valid:

- Raw events are immutable source authority and are committed before optional
  provider processing.
- A memory has a stable logical ID, immutable revisions, explicit evidence, and
  append-only lifecycle history.
- Model output is a proposal until deterministic validation and the applicable
  review policy accept it.
- FTS, embeddings, graph structures, features, aggregates, and retrieval caches
  are derived and rebuildable.
- Scope, lifecycle, sensitivity, policy, current revision, and valid-time filters
  are applied before top-k selection or graph traversal.
- Provider calls run outside SQLite write transactions and publish through
  lease, configuration, policy, and revision fences.

## Motivation

The current implementation has strong authority and recovery properties, but its
quality pipeline treats extraction, graph construction, recall, and context
selection too independently. This creates several failure modes:

- One event can produce many near-duplicate memories that consume index and
  context capacity.
- Confirmation of every new candidate turns extraction variation into permanent
  authority instead of consolidating equivalent facts.
- Memory text carries temporal, entity, and event structure only implicitly.
- Keyword and semantic retrieval use ranks but discard useful score margins.
- Graph expansion is seeded by primary retrieval, then receives an independent
  full RRF contribution, which double-counts correlated evidence.
- Low-confidence graph signals can receive the same fusion contribution as
  high-confidence signals when both are first in the graph channel.
- Broad `related_to` relations behave like a noisy semantic-similarity network
  and are allowed to propagate.
- Fixed top-k assembly admits duplicate memories and omits source time, speaker,
  provenance, diversity, and question intent.
- Global revision feedback is too weakly connected to the query and selection
  decision that produced the result.

The LoCoMo evaluation exposed the graph failure clearly. The evaluated corpus
contained 17,911 memories and 63,204 active memory relations. Of 49,777 active
`related_to` relations, only 416 had confidence at least 0.9, while every
`related_to` relation was automatically confirmed. Graph-only turn recall and
MRR were poor, and adding graph to semantic retrieval reduced first-evidence
ranking for substantially more questions than it improved. This benchmark is a
diagnostic signal, not the sole optimization target; the replacement must remain
domain-independent and preserve dMem's local authority model.

## Goals

The replacement should achieve the following outcomes:

1. Store fewer, denser, better-attributed memories without losing source
   evidence.
2. Separate explicit knowledge, event structure, provenance, and semantic
   similarity instead of representing all association as graph authority.
3. Make semantic retrieval the broad-recall backbone while retaining keyword
   precision for names, identifiers, dates, numbers, and exact phrases.
4. Use graph evidence only when query understanding and graph confidence justify
   it, with bounded influence and no correlated-channel double counting.
5. Rank candidates using calibrated query-dependent features rather than raw
   channel rank alone.
6. Assemble a compact, diverse, evidence-rich context under explicit token and
   character budgets.
7. Learn safely from feedback without allowing global popularity, one caller, or
   one noisy channel to dominate.
8. Make every retrieval decision explainable and reproducibly evaluable.

## Non-Goals

- Preserving existing SQLite files, index generations, logical exports, cursors,
  manifests, or checkpoints.
- Preserving current request or response shapes when a clearer contract is
  needed.
- Introducing an unbounded graph search or a graph database.
- Allowing model-generated facts, merges, contradictions, or destructive memory
  evolution to bypass deterministic checks and review policy.
- Optimizing only for LoCoMo at the expense of general agent memory workloads.
- Adding a large learned reranker before deterministic baselines and evaluation
  instrumentation are correct.

## Design Principles

### Evidence Before Fluency

Memory content is a retrieval and presentation view. Evidence, normalized
entities, temporal bounds, and event attributes are first-class data. A fluent
summary without complete provenance must not replace its sources.

### Consolidate Before Proliferating

New extraction output is first matched against current memories. Equivalent
information adds evidence; a refinement creates a revision or explicit
evolution proposal; only novel information creates a new logical memory.

### Independent Signals Earn Independent Weight

A signal derived from a semantic or keyword seed is not an independent third
vote. Graph expansion may supply features or gated candidates, but it must not
receive the same fusion treatment as independently generated primary recall.

### Confidence Must Survive The Pipeline

Extraction confidence, entity-resolution confidence, fact confidence, path
confidence, and retrieval score must not be reduced to rank before final
selection. Confidence is calibrated by signal type and used with hard gates and
bounded contributions.

### Selection Is Budgeted Coverage

The final result is not simply the first N ranked rows. It is a constrained
selection over relevance, evidence coverage, source diversity, temporal
coverage, redundancy, sensitivity, and context cost.

## Target Pipeline

```text
raw event
  -> ordered processing context
  -> structured candidate extraction
  -> deterministic validation
  -> candidate-to-memory matching
  -> consolidate / evolve / create / reject
  -> current memory projection
  -> FTS + embedding + knowledge graph + aggregate projections

query
  -> query analysis
  -> independent keyword and semantic recall
  -> calibrated primary ranking
  -> gated graph lookup and bounded expansion
  -> feature-based reranking
  -> duplicate and provenance-aware selection
  -> token-budgeted context package
  -> result and decision telemetry
  -> query-conditioned feedback
```

## Memory Model

### Memory Kinds

Replace the current broad text-first treatment with explicit retrieval roles:

| Kind | Purpose | Typical scope |
| --- | --- | --- |
| `fact` | Stable subject-attribute-value knowledge | Entity or task |
| `event` | Something that happened at a time involving participants | Session or task |
| `preference` | A stated preference with strength and validity | User or agent |
| `plan` | Intended future action with status and target time | User, project, or task |
| `procedure` | Reusable ordered instructions or operational knowledge | Project or application |
| `scene` | Reviewed bounded summary of one session or episode | Session or task |
| `topic` | Reviewed synthesis across explicit source revisions | Task, project, or user |
| `timeline` | Reviewed chronological synthesis for one entity or topic | Task, project, or user |

`scene`, `topic`, and `timeline` are normal memories with complete provenance;
they do not become a second content authority.

### Structured Revision Fields

Each revision retains display content and adds normalized retrieval fields:

```text
memory_id
revision_id
kind
display_content
canonical_content
subject_entity_ids[]
object_entity_ids[]
event_type
attributes{}
event_time_start
event_time_end
time_precision
time_source
valid_from
valid_until
source_speaker
language
sensitivity
specificity
confidence
```

The canonical fields are deterministic projections of reviewed revision
authority. Provider output may propose values, but validation normalizes types,
time intervals, entity references, and bounds before publication.

`event_time_*` describes when an event happened. `valid_*` describes when a fact
or preference is valid. They must not be conflated.

### Evidence Model

A revision can cite multiple evidence spans. Evidence ordering is explicit and
stable:

```text
event timestamp
event sequence
start offset
evidence ID
```

Evidence stores its support role:

- `direct`: the source explicitly states the memory.
- `derived`: the memory follows from several cited sources.
- `temporal_anchor`: the source anchors relative time.
- `context`: useful context that is not sufficient support by itself.

At least one `direct` or a valid set of `derived` evidence references is required
for a recallable revision. Context-only evidence cannot establish authority.

### Ordered Conversation Processing

Events require a stable sequence within their source stream. Add a caller- or
session-supplied sequence when available and otherwise assign one transactionally.
Processing context is ordered by source stream, sequence, timestamp, and stable
ID. Equal session timestamps must never cause UUID order to substitute for
dialogue order.

### Candidate Consolidation

Before creating a memory, retrieve a bounded consolidation set from the exact
target space using:

- canonical subject and kind equality;
- normalized entity and attribute overlap;
- valid-time overlap;
- exact canonical-content hash;
- high semantic similarity;
- shared evidence event or source stream proximity.

Classify a candidate into one action:

| Action | Meaning |
| --- | --- |
| `attach_evidence` | Equivalent memory; add non-duplicate evidence |
| `refine` | Same fact/event with a more precise value, time, or wording |
| `supersede` | New valid state replaces an old state |
| `contest` | Incompatible claim without sufficient resolution |
| `create` | Novel independent memory |
| `reject` | Empty, unsupported, redundant, or policy-invalid output |

Deterministic exact rules run first. A provider may classify only unresolved
cases, and its result remains a proposal. Consolidation thresholds are versioned
and evaluated per memory kind.

Initial semantic duplicate gates should be conservative:

- Same source event and cosine at least 0.90: merge candidate evidence unless
  normalized attributes conflict.
- Same subject, kind, compatible time, and cosine at least 0.94: propose
  `attach_evidence` or `refine`.
- Cosine alone never causes supersession, contest, or deletion.

### Memory Quality Score

Maintain a rebuildable quality projection for indexing and ranking:

```text
quality = calibrated(
  evidence strength,
  extraction confidence,
  specificity,
  temporal precision,
  source diversity,
  review status,
  contradiction state,
  duplicate density
)
```

Quality cannot make an ineligible memory eligible. It controls candidate
priority and context selection only after authority filters.

## Aggregate Strategy

Generate aggregates only after source memories have stabilized enough to avoid
summarizing extraction noise.

- `scene` summarizes one bounded episode or session.
- `topic` synthesizes a coherent subject across revisions.
- `timeline` orders events and state changes for one entity or topic.
- Every aggregate lists all direct input revisions and drills down to raw-event
  evidence.
- Aggregate inputs are deduplicated and bounded by both count and tokens.
- A source revision or state change marks the aggregate stale.
- Stale aggregates are excluded from default retrieval rather than returned with
  a warning.
- Rebuild creates a new proposal; it never silently edits a confirmed revision.

Aggregates are recall accelerators and reasoning aids. They do not consume a
primary result slot when a more specific source memory answers the query better.

## Query Analysis

Introduce a deterministic `QueryPlan` produced before recall:

```text
normalized_query
language
exact_terms[]
resolved_entities[]
entity_ambiguities[]
time_expression
reference_time
question_type
target_memory_kinds[]
target_predicates[]
requested_scope
graph_policy
candidate_budget
context_budget
```

Question types include `lookup`, `temporal`, `entity`, `relational`, `multi_hop`,
`procedural`, `exploratory`, and `verification`. Rules handle explicit dates,
identifiers, quoted strings, known aliases, and common interrogatives. Provider
query analysis is optional and may only enrich the plan within configured
egress policy and strict output bounds.

Ambiguous entity matches remain alternatives with normalized probabilities.
They are not resolved by UUID order. If context cannot disambiguate them, graph
expansion is disabled for that mention.

## Primary Recall

### Keyword Recall

Replace the current broad OR-only query compiler with a field-aware query:

- Preserve recognized entities, identifiers, dates, numbers, quoted phrases,
  acronyms, and short proper names regardless of generic token-length rules.
- Use exact phrase and canonical attribute fields as boosted clauses.
- Use AND for high-confidence required terms and OR for optional terms.
- Apply language-aware tokenization. CJK queries require unigram/bigram fallback
  when trigrams cannot be formed.
- Weight rare terms using corpus statistics and downweight common conversational
  words.
- Use one keyword implementation and one deterministic tie-break in all search
  paths.
- Return raw BM25, matched fields, exact-match flags, and query-term coverage.

### Semantic Recall

Semantic retrieval remains the broad-recall backbone.

- Embed canonical content plus compact structured fields, not display prose
  alone.
- Use memory-kind-specific prefixes and the same versioned formatting contract
  for indexing and query embedding.
- Retain raw cosine similarity and calibrate it by embedding identity, language,
  memory kind, and query type.
- Replace the global `0.2` floor with a calibrated absolute floor plus a
  query-relative margin from the best result.
- Maintain an exact-search implementation as the correctness baseline. Add ANN
  only when corpus scale requires it, with an exact rerank over the ANN window.
- A changed model, dimensions, instructions, structured formatting, or
  calibration identity creates a new semantic compatibility identity.

Recommended initial candidate policy:

```text
keep candidate when
  calibrated_similarity >= absolute_floor
  and calibrated_similarity >= top1_similarity - query_margin

cap semantic candidates by query plan and context budget
```

### Temporal Recall

Temporal behavior becomes a feature of primary recall rather than an independent
unbounded channel.

- Parse absolute and relative query time against `reference_time`.
- Filter impossible intervals before top-k.
- Score interval overlap, distance, precision, and anchor compatibility.
- Boost exact date, month, year, sequence, and relative-time matches.
- Preserve both source wording and normalized interval for answer context.

## Knowledge Graph Redesign

### Separate Knowledge From Similarity

The authority graph contains only typed, directional, evidence-backed knowledge:

```text
entity --predicate--> entity or typed literal
event --participant--> entity
event --occurred_at--> place
event --occurred_during--> time interval
memory revision --supports/derived_from/contradicts/supersedes--> revision
```

`related_to` is removed from graph authority. Semantic similarity is a derived
retrieval feature and must not be stored or traversed as a knowledge relation.
If a product feature needs explicit user-reviewed association, use a named,
directional relation with documented semantics.

### Predicate Registry

Replace the software-oriented fixed predicate set with a typed registry. Each
predicate defines:

```text
name
subject types
object types or literal type
directionality
inverse predicate
cardinality
transitivity policy
temporal policy
allowed path positions
auto-confirm policy
```

The initial general registry should cover identity and membership, people and
organizations, location, ownership, preference, work, participation, creation,
plans, event participants, event time, dependencies, and provenance. Free-form
provider predicates are rejected.

### Publication Policy

- Exact mentions of an already resolved entity may auto-confirm only when the
  extracted span and normalized surface match and mention confidence meets the
  predicate-independent threshold.
- New entities and aliases require high confidence and collision checks. Common
  terms, pronouns, and overly short aliases do not auto-confirm.
- Entity facts require valid subject/object types, direct evidence, confidence
  threshold, and cardinality/conflict checks.
- Revision relations require explicit semantics and complete provenance.
- No broad relation type bypasses confidence thresholds.
- Mention confidence is persisted and used during query-time scoring.
- Auto-review thresholds and rules are versioned by entity type, predicate, and
  source class.

### Graph Query Policy

Graph is a gated secondary mechanism, not a default independent vote.

The query plan chooses one policy:

| Policy | Behavior |
| --- | --- |
| `off` | No graph lookup |
| `features_only` | Add graph features to primary candidates; add no candidates |
| `direct` | Permit high-confidence direct mention and exact fact candidates |
| `bounded_path` | Permit an intent-matched path of at most two edges |

Default policy is `features_only`. `direct` requires an unambiguous resolved
entity. `bounded_path` additionally requires explicit relational or multi-hop
intent and compatible target predicates.

### Graph Seeds And Expansion

- Primary seeds are restricted to calibrated semantic top 10 and keyword top 10,
  deduplicated to at most 15 revisions.
- Weak candidates below primary score gates never become graph seeds.
- Similarity, graph-expanded, and relation-expanded candidates never become
  seeds.
- Direct entity lookup does not need a primary seed, but it requires unambiguous
  resolution and confidence gating.
- Each edge is filtered by scope, state, current revision, valid time, predicate,
  and minimum confidence before fan-out or LIMIT.
- Edge windows order by intent match, confidence, specificity, recency, and
  stable ID. UUID order is only the final tie-break.
- Two-hop traversal permits typed heterogeneous paths defined by the predicate
  registry. It does not require the same predicate on both hops.
- High-degree entities and edges receive an inverse-frequency penalty. Generic
  hubs cannot dominate a query merely by having many mentions.

### Graph Candidate Gate

A graph-only candidate may enter the primary rerank set only when all conditions
hold:

```text
query plan is direct or bounded_path
entity resolution is unambiguous
graph score is at least the policy threshold
supporting path has direct evidence
signal is an exact fact, direct mention, or allowed typed path
candidate is not produced only by a revision relation
```

Revision relations normally contribute features to existing candidates. They do
not produce graph-only candidates unless the relation type is explicitly allowed
by the query plan.

### Graph Score

Graph score remains numeric through final ranking:

```text
edge_score =
  predicate_intent
  * entity_resolution_confidence
  * fact_or_relation_confidence
  * evidence_strength
  * temporal_compatibility
  * hub_penalty

path_score = path_type_weight * product(edge_scores) * hop_decay

graph_score = max(valid direct, fact, and path scores)
```

Repeated graph evidence can add a small capped corroboration bonus, but cannot
increase score linearly with degree.

Graph availability is fail-closed for ranking. A stale, rebuilding, incompatible,
or partially populated graph is removed from executed retrieval and reported as
degraded. It must not silently alter ranking.

## Candidate Ranking

### Stage 1: Candidate Union

Take the bounded union of independently generated keyword and semantic
candidates plus graph-only candidates that pass the strict gate. Apply authority
and projection checks before feature extraction.

### Stage 2: Feature Calculation

Calculate query-local normalized features:

```text
semantic similarity and percentile
BM25 and keyword term coverage
exact entity, identifier, phrase, date, and number matches
memory quality
kind compatibility
temporal compatibility
scope distance
freshness where query-relevant
graph score and graph signal type
primary-channel agreement
aggregate specificity
global and query-conditioned feedback
duplicate-cluster size
estimated context cost
```

Missing features remain missing; they are not assigned a misleading zero that
looks like negative evidence.

### Stage 3: Deterministic Ranker

Replace equal-weight three-channel RRF with a versioned weighted feature ranker.
The initial deterministic formula is:

```text
primary =
    1.00 * semantic_calibrated
  + 0.35 * keyword_calibrated
  + 0.15 * exact_match
  + 0.10 * temporal_match
  + 0.10 * memory_quality
  + 0.05 * primary_agreement

graph_boost =
  graph_gate
  * min(0.10 * primary, 0.08 * graph_calibrated)

penalty =
    0.15 * duplicate_risk
  + 0.10 * aggregate_overbreadth
  + 0.10 * stale_or_contested_penalty

final = primary + graph_boost + bounded_feedback - penalty

graph_only =
  graph_candidate_gate
  * (
      0.45 * graph_calibrated
    + 0.15 * exact_match
    + 0.10 * temporal_match
    + 0.10 * memory_quality
  )
```

Weights are starting values, not fixed truths. They must be calibrated on fixed
development sets and promoted only through quality gates. The graph boost is
both gated and capped relative to primary relevance, so graph cannot displace a
strong direct semantic match with a weak association.

Graph-only candidates use `graph_only`, a separate admission floor, and a
protected-rank rule. They cannot enter above a strong primary exact match unless
they are backed by an exact fact or intent-compatible path, and they never
receive `graph_boost` in addition to `graph_only`.

### Stage 4: Optional Learned Reranker

Introduce a learned or provider reranker only after deterministic features,
telemetry, and labels are reliable. It receives a small top window, never the
whole corpus, and cannot bypass authority filters. Its identity includes model,
prompt, feature schema, and calibration version. The deterministic ranker
remains an offline and degraded-mode fallback.

## Result Diversification And Context Assembly

Ranking produces candidate relevance. Context assembly solves a different
budgeted selection problem.

### Duplicate Clustering

Cluster candidates using:

- same logical memory;
- shared primary evidence event;
- equivalent canonical fact tuple;
- high semantic similarity and compatible time;
- aggregate-to-source provenance.

Only the best representative consumes a normal context slot. Additional members
contribute evidence references or structured corroboration without duplicating
their prose.

### MMR Selection

Select under token, character, result-count, source, and sensitivity budgets:

```text
utility =
  relevance
  + evidence_coverage_gain
  + entity_or_session_coverage_gain
  - redundancy
  - context_cost
```

Question-type policies adjust the coverage term:

- Lookup and verification favor one precise source.
- Temporal questions favor event plus temporal anchor.
- Multi-hop questions favor distinct supporting events and path coverage.
- Procedural questions preserve ordered steps.
- Exploratory questions favor topic and source diversity.

### Context Package

Return structured context items instead of bare memory prose:

```text
rank
memory and revision IDs
kind
display content
event and validity time
source speaker
evidence references
matched features and channels
graph explanation when used
confidence and lifecycle state
estimated tokens
```

The plain-text rendering includes dates and speakers only when present; it does
not claim that reconstructed conversation dates exist when they were omitted.
Aggregates link to selected source memories rather than duplicating all source
text.

Context assembly stops at a score discontinuity or budget boundary instead of
always filling a fixed top-k. Callers can request a maximum budget but cannot
force ineligible or low-quality results into the package.

## Feedback And Online Adaptation

Keep feedback append-only but separate three uses:

1. `memory_quality_feedback`: correctness, contest, and durable memory quality.
2. `retrieval_feedback`: useful or irrelevant for a query and selected result.
3. `answer_feedback`: whether the assembled context supported the downstream
   answer.

Retrieval feedback stores privacy-preserving query features and decision context:

```text
query hash
query type
resolved entity IDs where policy permits
ranking and feature-schema version
candidate rank
selected or rejected state
matched signal types
caller
timestamp
```

Global feedback remains a small bounded prior. Query-conditioned feedback may
adjust calibrated features only after minimum support, time decay, caller
deduplication, and poisoning limits. Feedback never changes authority or bypasses
review.

Maintenance rebuilds and online aggregation must share one implementation and
one versioned definition. There must not be separate feedback semantics for
normal operation and recovery.

## Schema And Contract Replacement

Because no compatibility is required, implement the target model as a direct
schema replacement.

### Remove Or Replace

- Remove broad `related_to` graph authority and its auto-confirm behavior.
- Replace current graph extraction output and predicate registry.
- Replace rank-only graph candidates with scored graph evidence.
- Replace equal-weight `rrf-v3` with a new feature-ranking identity.
- Replace bare ranked result assembly with explicit candidate and context phases.
- Replace first-evidence-only evaluation mappings with complete evidence sets.
- Replace duplicate keyword recall implementations with one implementation.
- Replace stale aggregate default inclusion with default exclusion.
- Replace ambiguous entity UUID selection with explicit alternatives or no graph
  expansion.

### New Version Identities

At minimum version independently:

```text
processing contract
candidate consolidation rules
canonical memory projection
embedding document format
embedding query format
semantic calibration
keyword query compiler
entity normalization
predicate registry
graph extraction contract
graph scoring
query planning
ranking feature schema
ranking weights or model
context selection
feedback aggregation
evaluation metric schema
```

Cursor and checkpoint compatibility binds all identities that affect membership,
rank, or rendered context. Since old compatibility is intentionally discarded,
new decoders accept only the new versions.

### API Direction

The recall request should express intent and budget rather than expose internal
channel composition as the primary user abstraction:

```json
{
  "query": "...",
  "space_ids": ["..."],
  "include_ancestors": true,
  "reference_time": "...",
  "intent": "auto",
  "max_results": 20,
  "max_context_tokens": 4000,
  "include_evidence": true,
  "explain": false
}
```

Internal channel overrides may exist behind an internal diagnostics field, but
production defaults come from query planning. The response distinguishes raw
ranked candidates from selected context only when explicitly requested for
debugging; normal callers receive the selected context package and execution
metadata.

## Evaluation And Quality Gates

### Correct Metrics First

Before algorithm comparison, replace incorrect or ambiguous evaluation metrics:

- `recall_all@k` is `gold <= set(top_k)`.
- nDCG ideal relevance contains `min(k, len(gold))` relevant items regardless of
  what the system retrieved.
- MRR is either reported once over the full bounded list or explicitly truncated
  at k.
- Report top-k returned memories separately from top-k unique evidence turns and
  top-k sessions.
- Map every evidence reference on a revision, not only the first one.
- Report unmapped-result rate, duplicate rate, context utilization, and whether
  each gold source was represented by any indexed memory before retrieval.
- Equal coverage and identical query sets are mandatory for comparisons.

### Evaluation Layers

Measure each stage independently:

| Layer | Required measurements |
| --- | --- |
| Extraction | supported precision, omission rate, hallucination rate, candidates per event |
| Consolidation | duplicate merge precision/recall, false merge rate, memories per event |
| Indexing | eligible coverage, stale rate, build time, storage |
| Recall | recall, evidence coverage, first relevant rank, candidate count |
| Ranking | nDCG, MRR, pairwise wins, graph promotion harm |
| Selection | duplicate rate, source/session coverage, tokens, useful density |
| End-to-end | answer F1, semantic judge, abstention, latency, cost |

### Fixed Ablations

Use one immutable fresh corpus for retrieval ablations:

| ID | Semantic | Keyword | Graph features | Graph candidates | Diversification |
| --- | --- | --- | --- | --- | --- |
| S | yes | no | no | no | no |
| SK | yes | yes | no | no | no |
| SKF | yes | yes | yes | no | no |
| SKD | yes | yes | direct/fact | gated direct/fact | no |
| SKP | yes | yes | all valid | gated path | no |
| SKPM | yes | yes | all valid | gated path | MMR |

Additional graph ablations isolate mentions, facts, revision relations, one-hop
paths, two-hop paths, hub penalties, confidence gates, seed budgets, and boost
caps. Never evaluate only “graph on/off,” because that cannot identify which
graph family causes benefit or harm.

### Promotion Gates

Initial quality gates for a ranking release:

- No statistically credible regression in semantic-only or keyword-only recall.
- Adding graph must not reduce overall Recall@1 or MRR by more than 0.5 percentage
  points.
- Graph-promoted top-10 candidates must have measured precision of at least 50%
  on the development set before graph candidates are enabled by default.
- Graph must produce positive paired end-to-end gain in at least one intended
  category without a larger aggregate regression elsewhere.
- Top-context duplicate rate decreases by at least 50% from the current baseline.
- Multi-evidence coverage and temporal answer quality improve without increasing
  the context token median by more than 10%.
- Scope leakage, stale revision return, invalid evidence, and graph traversal
  outside valid-time bounds remain zero.
- P95 latency, allocation, database growth, and provider cost remain within
  separately recorded budgets.

End-to-end model generation and judging are repeated with fixed parameters and
multiple runs. Report means, standard deviations, paired bootstrap confidence
intervals, per-category changes, and evidence-count buckets. Small differences
without confidence intervals are not promotion evidence.

## Observability

Every recall operation records bounded decision telemetry without ordinary query
text logging:

```text
query-plan version and type
requested and executed capabilities
candidate counts by source
filter and gate rejection counts
score distribution summaries
graph seed, edge, path, and promoted-candidate counts
duplicate clusters and selected representatives
context tokens and characters
index and ranking identities
degradation reasons
latency by phase
```

Explain mode returns per-result feature contributions, graph paths, evidence, and
selection or rejection reasons. It is intended for debugging and evaluation and
must enforce the same policy and sensitivity rules as normal recall.

## Implementation Plan

The session-oriented implementation checklist is maintained in
[`specs/002-memory-retrieval-optimization/tasks.md`](../../specs/002-memory-retrieval-optimization/tasks.md).
That checklist is the execution record for task status, dependencies, focused
tests, phase checkpoints, and cross-session handoff.

### Phase 0: Evaluation And Instrumentation

1. Correct retrieval metrics and add monotonicity/unit tests.
2. Preserve complete evidence mappings in evaluation records.
3. Add memory-level, evidence-level, session-level, duplicate, and unmapped
   metrics.
4. Record build, schema, processing, embedding, graph, ranking, and context
   identities in manifests.
5. Add graph readiness barriers and fail degraded evaluation runs by default.
6. Extend recall telemetry with raw channel scores and graph signal families.

Exit criteria: historical result files can be recomputed under the new metric
schema where they contain sufficient data, and fresh runs cannot silently mix
incompatible checkpoints.

### Phase 1: Graph Containment And Ranker Replacement

1. Remove `related_to` from graph authority and extraction.
2. Persist mention confidence and enforce entity, alias, fact, and relation
   publication thresholds.
3. Require graph readiness before execution.
4. Restrict graph seeds and order graph windows by quality before LIMIT.
5. Preserve graph scores through candidate fusion.
6. Introduce `QueryPlan` graph policies and strict graph-only gates.
7. Replace equal RRF with the deterministic feature ranker and graph boost cap.
8. Change production default retrieval to semantic plus keyword with graph
   `features_only` when ready.

Exit criteria: graph no longer causes a material head-ranking regression, and
each graph promotion is attributable to a high-confidence explicit fact or path.

### Phase 2: Memory Consolidation And Structured Time

1. Add ordered source-stream sequence.
2. Replace extraction contract with structured memory candidates.
3. Add canonical revision fields and event/valid time separation.
4. Add deterministic candidate matching and consolidation actions.
5. Add duplicate clusters and quality projections.
6. Rebuild FTS and embeddings from canonical content.

Exit criteria: duplicate memory and context rates fall substantially without
reducing extraction evidence coverage; temporal retrieval improves on fixed
tests.

### Phase 3: Typed Event Graph

1. Replace predicate schema and graph provider contract.
2. Add typed events, literals, cardinality, inverse relations, and path policies.
3. Implement ambiguity-safe entity resolution and hub penalties.
4. Implement intent-compatible heterogeneous one- and two-hop traversal.
5. Add graph-specific fixed corpora and performance gates.

Exit criteria: graph candidates provide positive precision-controlled gain for
relational and multi-hop queries and remain disabled for unsupported intent.

### Phase 4: Context Selection And Aggregates

1. Add provenance-aware duplicate clustering.
2. Add MMR-style budgeted selection.
3. Add structured context rendering with time, speaker, and evidence.
4. Add timeline aggregates and default stale-aggregate exclusion.
5. Add query-type-specific selection policies and score-discontinuity stopping.

Exit criteria: selected contexts are smaller or equal in median size, contain
fewer duplicates, cover more distinct required evidence, and improve end-to-end
answer quality.

### Phase 5: Feedback Calibration And Optional Reranking

1. Separate memory, retrieval, and answer feedback.
2. Unify online and maintenance aggregate semantics.
3. Add query-conditioned bounded feedback with minimum-support and poisoning
   defenses.
4. Calibrate deterministic weights on fixed development sets.
5. Evaluate an optional small-window learned reranker against the deterministic
   baseline.

Exit criteria: feedback produces repeatable gains without caller dominance,
scope leakage, ranking instability, or authority mutation.

## Expected Code Areas

The implementation will directly modify or replace contracts in:

```text
internal/domain/
internal/storage/schema-v1.sql
docs/storage/schema-v1.sql
internal/provider/
internal/jobs/
internal/storage/memories.go
internal/storage/semantic.go
internal/storage/semantic_search.go
internal/storage/keyword_query.go
internal/storage/search.go
internal/storage/graph.go
internal/storage/graph_facts.go
internal/storage/graph_recall.go
internal/storage/feedback.go
internal/aggregate/
# removed by specs/003-api-layer-refactor:
#   the former API package, the former OpenAPI contract, and the
#   evaluation harness
internal/storage/testdata/
```

Existing ADRs that define equal RRF, broad graph relations, old predicates, or
stale aggregate behavior should be marked superseded after the replacement
contracts are accepted. Do not retain their behavior in code for compatibility.

## Risks And Controls

| Risk | Control |
| --- | --- |
| False duplicate merge | Conservative gates, immutable revisions, proposal review, false-merge evaluation |
| Structured extraction omissions | Preserve display content and raw evidence; measure field and evidence coverage |
| Graph becomes too sparse | Keep semantic/keyword primary recall; expand typed predicates only from measured misses |
| Query planner misclassification | Safe default to semantic+keyword and graph `off`/`features_only` |
| Weight overfitting | Fixed holdout sets, cross-benchmark gates, paired confidence intervals |
| Context diversity hurts exact lookup | Query-type policies and exact-match protection |
| Increased write cost | Bounded consolidation windows, indexed canonical keys, asynchronous provider resolution |
| Increased read latency | Bounded candidate windows, precomputed projections, phase latency gates |
| Feedback poisoning | Caller deduplication, minimum support, clamping, decay, audit, no authority mutation |

## Completion Definition

The optimization is complete when:

- The new schema and API are the only supported contracts.
- Fresh initialization, rebuild, backup/restore, and logical portability use the
  new model with no legacy branches.
- Extraction consolidates equivalent information and preserves complete ordered
  evidence.
- Keyword, semantic, temporal, and graph features are calibrated and observable.
- Graph cannot independently promote weak similarity or low-confidence
  association.
- Ranking and context selection are separate, versioned, deterministic stages.
- Evaluation metrics are correct, stage-specific, and reproducible.
- Fixed quality, safety, recovery, performance, and end-to-end benchmark gates
  pass on a fresh data directory.

## Completion Record

Recorded at the end of Phase 7. The authoritative task-level record, including
raw gate output, is the Final Completion Record in
[`specs/002-memory-retrieval-optimization/tasks.md`](../../specs/002-memory-retrieval-optimization/tasks.md).

### Accepted Algorithm Identities

| Stage | Identity |
| --- | --- |
| Schema | `schema-v1` (`schema_version = 1`) |
| Processing | processor `builtin-noop-v1` under provider contract `processing-structured-v1` |
| Consolidation | `consolidation-v1` (hash on, cosines 0.90/0.94, 32 candidates) |
| Embedding document/query | `embedding-document-v2` / `embedding-query-v1` over `canonical-document-v1` |
| Semantic calibration | `semantic-calibration-v1` (floor 0.35, margin 0.08) |
| Keyword compiler | `keyword-compiler-v2` |
| Predicate registry | `predicate-registry-v2` |
| Graph extraction/scoring | `graph-typed-v1` / `graph-scoring-v3`; `auto-review-v1`, `entity-graph-statistics-v2` |
| Query plan / temporal | `query-plan-v2` / `temporal-features-v1` |
| Ranking features/weights | `ranking-features-v1` / `ranking-weights-v1` |
| Context selection | `context-selection-v2` / `context-tokens-v1` |
| Feedback | `v2` (90d window, minimum support 3, half-life 45d) |
| Evaluation metric | `locomo-retrieval-metrics-v2`, `longmemeval-retrieval-metrics-v2` |

### Accepted Evidence

No end-to-end LoCoMo or LongMemEval provider run is committed. Accepted
evidence is the deterministic quality and performance gates. The former
evaluation calibration report and the removed evaluation harness documentation
are not part of the current repository. Calibrated promotion remains rejected and
the deterministic default weights above remain authoritative; benchmark
test-set labels remain holdout-only and never enter runtime feedback or weight
fitting.

### Capacity Bounds

Consolidation accepts at most 32 candidates. Structured extraction is bounded at
32 candidates, 4096 content characters, 100 evidence spans, and 64 entity
references per event. Graph items are bounded at 256, with seeds limited to the
calibrated semantic top 10 plus keyword top 10 deduplicated to at most 15
revisions, at most two hops, and hub-penalized bounded fan-out. Context is
bounded at 4000 tokens, 16000 characters, and 20 results, with semantic
candidates capped by the query plan and context budget. Feedback priors are
capped at 0.001 global and 0.0005 query-conditioned.

### Known Limitations

- Exact cosine search remains the only semantic implementation. The ANN
  extension point and exact rerank are tested, but ANN is not implemented
  because measured scale did not require it.
- The learned reranker is diagnostic-only; production uses the deterministic
  ranker and its fallback.
- Calibration promotion is not met on the fixed synthetic development set, so
  weights remain the deterministic defaults rather than fitted values.
- Providers are disabled by default. Keyword recall, direct memory, query
  planning, and degraded graph work offline without secrets or network; structured
  extraction and graph authority require a configured provider.
- Graph health reports unavailable until a graph index generation exists, and
  ranking then uses `features_only` or `off` instead of graph candidates.
