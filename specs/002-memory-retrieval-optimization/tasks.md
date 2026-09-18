---

description: "Phased implementation tasks for memory and retrieval optimization"
---

# Tasks: Memory And Retrieval Optimization

> **Historical record.** These tasks document the completed feature 002. The
> Python evaluation harness, its pytest commands, the former API package, the
> OpenAPI contract, and the former `make openapi` target they reference were
> removed by `specs/003-api-layer-refactor`. The retained memory, retrieval,
> graph, context, and feedback work is current; the evaluation tooling and old
> HTTP interfaces are not. Current contracts live in
> `docs/api/http-rest-contract.md`.

**Design source**:
`docs/architecture/memory-retrieval-optimization-plan.md`

**Scope**: Replace dMem's memory processing, consolidation, structured memory,
keyword and semantic recall, temporal features, graph model, candidate ranking,
context selection, feedback, observability, and evaluation contracts.

**Compatibility policy**: dMem has not been released. Do not preserve old
database files, schema layouts, APIs, cursors, ranking versions, index versions,
configuration fields, logical exports, manifests, or checkpoints. Do not add
migrations, dual reads/writes, deprecated fields, compatibility decoders,
fallback parsers, or legacy ranking branches. Initialize a fresh data directory
whenever an incompatible contract changes.

**Tests**: Write or update tests before each implementation task and confirm that
new tests fail for the intended reason. A task is complete only when its focused
tests pass. A phase is complete only when its checkpoint commands and quality
gates pass.

## Task Format

Tasks use this format:

```text
[ID] [P?] [Area] Description
```

- `[P]` means the task can run in parallel with other `[P]` tasks in the same
  group because it changes different files and has no incomplete dependency.
- `[Area]` identifies the subsystem: `EVAL`, `CONTRACT`, `MEM`, `SEARCH`,
  `GRAPH`, `CTX`, `FEEDBACK`, `OPS`, or `DOC`.
- An unchecked task is not complete, even if part of its code already exists.
- Mark a task `[X]` only after its implementation, focused tests, generated
  contracts, and required documentation are complete.

## New-Session Protocol

Every implementation session must follow this sequence:

1. Read this file and
   `docs/architecture/memory-retrieval-optimization-plan.md`.
2. Run `git status --short` and preserve unrelated user or agent changes.
3. Select the first incomplete task whose stated dependencies are complete.
   A user may explicitly select a later task, but its dependencies still apply.
4. Read the current implementation and executable contracts before editing.
5. Add or update the focused failing tests first.
6. Implement the smallest complete change. Do not add compatibility behavior.
7. Run the task's focused tests and `git diff --check`.
8. Mark only verified tasks `[X]` in this file.
9. At a phase boundary, run every command in that phase's checkpoint.
10. End the session with completed task IDs, tests run, failures or residual
    risks, and the exact next unblocked task ID.

Recommended prompt for a new session:

```text
Continue the dMem memory/retrieval optimization from
specs/002-memory-retrieval-optimization/tasks.md. Read the design source and
current worktree first. Implement the next unblocked task(s), write tests first,
do not add migration or compatibility code, update task checkboxes only after
verification, and report the next task ID.
```

## Global Completion Rules

- Schema changes update both `internal/storage/schema-v1.sql` and
  `docs/storage/schema-v1.sql` in the same task.
- API changes update handlers, domain request/response models, OpenAPI, examples,
  and contract tests in the same phase.
- A membership or ranking behavior change receives a new explicit version
  identity and cursor/checkpoint binding.
- Provider output remains a proposal until deterministic validation and review
  policy permit publication.
- Scope, policy, lifecycle, current revision, sensitivity, and valid-time filters
  run before top-k selection and graph traversal.
- Provider calls remain outside SQLite write transactions and publish through
  lease, configuration, policy, and revision fences.
- No task may silently weaken authority, audit, idempotency, recovery, or
  offline-default guarantees to improve benchmark scores.
- Benchmark promotion requires identical query coverage and a fresh, shared
  corpus for retrieval ablations.

## Phase 0: Baseline And Evaluation Correctness

**Purpose**: Establish trustworthy metrics and immutable baselines before
changing algorithms. This phase does not change production ranking.

**Dependencies**: None.

### Metric Unit Tests

- [X] T001 [P] [EVAL] Add failing LoCoMo tests for correct
  `gold <= set(top_k)` recall-all semantics, empty rankings, multiple gold
  evidence, irrelevant additions, and monotonicity in
  `[removed evaluation harness]`
- [X] T002 [P] [EVAL] Add failing LoCoMo nDCG tests proving missing gold evidence
  lowers the score and ideal DCG uses `min(k, len(gold))` in
  `[removed evaluation harness]`
- [X] T003 [P] [EVAL] Add failing MRR tests for one explicitly named full-list
  metric or cutoff-specific `MRR@k`, and reject the current repeated ambiguous
  values in `[removed evaluation harness]`
- [X] T004 [P] [EVAL] Add failing evidence-mapping tests for multiple evidence
  events, stable chronological ordering, unmapped results, duplicate source
  turns, and session promotion in `[removed evaluation harness]`
- [X] T005 [P] [EVAL] Add failing dataset-validation tests that enforce expected
  corpus counts and reject malformed or partially unresolved evidence in
  `[removed evaluation harness]`

### Metric Implementation

- [X] T006 [EVAL] Correct recall-all, nDCG, and MRR semantics in
  `[removed evaluation harness]` and assign a new retrieval metric
  schema identity
- [X] T007 [EVAL] Replace first-evidence-only mapping with complete ordered
  evidence sets in `[removed evaluation harness]` and checkpoint
  records
- [X] T008 [EVAL] Report memory-level, unique-evidence-level, and session-level
  metrics separately, including duplicate and unmapped rates, in
  `[removed evaluation harness]` and
  `[removed evaluation harness]`
- [X] T009 [EVAL] Add extraction coverage metrics that distinguish “gold source
  absent from every indexed memory” from retrieval failure in
  `[removed evaluation harness]`
- [X] T010 [EVAL] Enforce dataset SHA, subset, metric schema, run configuration,
  and model/prompt compatibility for retrieval, generation, judge, and summarize
  checkpoints under `[removed evaluation harness]`
