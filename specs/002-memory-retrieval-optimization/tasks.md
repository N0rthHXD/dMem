---

description: "Phased implementation tasks for memory and retrieval optimization"
---

# Tasks: Memory And Retrieval Optimization

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
  `evaluation/locomo/tests/test_metrics.py`
- [X] T002 [P] [EVAL] Add failing LoCoMo nDCG tests proving missing gold evidence
  lowers the score and ideal DCG uses `min(k, len(gold))` in
  `evaluation/locomo/tests/test_metrics.py`
- [X] T003 [P] [EVAL] Add failing MRR tests for one explicitly named full-list
  metric or cutoff-specific `MRR@k`, and reject the current repeated ambiguous
  values in `evaluation/locomo/tests/test_metrics.py`
- [X] T004 [P] [EVAL] Add failing evidence-mapping tests for multiple evidence
  events, stable chronological ordering, unmapped results, duplicate source
  turns, and session promotion in `evaluation/locomo/tests/test_runner.py`
- [X] T005 [P] [EVAL] Add failing dataset-validation tests that enforce expected
  corpus counts and reject malformed or partially unresolved evidence in
  `evaluation/locomo/tests/test_dataset.py`

### Metric Implementation

- [X] T006 [EVAL] Correct recall-all, nDCG, and MRR semantics in
  `evaluation/locomo/dmem_locomo/metrics.py` and assign a new retrieval metric
  schema identity
- [X] T007 [EVAL] Replace first-evidence-only mapping with complete ordered
  evidence sets in `evaluation/locomo/dmem_locomo/runner.py` and checkpoint
  records
- [X] T008 [EVAL] Report memory-level, unique-evidence-level, and session-level
  metrics separately, including duplicate and unmapped rates, in
  `evaluation/locomo/dmem_locomo/runner.py` and
  `evaluation/locomo/dmem_locomo/cli.py`
- [X] T009 [EVAL] Add extraction coverage metrics that distinguish “gold source
  absent from every indexed memory” from retrieval failure in
  `evaluation/locomo/dmem_locomo/runner.py`
- [X] T010 [EVAL] Enforce dataset SHA, subset, metric schema, run configuration,
  and model/prompt compatibility for retrieval, generation, judge, and summarize
  checkpoints under `evaluation/locomo/dmem_locomo/`
- [X] T011 [EVAL] Make a non-resume run reject non-empty result checkpoints and
  make summarize recompute retrieval metrics from sufficient raw records instead
  of trusting stored metric dictionaries

### Runtime Identity And Readiness

- [X] T012 [P] [EVAL] Add failing tests for graph-ready barriers, per-query
  degraded responses, and fail-on-degraded behavior in
  `evaluation/locomo/tests/test_runner.py`
- [X] T013 [P] [EVAL] Add failing manifest tests for daemon build/commit, schema,
  processing, embedding, graph, keyword, ranking, context, and metric identities
  in `evaluation/locomo/tests/test_checkpoint.py`
- [X] T014 [EVAL] Implement an explicit graph completion/readiness barrier for
  graph and graph-using evaluation modes in
  `evaluation/locomo/dmem_locomo/runner.py` and
  `evaluation/locomo/dmem_locomo/api.py`
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
  `internal/storage/testdata/` and `evaluation/`
- [X] T018 [EVAL] Run and record fresh keyword, semantic, graph, and current
  hybrid baselines on one shared corpus; document exact commands and immutable
  identities in `evaluation/locomo/README.md`

**Checkpoint**:

