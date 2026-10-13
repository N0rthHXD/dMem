package storage

import (
	"github.com/N0rthHXD/dMem/internal/gateway/extraction"
	"github.com/N0rthHXD/dMem/internal/gateway/policy"
	gwretrieval "github.com/N0rthHXD/dMem/internal/gateway/retrieval"
	"github.com/N0rthHXD/dMem/internal/gateway/review"
	"github.com/N0rthHXD/dMem/internal/gateway/scope"
)

// Compile-time assertions that the SQLite-backed capability adapters satisfy the
// gateway-consumer-owned ports. These fail the build if a port signature drifts.
//
// Only capabilities with a production consumer are listed. The Agent Tool Route
// owns memory commit, feedback append, and forget through
// gateway/agenttool; the Provider Route owns raw-event ingress and worker
// processing through gateway/extraction. The former direct memory authority,
// provider feedback append, knowledge review, and consolidation-entry adapters
// were removed in Phase 7 together with their consumerless gateway modules.
var (
	_ scope.Store = (*ScopeCapability)(nil)

	_ gwretrieval.ReferenceTimeSource = (*RecallCapability)(nil)
	_ gwretrieval.CapabilityInspector = (*RecallCapability)(nil)
	_ gwretrieval.CandidateStore      = (*RecallCapability)(nil)
	_ gwretrieval.TelemetrySink       = (*RecallCapability)(nil)

	_ extraction.EventStore = (*EventCapability)(nil)
	_ extraction.WorkStore  = (*ExtractionWorkCapability)(nil)

	_ policy.Store = (*PolicyCapability)(nil)

	_ review.CandidateStore = (*ReviewCapability)(nil)
)