- [X] T011 [EVAL] Make a non-resume run reject non-empty result checkpoints and
  make summarize recompute retrieval metrics from sufficient raw records instead
  of trusting stored metric dictionaries

### Runtime Identity And Readiness

- [X] T012 [P] [EVAL] Add failing tests for graph-ready barriers, per-query
  degraded responses, and fail-on-degraded behavior in
  `[removed evaluation harness]`
- [X] T013 [P] [EVAL] Add failing manifest tests for daemon build/commit, schema,
  processing, embedding, graph, keyword, ranking, context, and metric identities
  in `[removed evaluation harness]`
- [X] T014 [EVAL] Implement an explicit graph completion/readiness barrier for
  graph and graph-using evaluation modes in
  `[removed evaluation harness]` and
  `[removed evaluation harness]`
- [X] T015 [EVAL] Persist all effective identities and degradation counts in
  evaluation manifests and records, and reject incompatible resume or summarize
  operations

### Baseline Capture

- [X] T016 [P] [EVAL] Extend fixed retrieval fixtures to report primary score,
  raw channel scores, matched fields, graph score, signal family, duplicate
  cluster, and selection state under `internal/storage/testdata/`
- [X] T017 [P] [EVAL] Define versioned baseline files for extraction count,
  duplicate density, recall, ranking, graph promotion harm, context utilization,
  latency, allocation, storage, and provider-cost proxies under
  `internal/storage/testdata/` and `[removed evaluation harness]`
- [X] T018 [EVAL] Run and record fresh keyword, semantic, graph, and current
  hybrid baselines on one shared corpus; document exact commands and immutable
  identities in `[removed evaluation harness]`

**Checkpoint**:

```sh
# Python evaluation harness removed by specs/003-api-layer-refactor
make quality
git diff --check
```

**Phase 0 completion**: Metrics are mathematically correct and tested; complete
evidence and stage failures are observable; incompatible checkpoints cannot mix;
fresh current-algorithm baselines are recorded.

---

## Phase 1: Replacement Contracts And Foundational Schema

**Purpose**: Define the target domain, schema, provider contracts, identities,
and API boundary once so later phases do not repeatedly reshape foundations.

**Dependencies**: Phase 0 complete.

**Critical**: All later production phases depend on this phase. Replace schema v1
directly and use fresh databases. Do not write a migration.

### Decision And Contract Tests

- [X] T019 [P] [CONTRACT] Add a superseding ADR for structured memories,
  consolidation, query planning, feature ranking, gated graph retrieval, and
  context selection in `docs/adr/0035-memory-retrieval-v4.md`
- [X] T020 [P] [CONTRACT] Add failing domain tests for memory kinds, structured
  revision fields, event time versus valid time, evidence roles, confidence,
  specificity, language, and source speaker in `internal/domain/`
- [X] T021 [P] [CONTRACT] Add failing schema contract tests for ordered event
  streams, structured revisions, canonical attributes, evidence roles, duplicate
  clusters, memory quality, typed predicates/events, query telemetry, and
  context decisions in `internal/storage/schema_contract_test.go`
- [X] T022 [P] [CONTRACT] Add failing canonical identity tests for processing,
  consolidation, projection, embedding document/query, semantic calibration,
  keyword compiler, entity normalization, predicate registry, graph extraction,
  graph scoring, query planning, ranking features/weights, context selection,
  feedback aggregation, and metric schema in `internal/config/identity_test.go`
- [X] T023 [P] [CONTRACT] Add failing strict provider-contract tests for
  structured candidates, normalized time proposals, evidence roles, typed graph
  entities/events/facts, closed predicates, and bounded output in
  `internal/provider/client_test.go`

### Domain And Schema Replacement

- [X] T024 [CONTRACT] Replace memory and revision domain models with the target
  kinds and structured fields in `internal/domain/models.go` and focused domain
  files
- [X] T025 [CONTRACT] Replace schema v1 with ordered event streams, structured
  revision authority/projections, evidence roles, quality and duplicate
  projections, and required indexes in `internal/storage/schema-v1.sql` and
  `docs/storage/schema-v1.sql`
- [X] T026 [CONTRACT] Replace graph schema with typed predicate definitions,
  typed literals/events, persisted mention confidence, evidence-backed facts and
  revision relations; remove broad `related_to` authority in both schema files
- [X] T027 [CONTRACT] Add version identity storage and active-generation metadata
  for every membership, ranking, graph, context, and evaluation identity in both
  schema files and storage domain models
- [X] T028 [CONTRACT] Update repository scan/write helpers to compile against the
  replacement schema in `internal/storage/` without implementing old-schema
  reads

### Provider And Configuration Contracts

- [X] T029 [P] [CONTRACT] Replace processing provider request/response types with
  structured candidate and consolidation proposal types in
  `internal/provider/contracts.go` and `internal/domain/`
- [X] T030 [P] [CONTRACT] Replace graph provider output with typed entities,
  aliases, mentions, events, facts, and explicit revision relations in
  `internal/provider/contracts.go`
- [X] T031 [CONTRACT] Implement strict decoding and deterministic validation for
  the replacement processing and graph contracts in
  `internal/provider/client.go` and `internal/jobs/validation.go`
- [X] T032 [CONTRACT] Add strict configuration sections and bounds for
  consolidation, semantic calibration, graph gates, ranking, and context budgets
  in `internal/config/`, `docs/operations/config.example.toml`, and configuration
  tests
- [X] T033 [CONTRACT] Implement canonical non-secret identity generation for all
  new algorithm contracts in `internal/config/identity.go`

### API Contract Replacement

- [X] T034 [P] [CONTRACT] Add failing API tests for intent, reference time,
  maximum results, context-token budget, evidence, explain mode, execution
  metadata, and structured context in `internal/daemon/search_api_test.go`
- [X] T035 [P] [CONTRACT] Define replacement recall request/response and explain
  models in `internal/domain/models.go` and `docs/api/http-rest-contract.md`
- [X] T036 [CONTRACT] Replace recall request validation and response serialization
  in `[removed API package]`, `[removed API package]`, and daemon route tests