```sh
~/miniforge3/envs/agent/bin/python -m pytest evaluation/locomo/tests
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

- [ ] T019 [P] [CONTRACT] Add a superseding ADR for structured memories,
  consolidation, query planning, feature ranking, gated graph retrieval, and
  context selection in `docs/adr/0035-memory-retrieval-v4.md`
- [ ] T020 [P] [CONTRACT] Add failing domain tests for memory kinds, structured
  revision fields, event time versus valid time, evidence roles, confidence,
  specificity, language, and source speaker in `internal/domain/`
- [ ] T021 [P] [CONTRACT] Add failing schema contract tests for ordered event
  streams, structured revisions, canonical attributes, evidence roles, duplicate
  clusters, memory quality, typed predicates/events, query telemetry, and
  context decisions in `internal/storage/schema_contract_test.go`
- [ ] T022 [P] [CONTRACT] Add failing canonical identity tests for processing,
  consolidation, projection, embedding document/query, semantic calibration,
  keyword compiler, entity normalization, predicate registry, graph extraction,
  graph scoring, query planning, ranking features/weights, context selection,
  feedback aggregation, and metric schema in `internal/config/identity_test.go`
- [ ] T023 [P] [CONTRACT] Add failing strict provider-contract tests for
  structured candidates, normalized time proposals, evidence roles, typed graph
  entities/events/facts, closed predicates, and bounded output in
  `internal/provider/client_test.go`

### Domain And Schema Replacement

- [ ] T024 [CONTRACT] Replace memory and revision domain models with the target
  kinds and structured fields in `internal/domain/models.go` and focused domain
  files
- [ ] T025 [CONTRACT] Replace schema v1 with ordered event streams, structured
  revision authority/projections, evidence roles, quality and duplicate
  projections, and required indexes in `internal/storage/schema-v1.sql` and
  `docs/storage/schema-v1.sql`
- [ ] T026 [CONTRACT] Replace graph schema with typed predicate definitions,
  typed literals/events, persisted mention confidence, evidence-backed facts and
  revision relations; remove broad `related_to` authority in both schema files
- [ ] T027 [CONTRACT] Add version identity storage and active-generation metadata
  for every membership, ranking, graph, context, and evaluation identity in both
  schema files and storage domain models
- [ ] T028 [CONTRACT] Update repository scan/write helpers to compile against the
  replacement schema in `internal/storage/` without implementing old-schema
  reads

### Provider And Configuration Contracts

- [ ] T029 [P] [CONTRACT] Replace processing provider request/response types with
  structured candidate and consolidation proposal types in
  `internal/provider/contracts.go` and `internal/domain/`
- [ ] T030 [P] [CONTRACT] Replace graph provider output with typed entities,
  aliases, mentions, events, facts, and explicit revision relations in
  `internal/provider/contracts.go`
- [ ] T031 [CONTRACT] Implement strict decoding and deterministic validation for
  the replacement processing and graph contracts in
  `internal/provider/client.go` and `internal/jobs/validation.go`
- [ ] T032 [CONTRACT] Add strict configuration sections and bounds for
  consolidation, semantic calibration, graph gates, ranking, and context budgets
  in `internal/config/`, `docs/operations/config.example.toml`, and configuration
  tests
- [ ] T033 [CONTRACT] Implement canonical non-secret identity generation for all
  new algorithm contracts in `internal/config/identity.go`

### API Contract Replacement

- [ ] T034 [P] [CONTRACT] Add failing API tests for intent, reference time,
  maximum results, context-token budget, evidence, explain mode, execution
  metadata, and structured context in `internal/daemon/search_api_test.go`
- [ ] T035 [P] [CONTRACT] Define replacement recall request/response and explain
  models in `internal/domain/models.go` and `docs/api/openapi.yaml`
- [ ] T036 [CONTRACT] Replace recall request validation and response serialization
  in `internal/api/agent.go`, `internal/api/search.go`, and daemon route tests
- [ ] T037 [CONTRACT] Remove production channel selection as the primary API
  abstraction; retain explicit channel/feature overrides only in a clearly named
  evaluation/debug contract
- [ ] T038 [CONTRACT] Replace cursor payload and validation so it accepts only the
  new schema, query-plan, candidate-membership, ranking, graph, context, and
  feedback identities

### Foundational Compilation

- [ ] T039 [CONTRACT] Update fixtures, helper constructors, direct remember,
  explicit memory creation, import/export types, backup/restore validation, and
  tests to compile and initialize only the new schema
- [ ] T040 [CONTRACT] Update executable schema/OpenAPI snapshots and remove all
  obsolete enum values, `rrf-v3`, `related_to`, old graph predicates, and old
  recall fields from current contracts

**Checkpoint**:

```sh
make schema-check
make openapi
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

- [ ] T041 [P] [SEARCH] Add failing query-plan tests for normalization,
  language, exact terms, identifiers, quoted phrases, dates, numbers, reference
  time, question type, memory kinds, predicates, budgets, and safe graph policy
  in `internal/storage/query_plan_test.go`
- [ ] T042 [P] [SEARCH] Add failing ambiguity tests proving entity UUID order
  cannot resolve an ambiguous alias and unresolved ambiguity disables graph
  expansion in `internal/storage/graph_test.go`
