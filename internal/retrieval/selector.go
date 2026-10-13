package retrieval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

// Context selection is a distinct, versioned stage after ranking. It clusters
// duplicate or provenance-linked candidates, collapses each cluster to one
// representative, and selects representatives under hard budgets with a
// deterministic MMR-style utility. It never changes candidate eligibility or
// relevance; it only decides which representatives consume context slots.
const (
	ContextSelectionVersion = domain.ContextSelectionVersion

	// clusterSemanticCosineFloor is the minimum cosine similarity for two
	// candidates in the same space with compatible time to share a cluster.
	clusterSemanticCosineFloor = 0.94

	// selectionTieEpsilon is the utility tolerance under which the deterministic
	// cluster order (exact protection, then rank) decides instead. It must stay
	// below the smallest diversity weight (Session 0.01) so coverage gains still
	// reorder candidates, and above the context-cost spread of ordinary
	// candidates so size alone never outranks a better-ranked candidate at the
	// same relevance.
	selectionTieEpsilon = 0.001
)

// Selection stop and rejection reasons. Rejections are policy-safe metadata
// exposed only through explain/debug output.
const (
	StopReasonExhausted           = "exhausted"
	StopReasonNoCandidates        = "no_candidates"
	StopReasonResultBudget        = "result_budget"
	StopReasonTokenBudget         = "token_budget"
	StopReasonCharBudget          = "char_budget"
	StopReasonSourceBudget        = "source_budget"
	StopReasonSensitivityBudget   = "sensitivity_budget"
	StopReasonScoreDiscontinuity  = "score_discontinuity"
	StopReasonBelowMinimumUtility = "below_minimum_utility"
	StopReasonDuplicateCluster    = "duplicate_cluster"
)

// SelectionPolicy is the versioned, question-type-specific selection contract.
// Weights are starting points and must be calibrated before promotion; the
// policy identity changes whenever any field changes.
type SelectionPolicy struct {
	Version        string       `json:"version"`
	QuestionType   QuestionType `json:"question_type"`
	Relevance      float64      `json:"relevance"`
	Evidence       float64      `json:"evidence"`
	Source         float64      `json:"source"`
	Entity         float64      `json:"entity"`
	Session        float64      `json:"session"`
	Redundancy     float64      `json:"redundancy"`
	Cost           float64      `json:"cost"`
	Discontinuity  float64      `json:"discontinuity"`
	MinimumUtility float64      `json:"minimum_utility"`
	ProtectExact   bool         `json:"protect_exact"`
	PreserveOrder  bool         `json:"preserve_order"`
}