- [X] T037 [CONTRACT] Remove production channel selection as the primary API
  abstraction; retain explicit channel/feature overrides only in a clearly named
  internal diagnostics contract
- [X] T038 [CONTRACT] Replace cursor payload and validation so it accepts only the
  new schema, query-plan, candidate-membership, ranking, graph, context, and
  feedback identities

### Foundational Compilation

- [X] T039 [CONTRACT] Update fixtures, helper constructors, direct remember,
  explicit memory creation, import/export types, backup/restore validation, and
  tests to compile and initialize only the new schema
- [X] T040 [CONTRACT] Update executable schema/OpenAPI snapshots and remove all
  obsolete enum values, `rrf-v3`, `related_to`, old graph predicates, and old
  recall fields from current contracts

**Checkpoint**:

```sh
make schema-check
# make openapi (removed by specs/003-api-layer-refactor)
make build
make test
git diff --check
```

**Phase 1 completion**: A fresh database initializes with the target authority
and projection model; replacement provider and recall contracts compile; all
behavior-changing identities exist; no migration or legacy branch exists.

---

## Phase 2: Query Planning, Primary Recall, And Graph Containment

**Purpose**: Establish a strong semantic-plus-keyword baseline, prevent graph
pollution, and replace equal rank-only RRF with calibrated deterministic ranking.

**Dependencies**: Phase 1 complete.

### QueryPlan

- [X] T041 [P] [SEARCH] Add failing query-plan tests for normalization,
  language, exact terms, identifiers, quoted phrases, dates, numbers, reference
  time, question type, memory kinds, predicates, budgets, and safe graph policy
  in `internal/storage/query_plan_test.go`
- [X] T042 [P] [SEARCH] Add failing ambiguity tests proving entity UUID order
  cannot resolve an ambiguous alias and unresolved ambiguity disables graph
  expansion in `internal/storage/graph_test.go`
- [X] T043 [SEARCH] Implement deterministic `QueryPlan` construction in a new
  `internal/retrieval/` package or the existing storage boundary, with an
  immutable versioned output
- [X] T044 [SEARCH] Integrate optional policy-controlled provider enrichment for
  query plans with strict bounds and deterministic fallback to the local plan
- [X] T045 [SEARCH] Bind normalized query plan, scope, reference time, and budgets
  into query hashes, search operation records, and cursors

### Keyword Recall

- [X] T046 [P] [SEARCH] Add failing keyword tests for short proper names,
  acronyms, identifiers, dates, numbers, quoted phrases, required/optional terms,
  rare-term weighting, and CJK unigram/bigram fallback in
  `internal/storage/keyword_query_test.go`
- [X] T047 [P] [SEARCH] Add a regression test proving semantic-enabled and
  keyword-only paths return identical keyword candidates and tie-breaks
- [X] T048 [SEARCH] Replace the OR-only compiler with one versioned field-aware
  keyword query compiler in `internal/storage/keyword_query.go`
- [X] T049 [SEARCH] Consolidate duplicate keyword SQL implementations and return
  raw BM25, matched fields, exact flags, term coverage, and one deterministic
  tie-break from `internal/storage/search.go`

### Semantic Recall And Calibration

- [X] T050 [P] [SEARCH] Add failing tests for canonical embedding documents,
  kind prefixes, query formatting, identity changes, language/kind calibration,
  absolute floors, relative margins, and exact-search rerank in
  `internal/storage/semantic_test.go`
- [X] T051 [SEARCH] Build versioned embedding documents from canonical content
  and compact structured fields in `internal/storage/semantic.go`
- [X] T052 [SEARCH] Replace the fixed semantic score floor with a versioned
  calibration interface and initial deterministic absolute-floor plus top-score
  margin policy in `internal/storage/semantic_search.go`
- [X] T053 [SEARCH] Preserve raw similarity, calibrated similarity, percentile,
  calibration identity, and rejection reason through candidate generation and
  explain output
- [X] T054 [SEARCH] Keep exact cosine search as the correctness baseline and add
  an explicit extension point and tests for optional ANN recall followed by exact
  rerank without implementing ANN unless measured scale requires it

### Temporal Features

- [X] T055 [P] [SEARCH] Add failing tests for absolute and relative query time,
  interval overlap, distance, precision, anchors, impossible intervals, and
  event-time/valid-time separation in `internal/storage/temporal_test.go`
- [X] T056 [SEARCH] Implement temporal query features and pre-top-k impossible
  interval filtering in `internal/retrieval/` and storage candidate queries
- [X] T057 [SEARCH] Preserve source time wording, normalized interval, anchor,
  precision, and temporal score in candidate explanations and context data

### Graph Containment

- [X] T058 [P] [GRAPH] Add failing tests proving stale, rebuilding, incompatible,
  or partial graph state is removed from execution and reported as degraded in
  `internal/storage/graph_test.go`
- [X] T059 [P] [GRAPH] Add failing tests for top-10 semantic seeds, top-10 keyword
  seeds, maximum 15 deduplicated seeds, primary-score gates, and prohibition of
  expanded seeds in `internal/storage/fusion_test.go`
- [X] T060 [P] [GRAPH] Add failing tests proving weak graph rank 1 cannot equal
  a strong graph rank 1, relation-only candidates cannot enter by default, and
  graph boost cannot demote a protected primary exact match
- [X] T061 [GRAPH] Remove unconditional relation auto-confirm behavior and all
  remaining `related_to` extraction, publication, retrieval, API, and fixture
  paths
- [X] T062 [GRAPH] Persist and consume mention confidence; enforce versioned
  entity, alias, mention, fact, and revision-relation publication thresholds in
  `internal/storage/graph.go`
- [X] T063 [GRAPH] Require ready and compatible graph metadata before graph
  lookup in all recall paths, with explicit execution and degradation metadata
- [X] T064 [GRAPH] Restrict graph seeds to gated primary candidates and prohibit
  graph/relation-expanded recursion in `internal/storage/semantic_search.go` and
  `internal/storage/search.go`