- [ ] T043 [SEARCH] Implement deterministic `QueryPlan` construction in a new
  `internal/retrieval/` package or the existing storage boundary, with an
  immutable versioned output
- [ ] T044 [SEARCH] Integrate optional policy-controlled provider enrichment for
  query plans with strict bounds and deterministic fallback to the local plan
- [ ] T045 [SEARCH] Bind normalized query plan, scope, reference time, and budgets
  into query hashes, search operation records, and cursors

### Keyword Recall

- [ ] T046 [P] [SEARCH] Add failing keyword tests for short proper names,
  acronyms, identifiers, dates, numbers, quoted phrases, required/optional terms,
  rare-term weighting, and CJK unigram/bigram fallback in
  `internal/storage/keyword_query_test.go`
- [ ] T047 [P] [SEARCH] Add a regression test proving semantic-enabled and
  keyword-only paths return identical keyword candidates and tie-breaks
- [ ] T048 [SEARCH] Replace the OR-only compiler with one versioned field-aware
  keyword query compiler in `internal/storage/keyword_query.go`
- [ ] T049 [SEARCH] Consolidate duplicate keyword SQL implementations and return
  raw BM25, matched fields, exact flags, term coverage, and one deterministic
  tie-break from `internal/storage/search.go`

### Semantic Recall And Calibration

- [ ] T050 [P] [SEARCH] Add failing tests for canonical embedding documents,
  kind prefixes, query formatting, identity changes, language/kind calibration,
  absolute floors, relative margins, and exact-search rerank in
  `internal/storage/semantic_test.go`
- [ ] T051 [SEARCH] Build versioned embedding documents from canonical content
  and compact structured fields in `internal/storage/semantic.go`
- [ ] T052 [SEARCH] Replace the fixed semantic score floor with a versioned
  calibration interface and initial deterministic absolute-floor plus top-score
  margin policy in `internal/storage/semantic_search.go`
- [ ] T053 [SEARCH] Preserve raw similarity, calibrated similarity, percentile,
  calibration identity, and rejection reason through candidate generation and
  explain output
- [ ] T054 [SEARCH] Keep exact cosine search as the correctness baseline and add
  an explicit extension point and tests for optional ANN recall followed by exact
  rerank without implementing ANN unless measured scale requires it

### Temporal Features

- [ ] T055 [P] [SEARCH] Add failing tests for absolute and relative query time,
  interval overlap, distance, precision, anchors, impossible intervals, and
  event-time/valid-time separation in `internal/storage/temporal_test.go`
- [ ] T056 [SEARCH] Implement temporal query features and pre-top-k impossible
  interval filtering in `internal/retrieval/` and storage candidate queries
- [ ] T057 [SEARCH] Preserve source time wording, normalized interval, anchor,
  precision, and temporal score in candidate explanations and context data

### Graph Containment

- [ ] T058 [P] [GRAPH] Add failing tests proving stale, rebuilding, incompatible,
  or partial graph state is removed from execution and reported as degraded in
  `internal/storage/graph_test.go`
- [ ] T059 [P] [GRAPH] Add failing tests for top-10 semantic seeds, top-10 keyword
  seeds, maximum 15 deduplicated seeds, primary-score gates, and prohibition of
  expanded seeds in `internal/storage/fusion_test.go`
- [ ] T060 [P] [GRAPH] Add failing tests proving weak graph rank 1 cannot equal
  a strong graph rank 1, relation-only candidates cannot enter by default, and
  graph boost cannot demote a protected primary exact match
- [ ] T061 [GRAPH] Remove unconditional relation auto-confirm behavior and all
  remaining `related_to` extraction, publication, retrieval, API, and fixture
  paths
- [ ] T062 [GRAPH] Persist and consume mention confidence; enforce versioned
  entity, alias, mention, fact, and revision-relation publication thresholds in
  `internal/storage/graph.go`
- [ ] T063 [GRAPH] Require ready and compatible graph metadata before graph
  lookup in all recall paths, with explicit execution and degradation metadata
- [ ] T064 [GRAPH] Restrict graph seeds to gated primary candidates and prohibit
  graph/relation-expanded recursion in `internal/storage/semantic_search.go` and
  `internal/storage/search.go`
- [ ] T065 [GRAPH] Reorder mention, fact, relation, and path windows by intent,
  confidence, specificity, temporal compatibility, recency, and stable ID before
  LIMIT in `internal/storage/graph_recall.go`