// Identity returns a deterministic hash of the selection policy.
func (p SelectionPolicy) Identity() string {
	encoded, _ := json.Marshal(p)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

// PolicyForQuestionType returns the deterministic selection policy for a query
// type. Lookup and verification favor one precise source; temporal favors
// event plus anchor evidence; multi-hop favors distinct supporting evidence;
// procedural preserves ordered steps; exploratory favors source and entity
// diversity.
func PolicyForQuestionType(question QuestionType) SelectionPolicy {
	policy := SelectionPolicy{
		Version: ContextSelectionVersion, QuestionType: question, ProtectExact: true,
		Relevance: 1.00, Evidence: 0.05, Source: 0.02, Entity: 0.03, Session: 0.01,
		Redundancy: 0.90, Cost: 0.05, Discontinuity: 0.20, MinimumUtility: 0.20,
	}
	switch question {
	case QuestionVerification:
		policy.Evidence, policy.Source, policy.Redundancy = 0.08, 0.03, 0.80
	case QuestionTemporal:
		policy.Evidence, policy.Source, policy.Entity, policy.Session = 0.20, 0.05, 0.05, 0.05
		policy.Redundancy, policy.Discontinuity, policy.MinimumUtility = 0.70, 0.30, 0.15
	case QuestionMultiHop, QuestionRelational:
		policy.Evidence, policy.Source, policy.Entity, policy.Session = 0.30, 0.20, 0.15, 0.10
		policy.Redundancy, policy.Discontinuity, policy.MinimumUtility = 0.40, 0.60, 0.10
	case QuestionProcedural:
		policy.Evidence, policy.Source, policy.Entity, policy.Session = 0.15, 0.05, 0.05, 0.05
		policy.Redundancy, policy.Discontinuity, policy.MinimumUtility = 0.40, 0.40, 0.10
		policy.PreserveOrder = true
	case QuestionExploratory:
		policy.Relevance = 0.80
		policy.Evidence, policy.Source, policy.Entity, policy.Session = 0.15, 0.30, 0.25, 0.20
		policy.Redundancy, policy.Discontinuity, policy.MinimumUtility = 0.30, 0.60, 0.10
	}
	return policy
}

// SelectInput is the query-local selection request.
type SelectInput struct {
	QuestionType QuestionType
	Budget       domain.ContextBudget
	Candidates   []domain.ContextCandidate
}

// SelectOutput is the deterministic selection result. Raw candidates and
// rejection reasons are retained for policy-safe explain/debug output.
type SelectOutput struct {
	Version    string
	Identity   string
	Policy     SelectionPolicy
	Items      []domain.SelectedContextItem
	Rejected   []domain.RejectedContextCandidate
	Accounting domain.ContextBudgetAccounting
	Stopped    bool
	StopReason string
}

// SelectContext clusters candidates, collapses each cluster to one
// representative, and selects representatives under hard budgets. It is
// deterministic for identical input and never pads output to reach a bound.
func SelectContext(input SelectInput) SelectOutput {
	policy := PolicyForQuestionType(input.QuestionType)
	output := SelectOutput{
		Version: ContextSelectionVersion, Identity: policy.Identity(), Policy: policy,
		Items: []domain.SelectedContextItem{}, Rejected: []domain.RejectedContextCandidate{},
	}
	state := newSelectionState(policy, input.Budget)
	if len(input.Candidates) == 0 {
		output.Stopped, output.StopReason = true, StopReasonNoCandidates
		output.Accounting = state.accounting()
		return output
	}
	clusters := buildContextClusters(input.Candidates)
	// Collapsed members never consume a slot; they contribute corroboration.
	for _, cluster := range clusters {
		for _, member := range cluster.members {
			if member == cluster.rep {
				continue
			}
			state.reject(input.Candidates[member], cluster.id, StopReasonDuplicateCluster)
		}
	}
	order := make([]int, len(clusters))
	for i := range clusters {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return clusterOrderLess(clusters[order[i]], clusters[order[j]], input.Candidates)
	})
	state.mmr(order, clusters, input.Candidates)
	output.Items = state.items
	output.Rejected = state.rejected
	output.Stopped = state.stopped
	output.StopReason = state.stopReason
	output.Accounting = state.accounting()
	return output
}

// SelectContextFixedCutoff selects cluster representatives in deterministic
// rank order without the MMR utility. It is the fixed-cutoff ablation baseline:
// it still collapses duplicate clusters and enforces every hard budget, but it
// never trades relevance for evidence/source coverage. It exists only for
// diagnostic/debug ablations and is bound into the selection identity.
func SelectContextFixedCutoff(input SelectInput) SelectOutput {
	policy := PolicyForQuestionType(input.QuestionType)
	policy.Version = ContextSelectionVersion + ":fixed-cutoff"
	policy.PreserveOrder = true
	policy.Discontinuity = 0
	policy.MinimumUtility = 0
	output := SelectOutput{
		Version: ContextSelectionVersion, Identity: policy.Identity(), Policy: policy,
		Items: []domain.SelectedContextItem{}, Rejected: []domain.RejectedContextCandidate{},
	}
	state := newSelectionState(policy, input.Budget)
	if len(input.Candidates) == 0 {
		output.Stopped, output.StopReason = true, StopReasonNoCandidates
		output.Accounting = state.accounting()
		return output
	}
	clusters := buildContextClusters(input.Candidates)
	for _, cluster := range clusters {
		for _, member := range cluster.members {
			if member == cluster.rep {
				continue
			}
			state.reject(input.Candidates[member], cluster.id, StopReasonDuplicateCluster)
		}
	}
	order := make([]int, len(clusters))
	for i := range clusters {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return clusterOrderLess(clusters[order[i]], clusters[order[j]], input.Candidates)
	})
	for position, index := range order {
		if state.stopped {
			break
		}
		cluster := clusters[index]
		if reason := state.budgetFailure(cluster); reason != "" {
			state.reject(input.Candidates[cluster.rep], cluster.id, reason)
			state.lastBudgetReason = reason
			continue
		}
		state.add(cluster, input.Candidates)
		if state.resultReached() {
			state.stopWith(StopReasonResultBudget, order[position+1:], clusters, input.Candidates)
		}
	}
	if !state.stopped {
		state.stopped, state.stopReason = true, StopReasonExhausted
		if state.lastBudgetReason != "" {
			state.stopReason = state.lastBudgetReason
		}
	}
	output.Items = state.items
	output.Rejected = state.rejected
	output.Stopped = state.stopped
	output.StopReason = state.stopReason
	output.Accounting = state.accounting()
	return output
}

