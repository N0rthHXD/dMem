package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/gateway/agenttool"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

// commitAgentToolSuggestion commits one low-risk Agent Tool memory suggestion
// and returns its outcome. It is the shared fixture for the Phase 3 no-provider
// recall tests.
func commitAgentToolSuggestion(t *testing.T, capability *AgentToolCapability, key, scopeID, kind, content string, hints []string, validFrom *time.Time) agenttool.CommitOutcome {
	t.Helper()
	params := agenttool.CommitParams{
		ScopeID: scopeID,
		Source: agenttool.Source{
			Type: "agent_tool", Name: "assistant", ConversationID: "conversation-1",
			TurnID: "turn-1", OccurredAt: time.Date(2026, 8, 21, 11, 0, 0, 0, time.UTC),
		},
		Memories: []agenttool.MemorySuggestion{{
			LocalID: "memory-1", Kind: domain.MemoryKind(kind), Content: content,
			Sensitivity: agenttool.SensitivityNormal, Confidence: 0.9, SearchHints: hints, ValidFrom: validFrom,
		}},
	}
	record, replay, err := capability.CommitMemory(context.Background(), agentToolIdentity(t, key, params, agenttool.OperationMemoryCommit), params)
	if err != nil || replay || len(record.Memories) != 1 {
		t.Fatalf("commit %s: record=%+v replay=%t error=%v", key, record, replay, err)
	}
	return record.Memories[0]
}

func insertAgentToolSiblingSpace(t *testing.T, repository *Repository) {
	t.Helper()
	if _, err := repository.store.writer.Exec(`INSERT INTO memory_spaces(space_id, parent_space_id, space_type, external_id, display_name, created_at) VALUES ('space-sibling','space-1','project','project-sibling','Sibling','2026-08-21T11:00:00Z')`); err != nil {
		t.Fatal(err)
	}
}

func agentToolRecall(t *testing.T, repository *Repository, query string, scopeIDs []string, channels []string, limit int, explain bool, operationID string) domain.SearchResponse {
	t.Helper()
	result, err := repository.SearchMemories(context.Background(), SearchParams{
		OperationID: operationID, Query: query, SpaceIDs: scopeIDs, States: []string{"active", "contested"},
		Channels: channels, Limit: limit, MaxChars: 100000, MaxContextTokens: 100000, Explain: explain,
	})
	if err != nil {
		t.Fatalf("recall %q: %v", query, err)
	}
	return result
}

func agentToolRecallIDs(t *testing.T, repository *Repository, query string, scopeIDs []string, limit int) []string {
	t.Helper()
	result := agentToolRecall(t, repository, query, scopeIDs, []string{"keyword", "graph"}, limit, false, "")
	ids := make([]string, len(result.Context.Items))
	for i := range result.Context.Items {
		ids[i] = result.Context.Items[i].MemoryID
	}
	return ids
}