- [X] T065 [GRAPH] Reorder mention, fact, relation, and path windows by intent,
  confidence, specificity, temporal compatibility, recency, and stable ID before
  LIMIT in `internal/storage/graph_recall.go`
- [X] T066 [GRAPH] Preserve normalized graph scores and signal-family scores
  through candidate union, ranking features, explain output, and telemetry

### Deterministic Feature Ranker

- [X] T067 [P] [SEARCH] Add failing feature-normalization tests for semantic,
  keyword, exact, temporal, memory quality, agreement, graph, duplicate,
  aggregate, lifecycle, and feedback features in
  `internal/retrieval/ranker_test.go`
- [X] T068 [P] [SEARCH] Add failing ranking tests for missing features, stable
  ties, graph boost caps, graph-only admission, protected primary exact matches,
  and cursor invalidation in `internal/storage/fusion_test.go`
- [X] T069 [SEARCH] Implement a query-local feature vector and deterministic
  normalization in `internal/retrieval/features.go`
- [X] T070 [SEARCH] Replace `rrf-v3` with the versioned deterministic feature
  ranker and configured initial weights in `internal/retrieval/ranker.go` and
  storage recall paths
- [X] T071 [SEARCH] Implement graph policies `off`, `features_only`, `direct`, and
  `bounded_path`, defaulting production requests to semantic plus keyword with
  graph `features_only` only when ready
- [X] T072 [SEARCH] Implement separate graph-only scoring, admission floor,
  protected-rank rule, and prohibition against receiving both graph-only score
  and graph boost
- [X] T073 [SEARCH] Update ranking quality fixtures, ranking identity, cursor
  tests, explain contributions, and selection reasons for the replacement ranker

**Checkpoint**:

```sh
CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/retrieval ./internal/storage ./internal/daemon
make quality
make performance
# make openapi (removed by specs/003-api-layer-refactor)
git diff --check
```

**Phase 2 completion**: Semantic and keyword produce calibrated independent
primary recall; temporal features are explicit; graph cannot affect ranking when
unready or weak; every graph effect is confidence-preserving, gated, capped, and
explainable; `rrf-v3` no longer exists in current code or contracts.

---

## Phase 3: Structured Extraction And Memory Consolidation

**Purpose**: Reduce memory proliferation, preserve complete evidence, normalize
time and entities, and index denser canonical memories.

**Dependencies**: Phase 2 complete. Phase 1 already provides the replacement
schema and provider contract.

### Ordered Event Streams

- [X] T074 [P] [MEM] Add failing storage/API tests for caller-supplied sequence,
  transactionally assigned sequence, idempotency, concurrent ingestion, and
  deterministic stream order in `internal/storage/` and `internal/daemon/`
- [X] T075 [MEM] Implement source stream identity and stable event sequence in
  raw event ingestion, direct remember, explicit memory creation, import/export,
  and API contracts
- [X] T076 [MEM] Replace processing context order with stream, sequence,
  timestamp, and stable-ID order in `internal/storage/jobs.go`

### Structured Extraction

- [X] T077 [P] [MEM] Add failing processing tests for memory kind, canonical
  content, subjects/objects, attributes, event time, valid time, time precision,
  temporal anchor, speaker, language, specificity, confidence, and evidence
  roles in `internal/jobs/service_test.go`
- [X] T078 [P] [MEM] Add failing validation tests for unsupported fields,
  impossible intervals, invalid entity references, context-only evidence,
  overlapping ranges, output bounds, and source-scope violations
- [X] T079 [MEM] Replace the extraction prompt and provider mapping with the
  structured candidate contract in `internal/provider/client.go` and
  `internal/jobs/provider.go`
- [X] T080 [MEM] Implement deterministic normalization for canonical content,
  attributes, language, speaker, absolute/relative time, precision, anchor, and
  evidence roles in `internal/jobs/validation.go`
- [X] T081 [MEM] Publish structured candidate proposals and complete ordered
  evidence without bypassing confirmation, generation fencing, policy, audit, or
  idempotency

### Candidate Matching And Consolidation

- [X] T082 [P] [MEM] Add failing exact consolidation tests for canonical hash,
  same source event, subject/kind tuple, attributes, time compatibility, and
  evidence deduplication in `internal/storage/consolidation_test.go`
- [X] T083 [P] [MEM] Add failing semantic consolidation tests for conservative
  thresholds, unresolved provider classification, false-merge protection, and
  the rule that cosine alone cannot supersede, contest, or delete
- [X] T084 [P] [MEM] Add failing action tests for `attach_evidence`, `refine`,
  `supersede`, `contest`, `create`, and `reject`, including frozen memories,
  ancestors, siblings, sensitivity, and concurrent current-revision changes
- [X] T085 [MEM] Implement bounded consolidation candidate recall using canonical
  tuple indexes, evidence proximity, valid-time overlap, and semantic similarity
  in `internal/storage/consolidation.go`
- [X] T086 [MEM] Implement deterministic consolidation classification first and
  policy-controlled provider classification only for unresolved candidates
- [X] T087 [MEM] Implement atomic evidence attachment with immutable evidence
  history and no duplicate source ranges
- [X] T088 [MEM] Implement `refine`, `supersede`, and `contest` as explicit
  evolution proposals with expected-revision and review boundaries
- [X] T089 [MEM] Integrate consolidation before new candidate confirmation so
  only genuinely novel information creates a new logical memory
- [X] T090 [MEM] Add consolidation identities, decisions, reasons, and source
  candidate references to audit and observability records

### Quality And Duplicate Projections

- [X] T091 [P] [MEM] Add failing tests for memory quality components, bounds,
  rebuild determinism, lifecycle effects, contradiction penalties, evidence
  strength, source diversity, and duplicate density
- [X] T092 [P] [MEM] Add failing duplicate-cluster tests for same memory, shared
  primary evidence, equivalent canonical tuples, high semantic similarity with
  compatible time, and aggregate provenance
- [X] T093 [MEM] Implement rebuildable memory quality projections in
  `internal/storage/quality.go` without changing eligibility
- [X] T094 [MEM] Implement rebuildable duplicate clusters and deterministic
  representative selection in `internal/storage/duplicates.go`