// contextCluster is one provenance-linked group of candidates collapsed to a
// single representative.
type contextCluster struct {
	id         string
	members    []int
	rep        int
	evidence   map[string]struct{}
	sources    map[string]struct{}
	entities   map[string]struct{}
	sessions   map[string]struct{}
	vector     []float64
	canonical  string
	tokens     int
	chars      int
	relevance  float64
	confidence float64
	exact      bool
	sensitive  bool
	aggregate  bool
}

func buildContextClusters(candidates []domain.ContextCandidate) []contextCluster {
	n := len(candidates)
	union := newContextUnionFind(n)
	byMemory := map[string]int{}
	byEvidence := map[string]int{}
	byCanonical := map[string]int{}
	byExplicit := map[string]int{}
	for i := range candidates {
		candidate := candidates[i]
		if candidate.ClusterID != "" {
			if root, ok := byExplicit[candidate.ClusterID]; ok {
				union.union(root, i)
			} else {
				byExplicit[candidate.ClusterID] = i
			}
		}
		if candidate.MemoryID != "" {
			if root, ok := byMemory[candidate.MemoryID]; ok {
				union.union(root, i)
			} else {
				byMemory[candidate.MemoryID] = i
			}
		}
		for _, key := range candidate.PrimaryEvidence {
			if key == "" {
				continue
			}
			if root, ok := byEvidence[key]; ok {
				union.union(root, i)
			} else {
				byEvidence[key] = i
			}
		}
		if candidate.CanonicalHash != "" {
			key := candidate.SpaceID + "\x00" + string(candidate.Kind) + "\x00" + candidate.CanonicalHash
			if root, ok := byCanonical[key]; ok {
				union.union(root, i)
			} else {
				byCanonical[key] = i
			}
		}
	}
	for i := range candidates {
		for _, source := range candidates[i].AggregateSources {
			if root, ok := byMemory[source]; ok {
				union.union(root, i)
			}
		}
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if candidates[i].SpaceID != candidates[j].SpaceID {
				continue
			}
			if len(candidates[i].SemanticVector) == 0 || len(candidates[j].SemanticVector) == 0 {
				continue
			}
			if cosineSimilarity(candidates[i].SemanticVector, candidates[j].SemanticVector) < clusterSemanticCosineFloor {
				continue
			}
			if !intervalsCompatible(candidates[i], candidates[j]) {
				continue
			}
			union.union(i, j)
		}
	}
	groups, order := map[int][]int{}, []int{}
	for i := 0; i < n; i++ {
		root := union.find(i)
		if _, ok := groups[root]; !ok {
			order = append(order, root)
		}
		groups[root] = append(groups[root], i)
	}
	clusters := make([]contextCluster, 0, len(order))
	for _, root := range order {
		members := groups[root]
		cluster := contextCluster{
			members: members, rep: chooseRepresentative(candidates, members),
			evidence: map[string]struct{}{}, sources: map[string]struct{}{},
			entities: map[string]struct{}{}, sessions: map[string]struct{}{},
		}
		for _, id := range members {
			candidate := candidates[id]
			addKeys(cluster.evidence, candidate.PrimaryEvidence)
			addKeys(cluster.sources, candidate.SourceIDs)
			addKeys(cluster.entities, candidate.EntityIDs)
			addKeys(cluster.sessions, candidate.SessionIDs)
			if candidate.ExactPrimary {
				cluster.exact = true
			}
			if candidate.Sensitivity == "sensitive" {
				cluster.sensitive = true
			}
			if candidate.Aggregate {
				cluster.aggregate = true
			}
		}
		cluster.vector = candidates[cluster.rep].SemanticVector
		cluster.canonical = candidates[cluster.rep].CanonicalHash
		cluster.tokens = candidates[cluster.rep].EstimatedTokens
		cluster.chars = candidates[cluster.rep].EstimatedChars
		cluster.relevance = candidates[cluster.rep].Relevance
		cluster.confidence = candidates[cluster.rep].Confidence
		cluster.id = stableClusterID(candidates, members)
		clusters = append(clusters, cluster)
	}
	return clusters
}