- [ ] T066 [GRAPH] Preserve normalized graph scores and signal-family scores
  through candidate union, ranking features, explain output, and telemetry

### Deterministic Feature Ranker

- [ ] T067 [P] [SEARCH] Add failing feature-normalization tests for semantic,
  keyword, exact, temporal, memory quality, agreement, graph, duplicate,
  aggregate, lifecycle, and feedback features in
  `internal/retrieval/ranker_test.go`
- [ ] T068 [P] [SEARCH] Add failing ranking tests for missing features, stable
  ties, graph boost caps, graph-only admission, protected primary exact matches,
  and cursor invalidation in `internal/storage/fusion_test.go`
- [ ] T069 [SEARCH] Implement a query-local feature vector and deterministic
  normalization in `internal/retrieval/features.go`
- [ ] T070 [SEARCH] Replace `rrf-v3` with the versioned deterministic feature
  ranker and configured initial weights in `internal/retrieval/ranker.go` and
  storage recall paths
- [ ] T071 [SEARCH] Implement graph policies `off`, `features_only`, `direct`, and
  `bounded_path`, defaulting production requests to semantic plus keyword with
  graph `features_only` only when ready
- [ ] T072 [SEARCH] Implement separate graph-only scoring, admission floor,
  protected-rank rule, and prohibition against receiving both graph-only score
  and graph boost
- [ ] T073 [SEARCH] Update ranking quality fixtures, ranking identity, cursor
  tests, explain contributions, and selection reasons for the replacement ranker

**Checkpoint**:

