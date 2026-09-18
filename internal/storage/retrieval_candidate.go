package storage

import gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"

func toGatewayCandidates(candidates []*searchCandidate) []gwretrieval.Candidate {
	result := make([]gwretrieval.Candidate, len(candidates))
	for i, candidate := range candidates {
		result[i] = gwretrieval.Candidate{
			MemoryID: candidate.memoryID, RevisionID: candidate.revisionID, ContentHash: candidate.contentHash,
			FTSContent: candidate.ftsContent, ProjectionContent: candidate.projectionContent,
			KeywordRaw: candidate.keywordRaw, KeywordScore: candidate.keywordScore,
			SemanticRaw: candidate.semanticRaw, SemanticScore: candidate.semanticScore, SemanticPercentile: candidate.semanticPercentile,
			SemanticRejected: candidate.semanticRejected, SemanticRejectReason: candidate.semanticRejectReason,
			GraphScore: candidate.graphScore, Score: candidate.score,
			KeywordRank: candidate.keywordRank, SemanticRank: candidate.semanticRank, GraphRank: candidate.graphRank,
			GraphSignals: candidate.graphSignals, GraphFacts: candidate.graphFacts, GraphPaths: candidate.graphPaths,
			Confidence: candidate.confidence, Specificity: candidate.specificity,
			EventStart: candidate.eventStart, EventEnd: candidate.eventEnd, ValidFrom: candidate.validFrom, ValidUntil: candidate.validUntil,
			TimePrecision: candidate.timePrecision, TimeSource: candidate.timeSource, SourceSpeaker: candidate.sourceSpeaker,
			Language: candidate.language, Kind: candidate.kind, Features: candidate.features, RankOutput: candidate.rankOutput,
			GraphOnly: candidate.graphOnly, Protected: candidate.protected, Quality: candidate.quality,
			DuplicateRisk: candidate.duplicateRisk, Overbreadth: candidate.overbreadth, LifecyclePenalty: candidate.lifecyclePenalty,
		}
	}
	return result
}

func fromGatewayCandidates(candidates []gwretrieval.Candidate) []*searchCandidate {
	result := make([]*searchCandidate, len(candidates))
	for i, candidate := range candidates {
		result[i] = &searchCandidate{
			memoryID: candidate.MemoryID, revisionID: candidate.RevisionID, contentHash: candidate.ContentHash,
			ftsContent: candidate.FTSContent, projectionContent: candidate.ProjectionContent,
			keywordRaw: candidate.KeywordRaw, keywordScore: candidate.KeywordScore,
			semanticRaw: candidate.SemanticRaw, semanticScore: candidate.SemanticScore, semanticPercentile: candidate.SemanticPercentile,
			semanticRejected: candidate.SemanticRejected, semanticRejectReason: candidate.SemanticRejectReason,
			graphScore: candidate.GraphScore, score: candidate.Score,
			keywordRank: candidate.KeywordRank, semanticRank: candidate.SemanticRank, graphRank: candidate.GraphRank,
			graphSignals: candidate.GraphSignals, graphFacts: candidate.GraphFacts, graphPaths: candidate.GraphPaths,
			confidence: candidate.Confidence, specificity: candidate.Specificity,
			eventStart: candidate.EventStart, eventEnd: candidate.EventEnd, validFrom: candidate.ValidFrom, validUntil: candidate.ValidUntil,
			timePrecision: candidate.TimePrecision, timeSource: candidate.TimeSource, sourceSpeaker: candidate.SourceSpeaker,
			language: candidate.Language, kind: candidate.Kind, features: candidate.Features, rankOutput: candidate.RankOutput,
			graphOnly: candidate.GraphOnly, protected: candidate.Protected, quality: candidate.Quality,
			duplicateRisk: candidate.DuplicateRisk, overbreadth: candidate.Overbreadth, lifecyclePenalty: candidate.LifecyclePenalty,
		}
	}
	return result
}

func candidateMap(candidates []*searchCandidate) map[string]*searchCandidate {
	result := make(map[string]*searchCandidate, len(candidates))
	for _, candidate := range candidates {
		result[candidate.memoryID+"\x00"+candidate.revisionID] = candidate
	}
	return result
}

func gatewayRecallParams(params SearchParams) gwretrieval.RecallParams {
	return gwretrieval.RecallParams{
		OperationID: params.OperationID, Query: params.Query, SpaceIDs: params.SpaceIDs,
		IncludeAncestors: params.IncludeAncestors, MemoryTypes: params.MemoryTypes, MemoryTypesSet: params.MemoryTypesSet,
		States: params.States, ReferenceTime: params.ReferenceTime, ReferenceTimeSet: params.ReferenceTimeSet,
		Channels: params.Channels, EffectiveChannels: params.EffectiveChannels, DegradationReasons: params.DegradationReasons,
		SemanticVersion: params.SemanticVersion, Limit: params.Limit, MaxChars: params.MaxChars,
		MaxContextTokens: params.MaxContextTokens, MaxSources: params.MaxSources, MaxSensitive: params.MaxSensitive,
		IncludeEvidence: params.IncludeEvidence, ExcludeStaleAggregates: params.ExcludeStaleAggregates,
		Explain: params.Explain, Cursor: params.Cursor, Intent: params.Intent, GraphPolicy: params.GraphPolicy,
		CursorState: params.CursorState,
		Plan:        params.Plan, QueryTime: params.QueryTime, QueryTimeSet: params.QueryTimeSet,
		SkipDiversification: params.SkipDiversification,
		Telemetry:           params.telemetry,
	}
}

func gatewayCursorBinding(identities searchIdentities) gwretrieval.CursorBinding {
	return gwretrieval.CursorBinding{
		Schema: identities.Schema, QueryPlan: identities.QueryPlan, Membership: identities.Membership,
		Ranking: identities.Ranking, Graph: identities.Graph, Context: identities.Context, Feedback: identities.Feedback,
	}
}