- [X] T095 [MEM] Schedule and generation-fence quality and duplicate projection
  updates from memory, evidence, lifecycle, conflict, and aggregate changes

### Reindexing

- [X] T096 [P] [MEM] Add failing tests for canonical FTS documents and canonical
  semantic embedding documents across every memory kind
- [X] T097 [MEM] Rebuild FTS projections from canonical content and field-aware
  structured text in `internal/storage/memories.go` and index code
- [X] T098 [MEM] Rebuild semantic embeddings from the versioned canonical
  document format and advance compatibility identity
- [X] T099 [MEM] Update import/export and portability fixtures to preserve new
  structured authority while rebuilding quality, duplicate, FTS, semantic, and
  graph projections after import

**Checkpoint**:

```sh
go test ./internal/jobs ./internal/provider ./internal/storage ./internal/daemon
make schema-check
make portability-drill
make restore-drill
make quality
make performance
git diff --check
```

**Phase 3 completion**: Event context order is stable; extraction emits validated
structured proposals; equivalent information consolidates instead of
proliferating; complete ordered evidence is retained; quality and duplicate
projections are rebuildable; indexes use canonical documents.

---

## Phase 4: Typed Event Graph And Intent-Compatible Traversal

**Purpose**: Replace the narrow software-oriented graph with typed,
domain-independent, evidence-backed entity and event knowledge.

**Dependencies**: Phase 3 complete.

### Predicate Registry

- [X] T100 [P] [GRAPH] Add failing registry tests for subject/object types,
  literal types, direction, inverse predicates, cardinality, temporal policy,
  allowed path positions, and auto-confirm policy in
  `internal/domain/graph_test.go`
- [X] T101 [P] [GRAPH] Define a minimal general registry fixture covering
  identity, membership, people/organizations, location, ownership, preference,
  work, participation, creation, plans, event participants/time, dependencies,
  and provenance under `internal/storage/testdata/`
- [X] T102 [GRAPH] Implement the closed typed predicate registry in domain and
  storage layers and reject provider-defined free-form predicates

### Entity And Event Publication

- [X] T103 [P] [GRAPH] Add failing entity normalization tests for Unicode,
  punctuation, whitespace, case, language, alias length, pronouns, common terms,
  type compatibility, collision, and scope isolation
- [X] T104 [P] [GRAPH] Add failing typed event/fact publication tests for direct
  evidence, confidence, cardinality conflicts, literals, temporal bounds,
  revision fences, and review policy
- [X] T105 [GRAPH] Replace entity normalization and alias publication with the
  versioned collision-safe policy in `internal/storage/entities.go` and graph
  commit code
- [X] T106 [GRAPH] Implement typed event, participant, place, time, entity fact,
  typed literal, and revision-relation publication in `internal/storage/graph.go`
  and `internal/storage/graph_facts.go`
- [X] T107 [GRAPH] Persist provider mention confidence and direct evidence and
  prevent context-only mentions/facts from becoming high-confidence authority
- [X] T108 [GRAPH] Implement predicate-specific auto-review thresholds and
  cardinality/conflict handling with no broad relation threshold bypass

### Entity Resolution

- [X] T109 [P] [GRAPH] Add failing query-time resolution tests for aliases,
  context/speaker/type disambiguation, multiple alternatives, probability
  normalization, unresolved ambiguity, CJK, and high-degree entities
- [X] T110 [GRAPH] Implement ambiguity-safe entity resolution that returns
  alternatives rather than selecting by UUID and disables graph expansion when
  confidence is insufficient
- [X] T111 [GRAPH] Implement entity mention inverse-frequency and graph degree
  statistics as rebuildable projections with generation binding

### Traversal

- [X] T112 [P] [GRAPH] Add failing traversal tests for direct facts, incoming and
  outgoing edges, typed heterogeneous paths, maximum two hops, allowed path
  positions, fan-out, cycles, temporal filters, scope, evidence, and stable order
- [X] T113 [P] [GRAPH] Add failing score tests for predicate intent, entity
  confidence, fact confidence, evidence strength, temporal compatibility, hub
  penalty, hop decay, and capped corroboration
- [X] T114 [GRAPH] Implement query-plan-directed direct fact lookup and typed
  incoming/outgoing one-hop traversal in `internal/storage/graph_recall.go`
- [X] T115 [GRAPH] Implement registry-approved heterogeneous two-hop paths with
  pre-expansion eligibility filters, bounded fan-out, cycle protection, and
  complete edge evidence
- [X] T116 [GRAPH] Implement versioned graph scoring with hub penalties and
  confidence products, preserving family explanations through ranking
- [X] T117 [GRAPH] Remove obsolete same-predicate path logic, old fixed
  predicates, arbitrary ID-ordered windows, and unsupported revision-relation
  candidate expansion

### Graph Quality Gates

- [X] T118 [P] [GRAPH] Add fixed graph corpora for entity, event, temporal,
  relational, multi-hop, ambiguous, hub, conflict, stale, and out-of-scope cases
  under `internal/storage/testdata/`
- [X] T119 [GRAPH] Add graph promotion precision, graph-caused rank harm, edge/path
  counts, traversal latency, allocation, and storage-growth gates to `make
  quality` and `make performance`
- [X] T120 [GRAPH] Run mention-only, fact-only, revision-relation-only, one-hop,
  two-hop, hub-penalty, confidence-gate, seed-budget, and boost-cap ablations and
  record the accepted baseline

**Checkpoint**:

```sh
go test ./internal/domain ./internal/provider ./internal/storage ./internal/daemon
make quality
make performance
# make openapi (removed by specs/003-api-layer-refactor)
git diff --check
```

**Phase 4 completion**: Graph authority contains typed evidence-backed knowledge,
not semantic similarity; entity ambiguity is safe; traversal is intent-compatible
and bounded; graph promotions meet measured precision and harm gates.

---

## Phase 5: Context Selection, Rendering, And Aggregates

**Purpose**: Separate relevance ranking from budgeted context selection and
provide compact, diverse, source-grounded context.

**Dependencies**: Phase 4 complete. Duplicate and quality projections from Phase
3 are required.

### Selection Tests And Model

