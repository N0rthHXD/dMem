package retrieval

import (
	"context"
	"errors"
	"sort"
)

// RerankerInterfaceVersion is the versioned contract shared by any reranker,
// learned or deterministic.
const RerankerInterfaceVersion = "reranker-interface-v1"

var (
	ErrRerankWindowExceeded      = errors.New("rerank window exceeds the bounded top window")
	ErrRerankCandidateSetChanged = errors.New("reranker added or dropped candidates")
	ErrRerankScopeViolation      = errors.New("reranker crossed scope")
	ErrRerankBudgetExceeded      = errors.New("reranker exceeded the context budget")
	ErrRerankStaleIdentity       = errors.New("reranker published under a stale identity")
)

// RerankCandidate is one candidate inside the bounded top window. A reranker
// receives exactly these candidates and may only reorder them.
type RerankCandidate struct {
	MemoryID        string
	RevisionID      string
	ScopeID         string
	Score           float64
	EstimatedTokens int
}

// RerankWindow is the immutable, query-bound input to a reranker. It carries
// the complete identity set so a stale reranker cannot publish.
type RerankWindow struct {
	QueryHash           string
	QueryType           string
	RankingIdentity     string
	FeatureIdentity     string
	CalibrationIdentity string
	ScopeIDs            []string
	MaxTokens           int
	Candidates          []RerankCandidate
}

func (w RerankWindow) candidateKey(candidate RerankCandidate) string {
	return candidate.MemoryID + "\x00" + candidate.RevisionID
}

// RerankResult returns the candidate keys in their new order. It must be an
// exact permutation of the input window.
type RerankResult struct {
	Identity string
	Order    []string
}

// Reranker reorders a bounded top window. Implementations must not add
// candidates, bypass filters, cross scope, or exceed budgets.
type Reranker interface {
	Identity() string
	Rerank(ctx context.Context, window RerankWindow, maxWindow int) (RerankResult, error)
}

// ValidateRerankWindow enforces the bounded-window, scope, and budget rules
// before a reranker runs.
func ValidateRerankWindow(window RerankWindow, maxWindow int) error {
	if maxWindow < 1 || len(window.Candidates) > maxWindow {
		return ErrRerankWindowExceeded
	}
	allowed := map[string]bool{}
	for _, scope := range window.ScopeIDs {
		allowed[scope] = true
	}
	tokens := 0
	for _, candidate := range window.Candidates {
		if len(allowed) > 0 && !allowed[candidate.ScopeID] {
			return ErrRerankScopeViolation
		}
		tokens += candidate.EstimatedTokens
	}
	if window.MaxTokens > 0 && tokens > window.MaxTokens {
		return ErrRerankBudgetExceeded
	}
	return nil
}

// ValidateRerankResult proves a reranker only reordered the supplied window
// under the expected identity.
func ValidateRerankResult(window RerankWindow, expectedIdentity string, result RerankResult) error {
	if result.Identity != expectedIdentity {
		return ErrRerankStaleIdentity
	}
	if len(result.Order) != len(window.Candidates) {
		return ErrRerankCandidateSetChanged
	}
	index := map[string]bool{}
	for _, candidate := range window.Candidates {
		index[window.candidateKey(candidate)] = true
	}
	seen := map[string]bool{}
	for _, key := range result.Order {
		if !index[key] {
			return ErrRerankCandidateSetChanged
		}
		if seen[key] {
			return ErrRerankCandidateSetChanged
		}
		seen[key] = true
	}
	return nil
}

// DeterministicReranker is the always-available fallback. It preserves the
// deterministic ranker's order exactly.
type DeterministicReranker struct{}

func (DeterministicReranker) Identity() string {
	return RerankerInterfaceVersion + ":deterministic"
}

func (r DeterministicReranker) Rerank(_ context.Context, window RerankWindow, maxWindow int) (RerankResult, error) {
	if err := ValidateRerankWindow(window, maxWindow); err != nil {
		return RerankResult{}, err
	}
	order := make([]string, 0, len(window.Candidates))
	for _, candidate := range window.Candidates {
		order = append(order, window.candidateKey(candidate))
	}
	return RerankResult{Identity: r.Identity(), Order: order}, nil
}

// EvaluationReranker is an evaluation-only adapter. It is never installed in
// production defaults; callers must opt in explicitly in an evaluation harness.
type EvaluationReranker struct {
	Version string
	Score   func(window RerankWindow, candidate RerankCandidate) float64
}

func (r EvaluationReranker) Identity() string {
	return RerankerInterfaceVersion + ":evaluation:" + r.Version
}

func (r EvaluationReranker) Rerank(_ context.Context, window RerankWindow, maxWindow int) (RerankResult, error) {
	if err := ValidateRerankWindow(window, maxWindow); err != nil {
		return RerankResult{}, err
	}
	ordered := make([]RerankCandidate, len(window.Candidates))
	copy(ordered, window.Candidates)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := 0.0, 0.0
		if r.Score != nil {
			left = r.Score(window, ordered[i])
			right = r.Score(window, ordered[j])
		}
		if left != right {
			return left > right
		}
		if ordered[i].MemoryID != ordered[j].MemoryID {
			return ordered[i].MemoryID < ordered[j].MemoryID
		}
		return ordered[i].RevisionID < ordered[j].RevisionID
	})
	order := make([]string, 0, len(ordered))
	for _, candidate := range ordered {
		order = append(order, window.candidateKey(candidate))
	}
	return RerankResult{Identity: r.Identity(), Order: order}, nil
}

// RerankerOnlyEvaluation documents that a learned reranker must remain
// evaluation-only until promotion gates pass.
type RerankerMode string

const (
	RerankerModeOff        RerankerMode = "off"
	RerankerModeEvaluation RerankerMode = "evaluation"
)

// SelectReranker returns the deterministic fallback unless the caller
// explicitly opted into evaluation mode.
func SelectReranker(mode RerankerMode, evaluation Reranker) Reranker {
	if mode == RerankerModeEvaluation && evaluation != nil {
		return evaluation
	}
	return DeterministicReranker{}
}

// RerankPromotionThresholds are the minimum gains a learned reranker must show
// on fixed candidate windows before promotion. Zero or negative values disable
// a gate.
type RerankPromotionThresholds struct {
	MinimumMeanRankImprovement float64
	MaximumLatencyRatio        float64
	MaximumVarianceIncrease    float64
	MinimumWindowCount         int
}

// RerankPromotionEvidence is the paired comparison of learned and
// deterministic reranking on identical candidate windows.
type RerankPromotionEvidence struct {
	WindowCount         int
	MeanRankImprovement float64
	LatencyRatio        float64
	VarianceIncrease    float64
	DegradationCount    int
}

// PromotionAllowed reports whether every gate passes. Any observed degradation
// blocks promotion regardless of average improvement.
func PromotionAllowed(evidence RerankPromotionEvidence, thresholds RerankPromotionThresholds) bool {
	if evidence.WindowCount < thresholds.MinimumWindowCount {
		return false
	}
	if evidence.DegradationCount > 0 {
		return false
	}
	if evidence.MeanRankImprovement < thresholds.MinimumMeanRankImprovement {
		return false
	}
	if thresholds.MaximumLatencyRatio > 0 && evidence.LatencyRatio > thresholds.MaximumLatencyRatio {
		return false
	}
	if thresholds.MaximumVarianceIncrease > 0 && evidence.VarianceIncrease > thresholds.MaximumVarianceIncrease {
		return false
	}
	return true
}