```sh
go test ./internal/retrieval ./internal/storage ./internal/daemon
make quality
make performance
make openapi
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

- [ ] T074 [P] [MEM] Add failing storage/API tests for caller-supplied sequence,
  transactionally assigned sequence, idempotency, concurrent ingestion, and
  deterministic stream order in `internal/storage/` and `internal/daemon/`
- [ ] T075 [MEM] Implement source stream identity and stable event sequence in
  raw event ingestion, direct remember, explicit memory creation, import/export,
  and API contracts
- [ ] T076 [MEM] Replace processing context order with stream, sequence,
  timestamp, and stable-ID order in `internal/storage/jobs.go`

### Structured Extraction

- [ ] T077 [P] [MEM] Add failing processing tests for memory kind, canonical
  content, subjects/objects, attributes, event time, valid time, time precision,
  temporal anchor, speaker, language, specificity, confidence, and evidence
  roles in `internal/jobs/service_test.go`
- [ ] T078 [P] [MEM] Add failing validation tests for unsupported fields,
  impossible intervals, invalid entity references, context-only evidence,
  overlapping ranges, output bounds, and source-scope violations
- [ ] T079 [MEM] Replace the extraction prompt and provider mapping with the
  structured candidate contract in `internal/provider/client.go` and
  `internal/jobs/provider.go`
- [ ] T080 [MEM] Implement deterministic normalization for canonical content,
  attributes, language, speaker, absolute/relative time, precision, anchor, and
  evidence roles in `internal/jobs/validation.go`
- [ ] T081 [MEM] Publish structured candidate proposals and complete ordered
  evidence without bypassing confirmation, generation fencing, policy, audit, or
  idempotency

### Candidate Matching And Consolidation

- [ ] T082 [P] [MEM] Add failing exact consolidation tests for canonical hash,
  same source event, subject/kind tuple, attributes, time compatibility, and
  evidence deduplication in `internal/storage/consolidation_test.go`
- [ ] T083 [P] [MEM] Add failing semantic consolidation tests for conservative
  thresholds, unresolved provider classification, false-merge protection, and
  the rule that cosine alone cannot supersede, contest, or delete
- [ ] T084 [P] [MEM] Add failing action tests for `attach_evidence`, `refine`,
  `supersede`, `contest`, `create`, and `reject`, including frozen memories,
  ancestors, siblings, sensitivity, and concurrent current-revision changes
- [ ] T085 [MEM] Implement bounded consolidation candidate recall using canonical
  tuple indexes, evidence proximity, valid-time overlap, and semantic similarity
  in `internal/storage/consolidation.go`
- [ ] T086 [MEM] Implement deterministic consolidation classification first and
  policy-controlled provider classification only for unresolved candidates
- [ ] T087 [MEM] Implement atomic evidence attachment with immutable evidence
  history and no duplicate source ranges
- [ ] T088 [MEM] Implement `refine`, `supersede`, and `contest` as explicit
  evolution proposals with expected-revision and review boundaries
- [ ] T089 [MEM] Integrate consolidation before new candidate confirmation so
  only genuinely novel information creates a new logical memory
- [ ] T090 [MEM] Add consolidation identities, decisions, reasons, and source
  candidate references to audit and observability records

### Quality And Duplicate Projections

- [ ] T091 [P] [MEM] Add failing tests for memory quality components, bounds,
  rebuild determinism, lifecycle effects, contradiction penalties, evidence
  strength, source diversity, and duplicate density
- [ ] T092 [P] [MEM] Add failing duplicate-cluster tests for same memory, shared
  primary evidence, equivalent canonical tuples, high semantic similarity with
  compatible time, and aggregate provenance
- [ ] T093 [MEM] Implement rebuildable memory quality projections in
  `internal/storage/quality.go` without changing eligibility
- [ ] T094 [MEM] Implement rebuildable duplicate clusters and deterministic
  representative selection in `internal/storage/duplicates.go`
- [ ] T095 [MEM] Schedule and generation-fence quality and duplicate projection
  updates from memory, evidence, lifecycle, conflict, and aggregate changes

### Reindexing

- [ ] T096 [P] [MEM] Add failing tests for canonical FTS documents and canonical
  semantic embedding documents across every memory kind
- [ ] T097 [MEM] Rebuild FTS projections from canonical content and field-aware
  structured text in `internal/storage/memories.go` and index code
- [ ] T098 [MEM] Rebuild semantic embeddings from the versioned canonical
  document format and advance compatibility identity
- [ ] T099 [MEM] Update import/export and portability fixtures to preserve new
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

- [ ] T100 [P] [GRAPH] Add failing registry tests for subject/object types,
  literal types, direction, inverse predicates, cardinality, temporal policy,
  allowed path positions, and auto-confirm policy in
  `internal/domain/graph_test.go`
- [ ] T101 [P] [GRAPH] Define a minimal general registry fixture covering
  identity, membership, people/organizations, location, ownership, preference,
  work, participation, creation, plans, event participants/time, dependencies,
  and provenance under `internal/storage/testdata/`
- [ ] T102 [GRAPH] Implement the closed typed predicate registry in domain and
  storage layers and reject provider-defined free-form predicates

### Entity And Event Publication

- [ ] T103 [P] [GRAPH] Add failing entity normalization tests for Unicode,
  punctuation, whitespace, case, language, alias length, pronouns, common terms,
  type compatibility, collision, and scope isolation
- [ ] T104 [P] [GRAPH] Add failing typed event/fact publication tests for direct
  evidence, confidence, cardinality conflicts, literals, temporal bounds,
  revision fences, and review policy
- [ ] T105 [GRAPH] Replace entity normalization and alias publication with the
  versioned collision-safe policy in `internal/storage/entities.go` and graph
  commit code
- [ ] T106 [GRAPH] Implement typed event, participant, place, time, entity fact,
  typed literal, and revision-relation publication in `internal/storage/graph.go`
  and `internal/storage/graph_facts.go`
- [ ] T107 [GRAPH] Persist provider mention confidence and direct evidence and
  prevent context-only mentions/facts from becoming high-confidence authority
- [ ] T108 [GRAPH] Implement predicate-specific auto-review thresholds and
  cardinality/conflict handling with no broad relation threshold bypass

### Entity Resolution

- [ ] T109 [P] [GRAPH] Add failing query-time resolution tests for aliases,
  context/speaker/type disambiguation, multiple alternatives, probability
  normalization, unresolved ambiguity, CJK, and high-degree entities
- [ ] T110 [GRAPH] Implement ambiguity-safe entity resolution that returns
  alternatives rather than selecting by UUID and disables graph expansion when
  confidence is insufficient
- [ ] T111 [GRAPH] Implement entity mention inverse-frequency and graph degree
  statistics as rebuildable projections with generation binding

### Traversal

- [ ] T112 [P] [GRAPH] Add failing traversal tests for direct facts, incoming and
  outgoing edges, typed heterogeneous paths, maximum two hops, allowed path
  positions, fan-out, cycles, temporal filters, scope, evidence, and stable order
- [ ] T113 [P] [GRAPH] Add failing score tests for predicate intent, entity
  confidence, fact confidence, evidence strength, temporal compatibility, hub
  penalty, hop decay, and capped corroboration
- [ ] T114 [GRAPH] Implement query-plan-directed direct fact lookup and typed
  incoming/outgoing one-hop traversal in `internal/storage/graph_recall.go`
- [ ] T115 [GRAPH] Implement registry-approved heterogeneous two-hop paths with
  pre-expansion eligibility filters, bounded fan-out, cycle protection, and
  complete edge evidence
- [ ] T116 [GRAPH] Implement versioned graph scoring with hub penalties and
  confidence products, preserving family explanations through ranking
- [ ] T117 [GRAPH] Remove obsolete same-predicate path logic, old fixed
  predicates, arbitrary ID-ordered windows, and unsupported revision-relation
  candidate expansion

### Graph Quality Gates

- [ ] T118 [P] [GRAPH] Add fixed graph corpora for entity, event, temporal,
  relational, multi-hop, ambiguous, hub, conflict, stale, and out-of-scope cases
  under `internal/storage/testdata/`
- [ ] T119 [GRAPH] Add graph promotion precision, graph-caused rank harm, edge/path
  counts, traversal latency, allocation, and storage-growth gates to `make
  quality` and `make performance`
- [ ] T120 [GRAPH] Run mention-only, fact-only, revision-relation-only, one-hop,
  two-hop, hub-penalty, confidence-gate, seed-budget, and boost-cap ablations and
  record the accepted baseline

**Checkpoint**:

```sh
go test ./internal/domain ./internal/provider ./internal/storage ./internal/daemon
make quality
make performance
make openapi
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