func chooseRepresentative(candidates []domain.ContextCandidate, members []int) int {
	best := members[0]
	for _, id := range members[1:] {
		if representativeBetter(candidates[id], candidates[best]) {
			best = id
		}
	}
	return best
}

// representativeBetter keeps exact primary matches, prefers a specific source
// memory over a broad aggregate, then prefers higher relevance and confidence.
func representativeBetter(candidate, current domain.ContextCandidate) bool {
	if candidate.ExactPrimary != current.ExactPrimary {
		return candidate.ExactPrimary
	}
	if candidate.Aggregate != current.Aggregate {
		return !candidate.Aggregate
	}
	if candidate.Relevance != current.Relevance {
		return candidate.Relevance > current.Relevance
	}
	if candidate.Confidence != current.Confidence {
		return candidate.Confidence > current.Confidence
	}
	if candidate.MemoryID != current.MemoryID {
		return candidate.MemoryID < current.MemoryID
	}
	return candidate.RevisionID < current.RevisionID
}

func clusterOrderLess(a, b contextCluster, candidates []domain.ContextCandidate) bool {
	if a.exact != b.exact {
		return a.exact
	}
	rankA, rankB := representativeRank(a, candidates), representativeRank(b, candidates)
	if rankA != rankB {
		return rankA < rankB
	}
	if candidates[a.rep].MemoryID != candidates[b.rep].MemoryID {
		return candidates[a.rep].MemoryID < candidates[b.rep].MemoryID
	}
	return candidates[a.rep].RevisionID < candidates[b.rep].RevisionID
}

func representativeRank(cluster contextCluster, candidates []domain.ContextCandidate) int {
	if rank := candidates[cluster.rep].Rank; rank > 0 {
		return rank
	}
	return cluster.rep
}

type selectionState struct {
	policy                   SelectionPolicy
	budget                   domain.ContextBudget
	items                    []domain.SelectedContextItem
	rejected                 []domain.RejectedContextCandidate
	selected                 []selectedCluster
	coveredEvidence          map[string]struct{}
	coveredSources           map[string]struct{}
	coveredEntities          map[string]struct{}
	coveredSessions          map[string]struct{}
	tokensUsed, charsUsed    int
	resultsUsed, sourcesUsed int
	sensitiveUsed            int
	lastRelevance            float64
	hasLast                  bool
	lastBudgetReason         string
	stopped                  bool
	stopReason               string
}

type selectedCluster struct {
	vector    []float64
	canonical string
	evidence  map[string]struct{}
}

func newSelectionState(policy SelectionPolicy, budget domain.ContextBudget) *selectionState {
	return &selectionState{
		policy: policy, budget: budget,
		items: []domain.SelectedContextItem{}, rejected: []domain.RejectedContextCandidate{},
		coveredEvidence: map[string]struct{}{}, coveredSources: map[string]struct{}{},
		coveredEntities: map[string]struct{}{}, coveredSessions: map[string]struct{}{},
	}
}