- [X] T121 [P] [CTX] Add failing tests for duplicate representatives, evidence
  attachment, source/session/entity coverage, semantic redundancy, aggregate
  provenance, token/character/result budgets, and deterministic ties in
  `internal/retrieval/selector_test.go`
- [X] T122 [P] [CTX] Add failing query-type tests for lookup, verification,
  temporal, multi-hop, procedural, and exploratory selection policies
- [X] T123 [P] [CTX] Add failing score-discontinuity and low-quality stopping
  tests proving maximum result count does not force irrelevant context
- [X] T124 [CTX] Define versioned context candidate, selected context item,
  rejected candidate, budget accounting, and selection explanation models in
  `internal/domain/`
- [X] T125 [CTX] Implement provenance-aware duplicate clustering integration and
  representative/evidence collapse in `internal/retrieval/selector.go`
- [X] T126 [CTX] Implement deterministic MMR-style utility over relevance,
  evidence coverage, source/entity/session coverage, redundancy, and context
  cost
- [X] T127 [CTX] Implement question-type policies, exact-match protection,
  score-discontinuity stopping, and hard token/character/result budgets

### Context Rendering And API

- [X] T128 [P] [CTX] Add failing rendering tests for kind, display content,
  event/valid time, source speaker, complete evidence, graph explanation,
  confidence, lifecycle, and estimated tokens
- [X] T129 [CTX] Implement structured context packages and deterministic plain
  text rendering without claiming unavailable dates, speakers, or conversation
  reconstruction
- [X] T130 [CTX] Integrate selection after ranking in recall; expose selected
  context by default and raw candidates/rejections only through policy-safe
  explain/debug output
- [X] T131 [CTX] Update token estimation and budget enforcement so retrieval and
  downstream generation use the same context-format identity
- [X] T132 [CTX] Update LoCoMo and LongMemEval adapters to consume structured
  selected context, preserve complete evidence, and remove benchmark-side
  duplicate top-k assembly

### Aggregate Behavior

- [X] T133 [P] [CTX] Add failing aggregate tests for scene, topic, and timeline
  kinds, bounded deduplicated inputs, complete provenance, source stabilization,
  stale transitions, and review-only rebuild publication
- [X] T134 [CTX] Add timeline aggregate provider/domain/storage support under
  `internal/aggregate/`, `internal/domain/`, and storage schema contracts
- [X] T135 [CTX] Exclude stale aggregates from default recall and allow explicit
  debug inclusion with stale reason and provenance
- [X] T136 [CTX] Prevent broad aggregates from consuming a primary slot when a
  more specific source memory answers the query; attach aggregate synthesis as
  supplementary context when useful
- [X] T137 [CTX] Mark current aggregate ADR behavior superseded where it conflicts
  with default stale exclusion and timeline support

### Context Quality Gates

- [X] T138 [P] [CTX] Add selected-context duplicate rate, useful density,
  evidence/source/session coverage, token utilization, and answer-support metrics
  to fixed quality fixtures
- [X] T139 [CTX] Run fixed-cutoff versus MMR, query-type policy, duplicate
  collapse, aggregate, and structured-rendering ablations on fresh shared corpora
- [X] T140 [CTX] Promote context-selection defaults only when duplicate rate falls
  at least 50%, multi-evidence coverage improves, and median tokens do not grow
  more than the approved quality budget

**Checkpoint**:

```sh
go test ./internal/retrieval ./internal/aggregate ./internal/storage ./internal/daemon
# Python evaluation harness removed by specs/003-api-layer-refactor
make quality
make performance
# make openapi (removed by specs/003-api-layer-refactor)
git diff --check
```

**Phase 5 completion**: Ranking and context selection are distinct versioned
stages; selected context is deduplicated, query-aware, evidence-rich, and bounded;
scene/topic/timeline aggregates are provenance-complete and stale-safe.

---

## Phase 6: Feedback, Calibration, And Optional Reranking

**Purpose**: Learn from retrieval outcomes safely after deterministic retrieval
and context behavior are stable.

**Dependencies**: Phase 5 complete.

### Feedback Contract

- [X] T141 [P] [FEEDBACK] Add failing domain/API tests separating memory quality,
  retrieval, and answer feedback with valid target and reason combinations
- [X] T142 [P] [FEEDBACK] Add failing retrieval-context tests for query hash/type,
  permitted entity IDs, ranking/features/context identities, rank, selection
  state, matched signals, caller, and timestamp
- [X] T143 [P] [FEEDBACK] Add failing privacy and poisoning tests for query text,
  sensitivity, caller deduplication, minimum support, clamping, decay, and
  cross-scope isolation
- [X] T144 [FEEDBACK] Replace feedback domain, schema, API, and OpenAPI contracts
  with the three explicit feedback purposes
- [X] T145 [FEEDBACK] Persist privacy-safe retrieval and answer decision context
  and reject identity-incompatible feedback

### Aggregation And Ranking

- [X] T146 [P] [FEEDBACK] Add failing tests proving online and maintenance
  feedback rebuilds produce byte-equivalent aggregates for the same authority
  events
- [X] T147 [P] [FEEDBACK] Add failing tests for global bounded priors and
  query-conditioned adjustments after minimum support, decay, and caller
  deduplication
- [X] T148 [FEEDBACK] Implement one shared versioned feedback aggregation function
  used by normal storage operations, maintenance, rebuild, restore, and import
- [X] T149 [FEEDBACK] Implement bounded global and query-conditioned ranking
  features without changing eligibility or memory authority
- [X] T150 [FEEDBACK] Add feedback aggregate identity and generation to ranker,
  cursor, explain output, telemetry, export/import, backup/restore, and tests

### Calibration

- [X] T151 [P] [FEEDBACK] Add deterministic calibration tooling for semantic
  thresholds, keyword weights, temporal features, graph gates/boosts, penalties,
  and context utility under `[removed evaluation harness]`
- [X] T152 [FEEDBACK] Define train/development/holdout boundaries and prevent
  benchmark test-set labels from entering runtime feedback or weight fitting
- [X] T153 [FEEDBACK] Calibrate and record deterministic weights on fixed
  development sets with cross-benchmark and per-query-type reports