- [ ] T121 [P] [CTX] Add failing tests for duplicate representatives, evidence
  attachment, source/session/entity coverage, semantic redundancy, aggregate
  provenance, token/character/result budgets, and deterministic ties in
  `internal/retrieval/selector_test.go`
- [ ] T122 [P] [CTX] Add failing query-type tests for lookup, verification,
  temporal, multi-hop, procedural, and exploratory selection policies
- [ ] T123 [P] [CTX] Add failing score-discontinuity and low-quality stopping
  tests proving maximum result count does not force irrelevant context
- [ ] T124 [CTX] Define versioned context candidate, selected context item,
  rejected candidate, budget accounting, and selection explanation models in
  `internal/domain/`
- [ ] T125 [CTX] Implement provenance-aware duplicate clustering integration and
  representative/evidence collapse in `internal/retrieval/selector.go`
- [ ] T126 [CTX] Implement deterministic MMR-style utility over relevance,
  evidence coverage, source/entity/session coverage, redundancy, and context
  cost
- [ ] T127 [CTX] Implement question-type policies, exact-match protection,
  score-discontinuity stopping, and hard token/character/result budgets

### Context Rendering And API

- [ ] T128 [P] [CTX] Add failing rendering tests for kind, display content,
  event/valid time, source speaker, complete evidence, graph explanation,
  confidence, lifecycle, and estimated tokens
- [ ] T129 [CTX] Implement structured context packages and deterministic plain
  text rendering without claiming unavailable dates, speakers, or conversation
  reconstruction
- [ ] T130 [CTX] Integrate selection after ranking in recall; expose selected
  context by default and raw candidates/rejections only through policy-safe
  explain/debug output
- [ ] T131 [CTX] Update token estimation and budget enforcement so retrieval and
  downstream generation use the same context-format identity
- [ ] T132 [CTX] Update LoCoMo and LongMemEval adapters to consume structured
  selected context, preserve complete evidence, and remove benchmark-side
  duplicate top-k assembly

### Aggregate Behavior

- [ ] T133 [P] [CTX] Add failing aggregate tests for scene, topic, and timeline
  kinds, bounded deduplicated inputs, complete provenance, source stabilization,
  stale transitions, and review-only rebuild publication
- [ ] T134 [CTX] Add timeline aggregate provider/domain/storage support under
  `internal/aggregate/`, `internal/domain/`, and storage schema contracts
- [ ] T135 [CTX] Exclude stale aggregates from default recall and allow explicit
  debug inclusion with stale reason and provenance
- [ ] T136 [CTX] Prevent broad aggregates from consuming a primary slot when a
  more specific source memory answers the query; attach aggregate synthesis as
  supplementary context when useful