// TestAgentToolSearchHintsProjectedIntoFTSSeparateFromCanonicalDocument proves
// hints are indexed in a dedicated FTS field and never enter the canonical
// document used for exact-match scoring and embedding.
func TestAgentToolSearchHintsProjectedIntoFTSSeparateFromCanonicalDocument(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	capability := NewAgentToolCapability(repository)
	outcome := commitAgentToolSuggestion(t, capability, "hints-projection", "space-1", "preference", "PostgreSQL is preferred", []string{"database choice"}, nil)

	var content, hints string
	if err := repository.store.reader.QueryRow(`SELECT content, hints FROM memory_fts WHERE memory_id=?`, outcome.MemoryID).Scan(&content, &hints); err != nil {
		t.Fatal(err)
	}
	if hints != "database choice" {
		t.Fatalf("fts hints = %q, want the committed hint", hints)
	}
	revision, err := repository.GetMemory(context.Background(), outcome.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	if want := canonicalStructuredDocument(revision.CurrentRevision); content != want {
		t.Fatalf("fts content changed by hints:\n got %q\nwant %q", content, want)
	}
	if strings.Contains(content, "database choice") {
		t.Fatal("hint text leaked into the canonical FTS document")
	}
	var matched int
	if err := repository.store.reader.QueryRow(`SELECT count(*) FROM memory_fts WHERE memory_id=? AND memory_fts MATCH ?`, outcome.MemoryID, `"database"`).Scan(&matched); err != nil || matched != 1 {
		t.Fatalf("hint term not searchable: matched=%d err=%v", matched, err)
	}
}

// TestAgentToolHintRecallRanksCanonicalContentFirst proves a hint-only match is
// recallable but never outranks a canonical content match.
func TestAgentToolHintRecallRanksCanonicalContentFirst(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	capability := NewAgentToolCapability(repository)
	hintOnly := commitAgentToolSuggestion(t, capability, "hint-only", "space-1", "fact", "kubernetes cluster autoscaling", []string{"container orchestration"}, nil)
	canonical := commitAgentToolSuggestion(t, capability, "canonical-content", "space-1", "fact", "container orchestration guide", nil, nil)

	result := agentToolRecall(t, repository, "container orchestration", []string{"space-1"}, []string{"keyword"}, 10, false, "")
	if len(result.Results) < 2 {
		t.Fatalf("expected both candidates, got %+v", result.Results)
	}
	if result.Results[0].Memory.MemoryID != canonical.MemoryID {
		t.Fatalf("hint-only candidate outranked canonical content: %+v", result.Results)
	}
	if result.Results[1].Memory.MemoryID != hintOnly.MemoryID {
		t.Fatalf("hint-only candidate missing from recall: %+v", result.Results)
	}
	if len(result.Results[1].MatchedChannels) != 1 || result.Results[1].MatchedChannels[0] != ChannelKeyword {
		t.Fatalf("hint-only matched channels = %v", result.Results[1].MatchedChannels)
	}
}

// TestAgentToolRecallReportsSemanticDegradationWithoutEmbedding proves the
// keyword channel closes the loop and semantic is reported as unavailable
// instead of silently claiming a semantic or hybrid score.
func TestAgentToolRecallReportsSemanticDegradationWithoutEmbedding(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	if repository.emb().Provider != nil {
		t.Fatal("test requires an unconfigured embedding provider")
	}
	capability := NewAgentToolCapability(repository)
	outcome := commitAgentToolSuggestion(t, capability, "degraded", "space-1", "preference", "PostgreSQL is preferred", []string{"database choice"}, nil)

	result := agentToolRecall(t, repository, "database choice", []string{"space-1"}, []string{"keyword", "semantic", "graph"}, 10, false, "")
	if len(result.Results) != 1 || result.Results[0].Memory.MemoryID != outcome.MemoryID {
		t.Fatalf("recall results = %+v", result.Results)
	}
	if len(result.Context.Items) != 1 || result.Context.Items[0].MemoryID != outcome.MemoryID {
		t.Fatalf("hint-only candidate was not selected into the returned context: %+v", result.Context.Items)
	}
	if !contains(result.ExecutedChannels, ChannelKeyword) {
		t.Fatalf("keyword channel did not execute: %v", result.ExecutedChannels)
	}
	if contains(result.ExecutedChannels, ChannelSemantic) {
		t.Fatalf("semantic channel executed without a provider: %v", result.ExecutedChannels)
	}
	if !contains(result.DegradationReasons, "semantic_unavailable") {
		t.Fatalf("degradation reasons = %v, want semantic_unavailable", result.DegradationReasons)
	}
	for _, item := range result.Results {
		if item.SemanticScore != nil {
			t.Fatalf("semantic score returned without semantic execution: %v", *item.SemanticScore)
		}
		if contains(item.MatchedChannels, ChannelSemantic) {
			t.Fatalf("semantic channel claimed without execution: %v", item.MatchedChannels)
		}
	}
}

// TestAgentToolRecallFiltersScopeSensitivityLifecycleAndTime proves every
// eligibility filter applies before a hinted candidate can be returned.
func TestAgentToolRecallFiltersScopeSensitivityLifecycleAndTime(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	insertAgentToolSiblingSpace(t, repository)
	capability := NewAgentToolCapability(repository)

	visible := commitAgentToolSuggestion(t, capability, "filter-visible", "space-1", "fact", "filter probe visible memory", []string{"filter-token"}, nil)
	sibling := commitAgentToolSuggestion(t, capability, "filter-sibling", "space-sibling", "fact", "filter probe sibling memory", []string{"filter-token"}, nil)

	sensitiveParams := agenttool.CommitParams{
		ScopeID: "space-1",
		Source:  agenttool.Source{Type: "agent_tool", Name: "assistant", ConversationID: "conversation-1", TurnID: "turn-1", OccurredAt: time.Date(2026, 8, 21, 11, 0, 0, 0, time.UTC)},
		Memories: []agenttool.MemorySuggestion{{
			LocalID: "memory-1", Kind: domain.MemoryKindFact, Content: "filter probe sensitive memory",
			Sensitivity: agenttool.SensitivitySensitive, Confidence: 0.9, SearchHints: []string{"filter-token"},
		}},
	}
	sensitiveRecord, _, err := capability.CommitMemory(context.Background(), agentToolIdentity(t, "filter-sensitive", sensitiveParams, agenttool.OperationMemoryCommit), sensitiveParams)
	if err != nil || sensitiveRecord.Memories[0].Status != agenttool.CommitProposal || sensitiveRecord.Memories[0].RevisionID != "" {
		t.Fatalf("sensitive commit = %+v error=%v", sensitiveRecord, err)
	}

	future := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	futureMemory := commitAgentToolSuggestion(t, capability, "filter-future", "space-1", "fact", "filter probe future memory", []string{"filter-token"}, &future)

	forgotten := commitAgentToolSuggestion(t, capability, "filter-forgotten", "space-1", "fact", "filter probe forgotten memory", []string{"filter-token"}, nil)
	forget := agenttool.ForgetParams{ScopeID: "space-1", MemoryID: forgotten.MemoryID, ExpectedRevisionID: forgotten.RevisionID, Reason: "filter probe"}
	if _, _, err := capability.ForgetMemory(context.Background(), agentToolIdentity(t, "filter-forget", forget, agenttool.OperationForget), forget); err != nil {
		t.Fatal(err)
	}

	result := agentToolRecall(t, repository, "filter-token", []string{"space-1"}, []string{"keyword"}, 20, false, "")
	ids := make([]string, len(result.Results))
	for i := range result.Results {
		ids[i] = result.Results[i].Memory.MemoryID
	}
	if !includesID(ids, visible.MemoryID) {
		t.Fatalf("visible hinted memory missing: %v", ids)
	}
	for name, excluded := range map[string]string{
		"sibling":   sibling.MemoryID,
		"future":    futureMemory.MemoryID,
		"forgotten": forgotten.MemoryID,
	} {
		if includesID(ids, excluded) {
			t.Fatalf("%s memory leaked into recall: %v", name, ids)
		}
	}
	for _, item := range result.Results {
		if strings.Contains(item.Memory.CurrentRevision.DisplayContent, "sensitive") {
			t.Fatalf("sensitive proposal content leaked into recall: %+v", item.Memory)
		}
	}
	if futureMemory.MemoryID == visible.MemoryID || sibling.MemoryID == visible.MemoryID {
		t.Fatal("fixture did not create distinct memories")
	}
}

// TestAgentToolRecallRespectsResultBudget proves the requested result budget is
// enforced on the returned context, not just on the internal ranked window.
func TestAgentToolRecallRespectsResultBudget(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	capability := NewAgentToolCapability(repository)
	for i := 0; i < 3; i++ {
		commitAgentToolSuggestion(t, capability, "budget-"+formatInt(int64(i)), "space-1", "fact", "budget probe memory "+formatInt(int64(i)), []string{"budget-token"}, nil)
	}
	result := agentToolRecall(t, repository, "budget-token", []string{"space-1"}, []string{"keyword"}, 2, false, "")
	if len(result.Context.Items) > 2 {
		t.Fatalf("returned %d context items, want at most the requested budget of 2", len(result.Context.Items))
	}
	if len(result.Context.Items) == 0 {
		t.Fatal("budget recall returned no context")
	}
}

// TestAgentToolRecallExplainAndTelemetryDoNotLeakHintsOrQuery proves explain
// reports field names only and content-free telemetry never stores hint text,
// query text, or memory prose.
func TestAgentToolRecallExplainAndTelemetryDoNotLeakHintsOrQuery(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	capability := NewAgentToolCapability(repository)
	const (
		queryText   = "leak-hint-token-xyz"
		hintText    = "leak-hint-token-xyz"
		contentText = "leak probe memory prose"
	)
	commitAgentToolSuggestion(t, capability, "leak-probe", "space-1", "fact", contentText, []string{hintText}, nil)

	result := agentToolRecall(t, repository, queryText, []string{"space-1"}, []string{"keyword"}, 10, true, "op-leak-probe")
	if len(result.Results) != 1 {
		t.Fatalf("recall results = %+v", result.Results)
	}
	explain := result.Results[0]
	if !contains(explain.KeywordMatchedFields, "hints") {
		t.Fatalf("explain matched fields = %v, want hints", explain.KeywordMatchedFields)
	}
	for _, field := range explain.KeywordMatchedFields {
		if field != "content" && field != "hints" {
			t.Fatalf("explain leaked a non-field value: %q", field)
		}
	}

	forbidden := []string{queryText, hintText, contentText}
	telemetryColumns := `SELECT coalesce(operation_id,''),coalesce(query_plan_identity,''),coalesce(query_hash,''),coalesce(query_type,''),coalesce(requested_scope_json,''),coalesce(requested_capabilities_json,''),coalesce(executed_capabilities_json,''),coalesce(candidate_counts_json,''),coalesce(rejection_counts_json,''),coalesce(score_summary_json,''),coalesce(graph_counts_json,''),coalesce(degradation_reasons_json,''),coalesce(phase_latency_ms_json,''),coalesce(ranking_identity,''),coalesce(context_identity,''),coalesce(feedback_identity,''),coalesce(reference_time,'') FROM query_telemetry`
	assertNoLeak(t, repository, telemetryColumns, forbidden)
	assertNoLeak(t, repository, `SELECT operation_id,memory_id,revision_id,coalesce(selection_state,''),coalesce(selection_reason,''),coalesce(context_identity,'') FROM context_decisions`, forbidden)
}

func assertNoLeak(t *testing.T, repository *Repository, query string, forbidden []string) {
	t.Helper()
	rows, err := repository.store.reader.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	scanned := make([]any, len(columns))
	for i := range scanned {
		var value string
		scanned[i] = &value
	}
	for rows.Next() {
		if err := rows.Scan(scanned...); err != nil {
			t.Fatal(err)
		}
		for i := range scanned {
			value := *(scanned[i].(*string))
			for _, needle := range forbidden {
				if needle != "" && strings.Contains(value, needle) {
					t.Fatalf("telemetry column %s leaked %q: %q", columns[i], needle, value)
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestEntityAliasAndGraphRecallWithoutEmbedding proves entity names, aliases,
// and typed graph expansion add candidates with no embedding provider.
func TestEntityAliasAndGraphRecallWithoutEmbedding(t *testing.T) {
	repository, _ := newMemoryTestRepository(t)
	if repository.emb().Provider != nil {
		t.Fatal("test requires an unconfigured embedding provider")
	}
	enableGraphCandidates(repository)
	seedEntity(t, repository, "entity-alice", "Alice", "person")
	seedEntity(t, repository, "entity-alpha", "Alpha", "project")
	if _, err := repository.store.writer.Exec(`INSERT INTO entity_aliases(alias_id, entity_id, alias, normalized_alias, normalization_version, source, state, created_by, created_at, updated_at) VALUES ('alias-alicia','entity-alice','Alicia','alicia','v1','human_confirmation','active','test','2026-08-21T11:00:00Z','2026-08-21T11:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	mentionMemory := createTestMemory(t, repository, "graph-alias-mention")
	seedMention(t, repository, "mention-alicia", "entity-alice", mentionMemory.MemoryID, mentionMemory.CurrentRevision.RevisionID, 0, 5)
	factMemory := createTestMemory(t, repository, "graph-alias-fact")
	if _, err := repository.store.writer.Exec(`INSERT INTO entity_facts(fact_id, space_id, subject_entity_id, predicate, object_entity_id, source_memory_id, source_revision_id, confidence, state, source, specification_hash, created_by, processing_version, created_at, updated_at) VALUES ('fact-alice-alpha','space-1','entity-alice','participates_in','entity-alpha',?,?,0.9,'active','auto_review','hash','test','v1','2026-08-21T11:00:00Z','2026-08-21T11:00:00Z')`, factMemory.MemoryID, factMemory.CurrentRevision.RevisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`INSERT INTO entity_fact_evidence(evidence_id,fact_id,evidence_type,created_by,created_at) VALUES ('fact-alice-alpha-evidence','fact-alice-alpha','human_confirmation','test','2026-08-21T11:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.store.writer.Exec(`INSERT INTO memory_relations(relation_id, space_id, relation_type, source_memory_id, source_revision_id, target_memory_id, target_revision_id, state, source, confidence, specification_hash, created_by, processing_version, created_at, updated_at) VALUES ('alias-relation','space-1','supports',?,?,?,?, 'active','human_confirmation',0.8,'hash','test','v1','2026-08-21T11:00:00Z','2026-08-21T11:00:00Z')`, factMemory.MemoryID, factMemory.CurrentRevision.RevisionID, mentionMemory.MemoryID, mentionMemory.CurrentRevision.RevisionID); err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildGraphIndex(context.Background()); err != nil {
		t.Fatal(err)
	}

	result := agentToolRecall(t, repository, "Alicia participates in Alpha", []string{"space-1"}, []string{"keyword", "graph"}, 20, false, "")
	if len(result.ResolvedEntities) == 0 || result.ResolvedEntities[0].EntityID != "entity-alice" {
		t.Fatalf("alias did not resolve without embedding: %+v", result.ResolvedEntities)
	}
	if !contains(result.ExecutedChannels, ChannelGraph) {
		t.Fatalf("graph channel did not execute: %v", result.ExecutedChannels)
	}
	signals := map[string]bool{}
	for _, item := range result.Results {
		for _, signal := range item.GraphSignals {
			signals[signal.Kind] = true
		}
		if item.SemanticScore != nil {
			t.Fatalf("semantic score returned without a provider: %v", *item.SemanticScore)
		}
	}
	if !signals["direct_entity_mention"] {
		t.Fatalf("alias entity mention did not contribute a graph candidate: %+v", result.Results)
	}
}

type agentToolRecallCorpus struct {
	Version      string                 `json:"version"`
	PrimarySpace string                 `json:"primary_space"`
	SiblingSpace string                 `json:"sibling_space"`
	Documents    []agentToolRecallDoc   `json:"documents"`
	Queries      []agentToolRecallQuery `json:"queries"`
}

type agentToolRecallDoc struct {
	ID              string   `json:"id"`
	Space           string   `json:"space"`
	Kind            string   `json:"kind"`
	Content         string   `json:"content"`
	Hints           []string `json:"hints"`
	ExactIdentifier string   `json:"exact_identifier,omitempty"`
}

type agentToolRecallQuery struct {
	ID       string   `json:"id"`
	Text     string   `json:"text"`
	Relevant []string `json:"relevant"`
}

type agentToolRecallGates struct {
	ConfigVersion          string `json:"config_version"`
	CorpusVersion          string `json:"corpus_version"`
	KeywordCompilerVersion string `json:"keyword_compiler_version"`
	RankingVersion         string `json:"ranking_version"`
	Quality                struct {
		RecallK                     int     `json:"recall_k"`
		MinimumMacroRecallAtK       float64 `json:"minimum_macro_recall_at_k"`
		MinimumCanonicalExactRecall float64 `json:"minimum_canonical_exact_recall"`
		MaximumSiblingContamination int     `json:"maximum_sibling_contamination"`
	} `json:"quality"`
}

func loadAgentToolRecallEvaluation(t *testing.T) (agentToolRecallCorpus, agentToolRecallGates) {
	t.Helper()
	var corpus agentToolRecallCorpus
	var gates agentToolRecallGates
	readJSONFixture(t, "testdata/agent-tool-recall-corpus-v1.json", &corpus)
	readJSONFixture(t, "testdata/agent-tool-recall-gates-v1.json", &gates)
	if corpus.Version != gates.CorpusVersion || gates.ConfigVersion != "agent-tool-recall-gates-v1" ||
		gates.KeywordCompilerVersion != retrieval.KeywordCompilerVersion || gates.RankingVersion != rankingVersion {
		t.Fatalf("agent tool recall gate identity mismatch: corpus=%q config=%q keyword=%q ranking=%q", corpus.Version, gates.ConfigVersion, gates.KeywordCompilerVersion, gates.RankingVersion)
	}
	if gates.Quality.RecallK < 1 || gates.Quality.MinimumMacroRecallAtK <= 0 {
		t.Fatal("agent tool recall gate configuration is not meaningful")
	}
	return corpus, gates
}

func agentToolRecallFixture(t *testing.T, corpus agentToolRecallCorpus) (*Repository, map[string]string) {
	t.Helper()
	repository, _ := newMemoryTestRepository(t)
	if repository.emb().Provider != nil {
		t.Fatal("agent tool recall corpus must run without an embedding provider")
	}
	insertAgentToolSiblingSpace(t, repository)
	capability := NewAgentToolCapability(repository)
	docIDs := make(map[string]string, len(corpus.Documents))
	for _, document := range corpus.Documents {
		space := corpus.PrimarySpace
		if document.Space == "sibling" {
			space = corpus.SiblingSpace
		}
		outcome := commitAgentToolSuggestion(t, capability, "agent-tool-corpus-"+document.ID, space, document.Kind, document.Content, document.Hints, nil)
		if outcome.Status != agenttool.CommitAuthority {
			t.Fatalf("document %s status = %s", document.ID, outcome.Status)
		}
		docIDs[document.ID] = outcome.MemoryID
	}
	return repository, docIDs
}

// TestAgentToolRecallQualityFixedCorpus is the Phase 3 no-provider retrieval
// gate over Agent Tool typical queries: synonyms, entity aliases, temporal
// questions, preferences, decisions, and procedural memory.
func TestAgentToolRecallQualityFixedCorpus(t *testing.T) {
	corpus, gates := loadAgentToolRecallEvaluation(t)
	repository, docIDs := agentToolRecallFixture(t, corpus)

	macroRecall := 0.0
	for _, query := range corpus.Queries {
		relevant := make(map[string]bool, len(query.Relevant))
		for _, id := range query.Relevant {
			relevant[docIDs[id]] = true
		}
		ids := agentToolRecallIDs(t, repository, query.Text, []string{corpus.PrimarySpace}, gates.Quality.RecallK)
		macroRecall += recallAtK(ids, relevant)
	}
	macroRecall /= float64(len(corpus.Queries))
	if macroRecall < gates.Quality.MinimumMacroRecallAtK {
		t.Fatalf("macro recall@%d = %.3f, want >= %.3f", gates.Quality.RecallK, macroRecall, gates.Quality.MinimumMacroRecallAtK)
	}
	t.Logf("agent tool recall quality: macro recall@%d=%.3f queries=%d", gates.Quality.RecallK, macroRecall, len(corpus.Queries))

	hits, primary := 0, 0
	for _, document := range corpus.Documents {
		if document.Space != "primary" || document.ExactIdentifier == "" {
			continue
		}
		primary++
		if includesID(agentToolRecallIDs(t, repository, `"`+document.ExactIdentifier+`"`, []string{corpus.PrimarySpace}, 5), docIDs[document.ID]) {
			hits++
		}
	}
	exactRecall := float64(hits) / float64(primary)
	if exactRecall < gates.Quality.MinimumCanonicalExactRecall {
		t.Fatalf("canonical exact recall = %.3f, want >= %.3f", exactRecall, gates.Quality.MinimumCanonicalExactRecall)
	}
}

// TestAgentToolRecallEveryQueryHitsTopK is the strict per-query companion to the
// macro gate. The macro average can absorb a query that recall misses entirely:
// every query in the fixed corpus must recall all of its relevant documents
// within the recall window. Chinese queries in particular depend on the literal
// substring path, which a single failed query used to be able to hide behind the
// macro average.
func TestAgentToolRecallEveryQueryHitsTopK(t *testing.T) {
	corpus, gates := loadAgentToolRecallEvaluation(t)
	repository, docIDs := agentToolRecallFixture(t, corpus)

	for _, query := range corpus.Queries {
		relevant := make(map[string]bool, len(query.Relevant))
		for _, id := range query.Relevant {
			relevant[docIDs[id]] = true
		}
		ids := agentToolRecallIDs(t, repository, query.Text, []string{corpus.PrimarySpace}, gates.Quality.RecallK)
		if recall := recallAtK(ids, relevant); recall < 1 {
			t.Fatalf("query %q recall@%d = %.3f, want 1.0 (recalled=%v relevant=%v)",
				query.Text, gates.Quality.RecallK, recall, ids, query.Relevant)
		}
	}
}

// TestAgentToolRecallSiblingContaminationZero proves hinted sibling scopes never
// leak into recall across keyword or graph execution.
func TestAgentToolRecallSiblingContaminationZero(t *testing.T) {
	corpus, gates := loadAgentToolRecallEvaluation(t)
	repository, docIDs := agentToolRecallFixture(t, corpus)
	siblingID := docIDs["sibling-private"]
	contamination := 0
	for _, channels := range [][]string{{"keyword"}, {"keyword", "graph"}} {
		result := agentToolRecall(t, repository, "vacation travel", []string{corpus.PrimarySpace}, channels, 20, false, "")
		for _, item := range result.Results {
			if item.Memory.MemoryID == siblingID {
				contamination++
			}
		}
	}
	if contamination > gates.Quality.MaximumSiblingContamination {
		t.Fatalf("sibling contamination = %d, want <= %d", contamination, gates.Quality.MaximumSiblingContamination)
	}
}