func (s *selectionState) mmr(order []int, clusters []contextCluster, candidates []domain.ContextCandidate) {
	pending := append([]int(nil), order...)
	for {
		if s.stopped {
			return
		}
		if len(pending) == 0 {
			s.stopWith(StopReasonExhausted, pending, clusters, candidates)
			return
		}
		position := s.choose(pending, clusters, candidates)
		cluster := clusters[pending[position]]
		if !cluster.exact {
			utility, _ := s.evaluate(cluster)
			if utility < s.policy.MinimumUtility {
				s.stopWith(StopReasonBelowMinimumUtility, pending, clusters, candidates)
				return
			}
			if s.hasLast && s.lastRelevance-cluster.relevance > s.policy.Discontinuity {
				s.stopWith(StopReasonScoreDiscontinuity, pending, clusters, candidates)
				return
			}
		}
		if reason := s.budgetFailure(cluster); reason != "" {
			s.reject(candidates[cluster.rep], cluster.id, reason)
			s.lastBudgetReason = reason
			pending = append(pending[:position], pending[position+1:]...)
			continue
		}
		s.add(cluster, candidates)
		pending = append(pending[:position], pending[position+1:]...)
		if s.resultReached() {
			s.stopWith(StopReasonResultBudget, pending, clusters, candidates)
			return
		}
	}
}

// choose returns the position of the next cluster. Protected exact primary
// clusters are always chosen before any utility comparison so no aggregate or
// diversification decision can displace them.
func (s *selectionState) choose(pending []int, clusters []contextCluster, candidates []domain.ContextCandidate) int {
	if clusters[pending[0]].exact {
		return 0
	}
	best, bestUtility := 0, 0.0
	for position, index := range pending {
		utility, _ := s.evaluate(clusters[index])
		if position == 0 {
			best, bestUtility = 0, utility
			continue
		}
		if utility > bestUtility+selectionTieEpsilon {
			best, bestUtility = position, utility
			continue
		}
		if math.Abs(utility-bestUtility) <= selectionTieEpsilon &&
			clusterOrderLess(clusters[index], clusters[pending[best]], candidates) {
			best, bestUtility = position, utility
		}
	}
	return best
}

func (s *selectionState) evaluate(cluster contextCluster) (float64, domain.ContextSelectionExplanation) {
	relevance := Clamp01(cluster.relevance)
	evidence := coverageGain(cluster.evidence, s.coveredEvidence)
	source := coverageGain(cluster.sources, s.coveredSources)
	entity := coverageGain(cluster.entities, s.coveredEntities)
	session := coverageGain(cluster.sessions, s.coveredSessions)
	redundancy := s.redundancy(cluster)
	cost := s.contextCost(cluster)
	utility := relevance*s.policy.Relevance + evidence*s.policy.Evidence + source*s.policy.Source +
		entity*s.policy.Entity + session*s.policy.Session - redundancy*s.policy.Redundancy - cost*s.policy.Cost
	explanation := domain.ContextSelectionExplanation{
		Utility: utility, Relevance: relevance, EvidenceCoverage: evidence, SourceCoverage: source,
		EntityCoverage: entity, SessionCoverage: session, Redundancy: redundancy, ContextCost: cost,
		CollapsedMembers: len(cluster.members) - 1, ExactPrimary: cluster.exact, Representative: true,
	}
	return utility, explanation
}

func (s *selectionState) redundancy(cluster contextCluster) float64 {
	best := 0.0
	for _, chosen := range s.selected {
		if cluster.canonical != "" && cluster.canonical == chosen.canonical {
			best = 1
		}
		if len(cluster.vector) > 0 && len(chosen.vector) > 0 {
			if similarity := cosineSimilarity(cluster.vector, chosen.vector); similarity > best {
				best = similarity
			}
		}
		if overlap := jaccard(cluster.evidence, chosen.evidence); overlap > best {
			best = overlap
		}
	}
	return Clamp01(best)
}

func (s *selectionState) contextCost(cluster contextCluster) float64 {
	if s.budget.MaxTokens > 0 {
		return Clamp01(float64(cluster.tokens) / float64(s.budget.MaxTokens))
	}
	if s.budget.MaxChars > 0 {
		return Clamp01(float64(cluster.chars) / float64(s.budget.MaxChars))
	}
	return 0
}