- [ ] T137 [CTX] Mark current aggregate ADR behavior superseded where it conflicts
  with default stale exclusion and timeline support

### Context Quality Gates

- [ ] T138 [P] [CTX] Add selected-context duplicate rate, useful density,
  evidence/source/session coverage, token utilization, and answer-support metrics
  to fixed quality fixtures
- [ ] T139 [CTX] Run fixed-cutoff versus MMR, query-type policy, duplicate
  collapse, aggregate, and structured-rendering ablations on fresh shared corpora
- [ ] T140 [CTX] Promote context-selection defaults only when duplicate rate falls
  at least 50%, multi-evidence coverage improves, and median tokens do not grow
  more than the approved quality budget

**Checkpoint**:

```sh
go test ./internal/retrieval ./internal/aggregate ./internal/storage ./internal/daemon
~/miniforge3/envs/agent/bin/python -m pytest evaluation/locomo/tests evaluation/longmemeval/tests
make quality
make performance
make openapi
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

- [ ] T141 [P] [FEEDBACK] Add failing domain/API tests separating memory quality,
  retrieval, and answer feedback with valid target and reason combinations
- [ ] T142 [P] [FEEDBACK] Add failing retrieval-context tests for query hash/type,
  permitted entity IDs, ranking/features/context identities, rank, selection
  state, matched signals, caller, and timestamp
- [ ] T143 [P] [FEEDBACK] Add failing privacy and poisoning tests for query text,
  sensitivity, caller deduplication, minimum support, clamping, decay, and
  cross-scope isolation
- [ ] T144 [FEEDBACK] Replace feedback domain, schema, API, and OpenAPI contracts
  with the three explicit feedback purposes
- [ ] T145 [FEEDBACK] Persist privacy-safe retrieval and answer decision context
  and reject identity-incompatible feedback

### Aggregation And Ranking

- [ ] T146 [P] [FEEDBACK] Add failing tests proving online and maintenance
  feedback rebuilds produce byte-equivalent aggregates for the same authority
  events
- [ ] T147 [P] [FEEDBACK] Add failing tests for global bounded priors and
  query-conditioned adjustments after minimum support, decay, and caller
  deduplication
- [ ] T148 [FEEDBACK] Implement one shared versioned feedback aggregation function
  used by normal storage operations, maintenance, rebuild, restore, and import
- [ ] T149 [FEEDBACK] Implement bounded global and query-conditioned ranking
  features without changing eligibility or memory authority
- [ ] T150 [FEEDBACK] Add feedback aggregate identity and generation to ranker,
  cursor, explain output, telemetry, export/import, backup/restore, and tests

### Calibration

- [ ] T151 [P] [FEEDBACK] Add deterministic calibration tooling for semantic
  thresholds, keyword weights, temporal features, graph gates/boosts, penalties,
  and context utility under `evaluation/`
- [ ] T152 [FEEDBACK] Define train/development/holdout boundaries and prevent
  benchmark test-set labels from entering runtime feedback or weight fitting
- [ ] T153 [FEEDBACK] Calibrate and record deterministic weights on fixed
  development sets with cross-benchmark and per-query-type reports
- [ ] T154 [FEEDBACK] Promote calibrated weights only when paired confidence
  intervals and all safety/performance gates pass

### Optional Learned Reranker

- [ ] T155 [P] [FEEDBACK] Define a bounded top-window reranker interface with
  deterministic ranker fallback, policy checks, strict provider bounds, and
  complete model/prompt/feature/calibration identity
- [ ] T156 [P] [FEEDBACK] Add tests proving reranking cannot add candidates,
  bypass filters, cross scope, exceed budgets, or publish under a stale identity
- [ ] T157 [FEEDBACK] Implement an evaluation-only reranker adapter after T155 and
  T156; do not enable it in production defaults
- [ ] T158 [FEEDBACK] Compare learned and deterministic reranking on the same
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

- [ ] T159 [P] [OPS] Add failing telemetry tests for query-plan type, executed
  capabilities, candidate counts, rejection gates, score summaries, graph
  seeds/edges/paths/promotions, duplicate clusters, context budgets, identities,
  degradation, and phase latency
- [ ] T160 [P] [OPS] Add failing explain-mode policy tests proving explanations
  cannot expose denied spaces, sensitive content, hidden evidence, or provider
  data unavailable to normal recall
- [ ] T161 [OPS] Implement bounded content-free decision telemetry across query
  planning, recall, graph, ranking, and context selection
- [ ] T162 [OPS] Implement policy-safe per-result feature contributions, graph
  paths, evidence, and selection/rejection reasons in explain mode
- [ ] T163 [OPS] Expose compatible health/index state for consolidation, quality,
  duplicate, semantic calibration, graph, ranker, context, and feedback
  generations

### End-To-End Evaluation

- [ ] T164 [P] [EVAL] Add reproducible scripts for extraction, consolidation,
  indexing, recall, ranking, selection, and end-to-end reports with identical
  query coverage
- [ ] T165 [P] [EVAL] Add the fixed ablation matrix `S`, `SK`, `SKF`, `SKD`,
  `SKP`, and `SKPM`, plus graph-family ablations, to evaluation tooling
- [ ] T166 [EVAL] Run LoCoMo and LongMemEval on fresh shared corpora with fixed
  generation/judge parameters and at least three end-to-end repetitions
- [ ] T167 [EVAL] Report means, standard deviations, paired bootstrap confidence
  intervals, query-type/category deltas, evidence-count buckets, graph promotion
  precision/harm, duplicate rates, latency, storage, and token/provider cost
- [ ] T168 [EVAL] Verify promotion gates: graph Recall@1/MRR regression no worse
  than 0.5 points, graph-promoted top-10 precision at least 50%, positive intended
  category gain without larger aggregate harm, duplicate rate reduced at least
  50%, and no safety regression

### Recovery, Portability, And Performance

- [ ] T169 [P] [OPS] Extend restart/recovery drills for interrupted
  consolidation, quality/duplicate rebuild, graph extraction, calibration,
  ranking generation, context selection, and feedback aggregation
- [ ] T170 [P] [OPS] Extend logical export/import for new authority records and
  prove every derived projection rebuilds to retrieval-equivalent output
- [ ] T171 [P] [OPS] Add scale gates for write amplification, candidate
  consolidation latency, exact semantic search, graph fan-out, feature
  calculation, MMR selection, database growth, and memory allocation
- [ ] T172 [OPS] Verify provider-disabled operation preserves raw events, direct
  memory, keyword recall, deterministic query planning, and safe degraded
  behavior without secret access or network calls

### Contract And Documentation Cleanup

- [ ] T173 [P] [DOC] Update `docs/architecture/overview.md` and
  `docs/architecture/feedback-and-aggregation.md` to describe only the final
  implemented architecture
- [ ] T174 [P] [DOC] Update `README.md`, `docs/README.md`,
  `docs/api/usage.md`, and `docs/operations/runbook.md` for the replacement
  recall and operational model
- [ ] T175 [P] [DOC] Mark superseded ADRs and add links to the replacement ADR;
  preserve decision history without presenting old behavior as current
- [ ] T176 [P] [DOC] Update LoCoMo and LongMemEval READMEs with corrected metric
  semantics, fresh-corpus rules, identities, ablations, and reproducible commands
- [ ] T177 [CONTRACT] Search current code, tests, OpenAPI, schema, examples, and
  documentation and remove obsolete `rrf-v3`, broad `related_to`, old predicate,
  old channel-default, first-evidence-only, stale-aggregate-default, and legacy
  compatibility behavior
- [ ] T178 [CONTRACT] Verify there are no migration files, dual schema branches,
  old API decoders, deprecated fields, legacy cursor/checkpoint parsers, or old
  ranking fallbacks

### Final Gates

- [ ] T179 [OPS] Run formatting, generated-contract, schema, build, unit,
  integration, race, vet, recovery, portability, quality, performance, and
  offline-default checks and resolve all failures
- [ ] T180 [DOC] Record final algorithm identities, accepted benchmark baselines,
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
~/miniforge3/envs/agent/bin/python -m pytest evaluation/locomo/tests evaluation/longmemeval/tests
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

Fill this section only when T180 is complete.

```text
Schema identity:
Processing identity:
Consolidation identity:
Embedding document/query identity:
Semantic calibration identity:
Keyword compiler identity:
Predicate registry identity:
Graph extraction/scoring identity:
Query-plan identity:
Ranking feature/weight identity:
Context-selection identity:
Feedback identity:
Evaluation metric identity:

Accepted benchmark reports:
Quality gate result:
Performance gate result:
Recovery and portability result:
Known limitations:
```
