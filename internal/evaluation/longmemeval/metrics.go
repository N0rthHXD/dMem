package longmemeval

import "math"

type CutoffMetrics struct {
	RecallAny float64 `json:"recall_any"`
	RecallAll float64 `json:"recall_all"`
	NDCG      float64 `json:"ndcg"`
}

func RetrievalMetrics(rankedRelevant []bool, relevantTotal, cutoff int) CutoffMetrics {
	if cutoff > len(rankedRelevant) {
		cutoff = len(rankedRelevant)
	}
	found := 0
	dcg := 0.0
	for i := 0; i < cutoff; i++ {
		if !rankedRelevant[i] {
			continue
		}
		found++
		if i == 0 {
			dcg++
		} else {
			dcg += 1 / math.Log2(float64(i+1))
		}
	}
	idcg := 0.0
	ideal := relevantTotal
	if ideal > cutoff {
		ideal = cutoff
	}
	for i := 0; i < ideal; i++ {
		if i == 0 {
			idcg++
		} else {
			idcg += 1 / math.Log2(float64(i+1))
		}
	}
	result := CutoffMetrics{}
	if found > 0 {
		result.RecallAny = 1
	}
	if relevantTotal > 0 && found == relevantTotal {
		result.RecallAll = 1
	}
	if idcg > 0 {
		result.NDCG = dcg / idcg
	}
	return result
}

func SessionRetrievalMetrics(rankedSessionIDs, relevantSessionIDs []string, cutoff int) CutoffMetrics {
	relevant := make(map[string]bool, len(relevantSessionIDs))
	for _, id := range relevantSessionIDs {
		relevant[id] = true
	}
	seen := map[string]bool{}
	rankedRelevant := make([]bool, 0, len(rankedSessionIDs))
	for _, id := range rankedSessionIDs {
		if id == "" {
			rankedRelevant = append(rankedRelevant, false)
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		rankedRelevant = append(rankedRelevant, relevant[id])
	}
	return RetrievalMetrics(rankedRelevant, len(relevant), cutoff)
}
