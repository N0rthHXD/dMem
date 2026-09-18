package storage

import (
	"github.com/N0rthHXD/dMem/internal/gateway/consolidation"
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
	"github.com/N0rthHXD/dMem/internal/gateway/feedback"
	"github.com/N0rthHXD/dMem/internal/gateway/knowledge"
	"github.com/N0rthHXD/dMem/internal/gateway/memory"
	"github.com/N0rthHXD/dMem/internal/gateway/policy"
	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/gateway/scope"
)

// Compile-time assertions that the SQLite-backed capability adapters satisfy the
// gateway-consumer-owned ports. These fail the build if a port signature drifts.
var (
	_ memory.AuthorityStore = (*MemoryCapability)(nil)

	_ scope.Store = (*ScopeCapability)(nil)

	_ feedback.Store = (*FeedbackCapability)(nil)

	_ gwretrieval.RecallStore = (*RecallCapability)(nil)

	_ extraction.EventStore = (*EventCapability)(nil)
	_ extraction.WorkStore  = (*ExtractionWorkCapability)(nil)

	_ policy.Store = (*PolicyCapability)(nil)

	_ consolidation.Store     = (*ConsolidationCapability)(nil)
	_ ConsolidationClassifier = (*ConsolidationClassifierBridge)(nil)

	_ knowledge.ReadStore   = (*KnowledgeReadCapability)(nil)
	_ knowledge.ReviewStore = (*KnowledgeReviewCapability)(nil)
)