func (s *selectionState) budgetFailure(cluster contextCluster) string {
	if s.budget.MaxResults > 0 && s.resultsUsed+1 > s.budget.MaxResults {
		return StopReasonResultBudget
	}
	if s.budget.MaxSources > 0 {
		newSources := 0
		for source := range cluster.sources {
			if _, ok := s.coveredSources[source]; !ok {
				newSources++
			}
		}
		if s.sourcesUsed+newSources > s.budget.MaxSources {
			return StopReasonSourceBudget
		}
	}
	if cluster.sensitive && s.budget.MaxSensitive > 0 && s.sensitiveUsed+1 > s.budget.MaxSensitive {
		return StopReasonSensitivityBudget
	}
	if s.budget.MaxTokens > 0 && s.tokensUsed+cluster.tokens > s.budget.MaxTokens {
		return StopReasonTokenBudget
	}
	if s.budget.MaxChars > 0 && s.charsUsed+cluster.chars > s.budget.MaxChars {
		return StopReasonCharBudget
	}
	return ""
}

func (s *selectionState) add(cluster contextCluster, candidates []domain.ContextCandidate) {
	representative := candidates[cluster.rep]
	_, explanation := s.evaluate(cluster)
	corroborating := []string{}
	supplementary := []string{}
	for _, member := range cluster.members {
		if member == cluster.rep || candidates[member].MemoryID == representative.MemoryID {
			continue
		}
		if candidates[member].Aggregate {
			supplementary = append(supplementary, candidates[member].MemoryID)
			continue
		}
		corroborating = append(corroborating, candidates[member].MemoryID)
	}
	sort.Strings(corroborating)
	sort.Strings(supplementary)
	item := domain.SelectedContextItem{
		Rank: len(s.items) + 1, ClusterID: cluster.id, Representative: true,
		MemoryID: representative.MemoryID, RevisionID: representative.RevisionID,
		Kind: representative.Kind, DisplayContent: representative.DisplayContent,
		EventTimeStart: representative.EventTimeStart, EventTimeEnd: representative.EventTimeEnd,
		ValidFrom: representative.ValidFrom, ValidUntil: representative.ValidUntil,
		TimePrecision: representative.TimePrecision, TimeSource: representative.TimeSource,
		SourceSpeaker: representative.SourceSpeaker, Evidence: sortedKeys(cluster.evidence),
		Confidence: representative.Confidence, LifecycleState: representative.LifecycleState,
		EstimatedTokens: representative.EstimatedTokens, EstimatedChars: representative.EstimatedChars,
		CorroboratingMemoryIDs: corroborating, SupplementaryMemoryIDs: supplementary,
		Utility: explanation.Utility, Explanation: explanation,
	}
	s.items = append(s.items, item)
	for key := range cluster.evidence {
		s.coveredEvidence[key] = struct{}{}
	}
	for key := range cluster.sources {
		s.coveredSources[key] = struct{}{}
	}
	for key := range cluster.entities {
		s.coveredEntities[key] = struct{}{}
	}
	for key := range cluster.sessions {
		s.coveredSessions[key] = struct{}{}
	}
	s.selected = append(s.selected, selectedCluster{vector: cluster.vector, canonical: cluster.canonical, evidence: cluster.evidence})
	s.tokensUsed += representative.EstimatedTokens
	s.charsUsed += representative.EstimatedChars
	s.resultsUsed++
	s.sourcesUsed = len(s.coveredSources)
	if cluster.sensitive {
		s.sensitiveUsed++
	}
	s.lastRelevance, s.hasLast = cluster.relevance, true
}

func (s *selectionState) reject(candidate domain.ContextCandidate, clusterID, reason string) {
	s.rejected = append(s.rejected, domain.RejectedContextCandidate{
		MemoryID: candidate.MemoryID, RevisionID: candidate.RevisionID, Rank: candidate.Rank,
		ClusterID: clusterID, Score: candidate.Relevance, Reason: reason,
	})
}

