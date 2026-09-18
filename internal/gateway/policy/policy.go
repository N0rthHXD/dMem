// Package policy defines the protocol-independent authorization and disclosure
// contracts. It is named policy rather than privacy because it covers concrete
// authorization and disclosure responsibilities: authority writes and reads,
// recall, provider processing, embedding, semantic queries, evidence
// disclosure, and sensitive export.
package policy

import (
	"context"

	"github.com/N0rthHXD/dMem/internal/gateway"
)

// Capability is one policy-controlled decision point.
type Capability string

const (
	// CapabilityAuthorityWrite guards authoritative memory mutation.
	CapabilityAuthorityWrite Capability = "authority_write"
	// CapabilityAuthorityRead guards authoritative memory reads.
	CapabilityAuthorityRead Capability = "authority_read"
	// CapabilityRecall guards retrieval over a scope.
	CapabilityRecall Capability = "recall"
	// CapabilityProcessing guards provider-based extraction.
	CapabilityProcessing Capability = "processing"
	// CapabilityEmbedding guards provider-based embedding.
	CapabilityEmbedding Capability = "embedding"
	// CapabilitySemanticQuery guards semantic recall queries.
	CapabilitySemanticQuery Capability = "semantic_query"
	// CapabilityEvidenceDisclosure guards disclosure of evidence content.
	CapabilityEvidenceDisclosure Capability = "evidence_disclosure"
	// CapabilitySensitiveExport guards export of sensitive content.
	CapabilitySensitiveExport Capability = "sensitive_export"
)

// Valid reports whether the capability is a defined decision point.
func (c Capability) Valid() bool {
	switch c {
	case CapabilityAuthorityWrite, CapabilityAuthorityRead, CapabilityRecall,
		CapabilityProcessing, CapabilityEmbedding, CapabilitySemanticQuery,
		CapabilityEvidenceDisclosure, CapabilitySensitiveExport:
		return true
	default:
		return false
	}
}

// Authorizer decides whether a caller may exercise a capability in the given
// scopes. It never returns a storage error; adapters translate failures.
type Authorizer interface {
	Authorize(context.Context, gateway.CallContext, Request) (Decision, error)
}

// Request is one authorization question.
type Request struct {
	Capability Capability
	SpaceIDs   []string
}

// Decision is the resolved authorization outcome.
type Decision struct {
	Allowed          bool
	ReasonCode       string
	PolicyGeneration int
}

// DisclosureFilter removes items that must not cross a provider or candidate
// boundary. Filtering occurs before provider calls and before candidate
// admission, not as response redaction in a protocol adapter.
type DisclosureFilter interface {
	Filter(context.Context, gateway.CallContext, DisclosureRequest) (DisclosureResult, error)
}

// DisclosureRequest asks which items may be disclosed in one scope.
type DisclosureRequest struct {
	SpaceID string
	Items   []DisclosureItem
}

// DisclosureItem is one candidate for disclosure.
type DisclosureItem struct {
	MemoryID    string
	RevisionID  string
	Sensitivity string
}

// DisclosureDenial records one filtered item and the reason it was removed.
type DisclosureDenial struct {
	Item       DisclosureItem
	ReasonCode string
}

// DisclosureResult partitions the request into admitted and denied items.
type DisclosureResult struct {
	Allowed []DisclosureItem
	Denied  []DisclosureDenial
}