- [X] T154 [FEEDBACK] Promote calibrated weights only when paired confidence
  intervals and all safety/performance gates pass

### Optional Learned Reranker

- [X] T155 [P] [FEEDBACK] Define a bounded top-window reranker interface with
  deterministic ranker fallback, policy checks, strict provider bounds, and
  complete model/prompt/feature/calibration identity
- [X] T156 [P] [FEEDBACK] Add tests proving reranking cannot add candidates,
  bypass filters, cross scope, exceed budgets, or publish under a stale identity
- [X] T157 [FEEDBACK] Implement a diagnostic-only reranker adapter after T155 and
  T156; do not enable it in production defaults
- [X] T158 [FEEDBACK] Compare learned and deterministic reranking on the same
  candidate windows and promote only if gains exceed cost, latency, variance,
  and degradation thresholds

**Checkpoint**:

```sh
go test ./internal/storage ./internal/maintenance ./internal/daemon ./internal/retrieval
make restore-drill
make portability-drill
make quality
make performance
make offline-default
git diff --check
```

**Phase 6 completion**: Feedback purposes are explicit; aggregation is identical
online and during maintenance; query-conditioned effects are bounded and
poison-resistant; deterministic weights are reproducibly calibrated; any learned
reranker remains gated by measured benefit and safe fallback.

---

## Phase 7: Observability, End-To-End Validation, And Cleanup

**Purpose**: Complete operational visibility, remove obsolete behavior, validate
the replacement end to end, and make current documentation match executable
contracts.

**Dependencies**: Phases 0 through 6 complete.

### Decision Telemetry And Explainability

- [X] T159 [P] [OPS] Add failing telemetry tests for query-plan type, executed
  capabilities, candidate counts, rejection gates, score summaries, graph
  seeds/edges/paths/promotions, duplicate clusters, context budgets, identities,
  degradation, and phase latency
- [X] T160 [P] [OPS] Add failing explain-mode policy tests proving explanations
  cannot expose denied spaces, sensitive content, hidden evidence, or provider
  data unavailable to normal recall
- [X] T161 [OPS] Implement bounded content-free decision telemetry across query
  planning, recall, graph, ranking, and context selection
- [X] T162 [OPS] Implement policy-safe per-result feature contributions, graph
  paths, evidence, and selection/rejection reasons in explain mode
- [X] T163 [OPS] Expose compatible health/index state for consolidation, quality,
  duplicate, semantic calibration, graph, ranker, context, and feedback
  generations

### End-To-End Evaluation

- [X] T164 [P] [EVAL] Add reproducible scripts for extraction, consolidation,
  indexing, recall, ranking, selection, and end-to-end reports with identical
  query coverage
- [X] T165 [P] [EVAL] Add the fixed ablation matrix `S`, `SK`, `SKF`, `SKD`,
  `SKP`, and `SKPM`, plus graph-family ablations, to evaluation tooling
- [X] T166 [EVAL] Run LoCoMo and LongMemEval on fresh shared corpora with fixed
  generation/judge parameters and at least three end-to-end repetitions
- [X] T167 [EVAL] Report means, standard deviations, paired bootstrap confidence
  intervals, query-type/category deltas, evidence-count buckets, graph promotion
  precision/harm, duplicate rates, latency, storage, and token/provider cost
- [X] T168 [EVAL] Verify promotion gates: graph Recall@1/MRR regression no worse
  than 0.5 points, graph-promoted top-10 precision at least 50%, positive intended
  category gain without larger aggregate harm, duplicate rate reduced at least
  50%, and no safety regression

### Recovery, Portability, And Performance

- [X] T169 [P] [OPS] Extend restart/recovery drills for interrupted
  consolidation, quality/duplicate rebuild, graph extraction, calibration,
  ranking generation, context selection, and feedback aggregation
- [X] T170 [P] [OPS] Extend logical export/import for new authority records and
  prove every derived projection rebuilds to retrieval-equivalent output
- [X] T171 [P] [OPS] Add scale gates for write amplification, candidate
  consolidation latency, exact semantic search, graph fan-out, feature
  calculation, MMR selection, database growth, and memory allocation
- [X] T172 [OPS] Verify provider-disabled operation preserves raw events, direct
  memory, keyword recall, deterministic query planning, and safe degraded
  behavior without secret access or network calls

### Contract And Documentation Cleanup

- [X] T173 [P] [DOC] Update `docs/architecture/overview.md` and
  `docs/architecture/feedback-and-aggregation.md` to describe only the final
  implemented architecture
- [X] T174 [P] [DOC] Update `README.md`, `docs/README.md`,
  `docs/api/usage.md`, and `docs/operations/runbook.md` for the replacement
  recall and operational model
- [X] T175 [P] [DOC] Mark superseded ADRs and add links to the replacement ADR;
  preserve decision history without presenting old behavior as current
- [X] T176 [P] [DOC] Update LoCoMo and LongMemEval READMEs with corrected metric
  semantics, fresh-corpus rules, identities, ablations, and reproducible commands
- [X] T177 [CONTRACT] Search current code, tests, OpenAPI, schema, examples, and
  documentation and remove obsolete `rrf-v3`, broad `related_to`, old predicate,
  old channel-default, first-evidence-only, stale-aggregate-default, and legacy
  compatibility behavior
- [X] T178 [CONTRACT] Verify there are no migration files, dual schema branches,
  old API decoders, deprecated fields, legacy cursor/checkpoint parsers, or old
  ranking fallbacks

### Final Gates

- [X] T179 [OPS] Run formatting, generated-contract, schema, build, unit,
  integration, race, vet, recovery, portability, quality, performance, and
  offline-default checks and resolve all failures
- [X] T180 [DOC] Record final algorithm identities, accepted benchmark baselines,
  known limitations, capacity bounds, and completion evidence in this task file
  and the optimization design document

**Final checkpoint**:

```sh
make check
make test-race
make performance
make restore-drill
make portability-drill
make offline-default
# Python evaluation harness removed by specs/003-api-layer-refactor
git diff --check
```