func (s *selectionState) stopWith(reason string, pending []int, clusters []contextCluster, candidates []domain.ContextCandidate) {
	if reason == StopReasonExhausted && s.lastBudgetReason != "" {
		reason = s.lastBudgetReason
	}
	s.stopped, s.stopReason = true, reason
	for _, index := range pending {
		s.reject(candidates[clusters[index].rep], clusters[index].id, reason)
	}
}

func (s *selectionState) resultReached() bool {
	return s.budget.MaxResults > 0 && s.resultsUsed >= s.budget.MaxResults
}

func (s *selectionState) accounting() domain.ContextBudgetAccounting {
	accounting := domain.ContextBudgetAccounting{
		TokensUsed: s.tokensUsed, CharsUsed: s.charsUsed, ResultsUsed: s.resultsUsed,
		SourcesUsed: s.sourcesUsed, SensitiveUsed: s.sensitiveUsed,
	}
	if s.budget.MaxTokens > 0 {
		accounting.TokensRemaining = s.budget.MaxTokens - s.tokensUsed
	}
	if s.budget.MaxChars > 0 {
		accounting.CharsRemaining = s.budget.MaxChars - s.charsUsed
	}
	if s.budget.MaxResults > 0 {
		accounting.ResultsRemaining = s.budget.MaxResults - s.resultsUsed
	}
	if s.budget.MaxSources > 0 {
		accounting.SourcesRemaining = s.budget.MaxSources - s.sourcesUsed
	}
	if s.budget.MaxSensitive > 0 {
		accounting.SensitiveRemaining = s.budget.MaxSensitive - s.sensitiveUsed
	}
	return accounting
}

func coverageGain(set, covered map[string]struct{}) float64 {
	if len(set) == 0 {
		return 0
	}
	fresh := 0
	for key := range set {
		if _, ok := covered[key]; !ok {
			fresh++
		}
	}
	return float64(fresh) / float64(len(set))
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for key := range a {
		if _, ok := b[key]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func addKeys(set map[string]struct{}, keys []string) {
	for _, key := range keys {
		if key != "" {
			set[key] = struct{}{}
		}
	}
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stableClusterID(candidates []domain.ContextCandidate, members []int) string {
	explicit := []string{}
	revisions := []string{}
	for _, member := range members {
		if candidates[member].ClusterID != "" {
			explicit = append(explicit, candidates[member].ClusterID)
		}
		revisions = append(revisions, candidates[member].RevisionID)
	}
	if len(explicit) > 0 {
		sort.Strings(explicit)
		return explicit[0]
	}
	sort.Strings(revisions)
	hash := sha256.Sum256([]byte(strings.Join(revisions, "\x00")))
	return "cluster-" + hex.EncodeToString(hash[:])
}

func candidateInterval(candidate domain.ContextCandidate) (*time.Time, *time.Time) {
	if candidate.ValidFrom != nil || candidate.ValidUntil != nil {
		return candidate.ValidFrom, candidate.ValidUntil
	}
	return candidate.EventTimeStart, candidate.EventTimeEnd
}

func intervalsCompatible(a, b domain.ContextCandidate) bool {
	aStart, aEnd := candidateInterval(a)
	bStart, bEnd := candidateInterval(b)
	if aStart != nil && bEnd != nil && !aStart.Before(*bEnd) {
		return false
	}
	if bStart != nil && aEnd != nil && !bStart.Before(*aEnd) {
		return false
	}
	return true
}

func cosineSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

type contextUnionFind struct {
	parent []int
}

func newContextUnionFind(size int) *contextUnionFind {
	parent := make([]int, size)
	for i := range parent {
		parent[i] = i
	}
	return &contextUnionFind{parent: parent}
}

func (u *contextUnionFind) find(value int) int {
	for u.parent[value] != value {
		u.parent[value] = u.parent[u.parent[value]]
		value = u.parent[value]
	}
	return value
}

func (u *contextUnionFind) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return
	}
	if ra < rb {
		u.parent[rb] = ra
	} else {
		u.parent[ra] = rb
	}
}