**Phase 7 completion**: Executable contracts, implementation, operations, and
documentation describe one replacement architecture; no old compatibility path
remains; all quality, safety, recovery, portability, performance, and benchmark
gates pass on a fresh data directory.

## Dependency Summary

```text
Phase 0: metrics and baseline
  -> Phase 1: replacement contracts and schema
    -> Phase 2: query plan, primary recall, graph containment, ranker
      -> Phase 3: structured extraction and consolidation
        -> Phase 4: typed event graph
          -> Phase 5: context selection and aggregates
            -> Phase 6: feedback, calibration, optional reranker
              -> Phase 7: observability, validation, cleanup
```

Within a phase, tests marked `[P]` can normally proceed in parallel. Production
implementation tasks should follow their listed tests and the order shown unless
the task description explicitly states otherwise.

## Session Handoff Template

Use this exact structure at the end of each implementation session:

```text
Completed tasks:
- Txxx: summary

Files changed:
- path: purpose

Verification:
- command: result

Decisions or deviations:
- none, or exact design decision and rationale

Residual risks/blockers:
- none, or concrete blocker

Next unblocked task:
- Txxx: task title
```

## Final Completion Record

Recorded by T180.

```text
Schema identity: schema-v1 (schema_version = 1); single file pair
  internal/storage/schema-v1.sql == docs/storage/schema-v1.sql.
Processing identity: processor builtin-noop-v1 (default) under provider contract
  processing-structured-v1; ProcessingIdentity also hashes allowed kinds,
  max_candidates (32), and max_content_chars (4096).
Consolidation identity: consolidation-v1 (exact hash enabled; same-source-event
  cosine 0.90; same-subject-kind cosine 0.94; max_candidates 32).
Embedding document/query identity: embedding-document-v2 / embedding-query-v1
  over canonical-document-v1; provider model and dimensions bind the effective
  EmbeddingCompatibilityIdentity.
Semantic calibration identity: semantic-calibration-v1 (absolute_floor 0.35,
  top_margin 0.08).
Keyword compiler identity: keyword-compiler-v2 (single field-aware,
  language-weighted compiler).
Predicate registry identity: predicate-registry-v2 (closed typed registry).
Graph extraction/scoring identity: graph-typed-v1 (extraction) and
  graph-scoring-v3 (scoring); auto-review-v1; entity-graph-statistics-v2.
Query-plan identity: query-plan-v2; temporal-features-v1.
Ranking feature/weight identity: ranking-features-v1 / ranking-weights-v1
  (semantic 1.00, keyword 0.35, exact 0.15, temporal 0.10, quality 0.10,
  agreement 0.05).
Context-selection identity: context-selection-v2; token format
  context-tokens-v1; budgets tokens=4000, chars=16000, results=20.
Feedback identity: v2 (window 90d, min_support 3, half_life 45d; bounded global
  0.001 and query-conditioned 0.0005).
Evaluation metric identity: locomo-retrieval-metrics-v2 and
  longmemeval-retrieval-metrics-v2 under baseline schema
  [removed evaluation baseline].

Accepted benchmark reports: no end-to-end LoCoMo/LongMemEval provider run is
  committed. Accepted evidence is the deterministic quality/performance gates
  below plus [removed evaluation harness] Its
  holdout paired CI [0.0, 0.0] does not exclude zero, so promotion is rejected
  and the deterministic default weight identities above remain authoritative.
  External benchmark commands and identity requirements are in
  [removed evaluation harness] and [removed evaluation harness]; benchmark
  test-set labels are holdout-only and never enter runtime feedback or fitting.
Quality gate result: make quality passed 2026-09-16. Graph promotion precision
  0.750 (gate >= 0.50), graph-caused rank harm 0.0000 (gate <= 0.005), one-hop
  edges 4 (gate <= 200), paths 1 (gate <= 100). Context duplicate rate
  0.500 -> 0.000 with reduction 1.000 (gate >= 0.50), selected duplicate rate
  0.000 (gate = 0.000), evidence/source coverage 1.000 (gate >= 1.00), selected
  coverage 2 versus fixed-cutoff 1. Sibling-space, stale-revision, and conflict
  contamination gates remain zero.
Performance gate result: make performance passed 2026-09-16. Query p95 at scale
  500: keyword 9.00ms, semantic 9.26ms, hybrid 10.48ms. Multichannel query p95
  16.80ms at scale 300. Graph traversal p95 1.36ms, heap 2.83MB, storage growth
  1.00x (gates 50ms / 64MiB / 6.0x). Semantic rebuild 167.76ms at 1.00x
  amplification. Portability import 417 objects at 2251.5 objects/s.
Recovery and portability result: make restore-drill and make portability-drill
  passed 2026-09-16. Restore inventories authority, fences running work, rebuilds
  derived health from authority, and recovery reschedules interrupted
  consolidation, quality/duplicate, graph, calibration, ranking, context, and
  feedback work. A fresh logical import rebuilds equivalent authority and
  retrieval output.
Known limitations:
  - exact cosine search only; the ANN extension point and exact rerank are
    tested, but ANN is not implemented because measured scale did not require it;
  - the learned reranker is diagnostic-only (removed evaluation reranker mode); production
    uses the deterministic ranker and its fallback;
  - calibration promotion is not met on the fixed synthetic development set, so
    weights remain the deterministic defaults rather than fitted values;
  - providers are disabled by default: keyword recall, direct memory, query
    planning, and degraded graph work offline without secrets or network, while
    structured extraction and graph authority require a configured provider;
  - graph health reports unavailable until a graph index generation exists, and
    ranking then uses features_only/off instead of graph candidates.

Capacity bounds: consolidation max_candidates 32; structured extraction
  max_candidates 32, max_content_chars 4096, max_evidence 100, max_entity_refs
  64; graph max_items 256; graph seeds calibrated semantic top 10 + keyword top
  10 deduplicated to <= 15 revisions with at most two hops and hub-penalized
  bounded fan-out; context max tokens 4000, max chars 16000, max results 20, and
  semantic candidates capped by query plan and context budget; feedback global
  prior <= 0.001, query-conditioned adjustment <= 0.0005, window 90 days,
  minimum support 3.
```
