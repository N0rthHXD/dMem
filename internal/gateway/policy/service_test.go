package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/N0rthHXD/dMem/internal/gateway"
)

type fakeStore struct {
	policies   map[string]EffectivePolicy
	errs       map[string]error
	calls      []string
	respectCtx bool
}

func (f *fakeStore) EffectivePolicy(ctx context.Context, capability Capability, spaceID string) (EffectivePolicy, error) {
	f.calls = append(f.calls, string(capability)+"/"+spaceID)
	if f.respectCtx {
		if err := ctx.Err(); err != nil {
			return EffectivePolicy{}, err
		}
	}
	key := string(capability) + "/" + spaceID
	if err := f.errs[key]; err != nil {
		return EffectivePolicy{}, err
	}
	policy, ok := f.policies[key]
	if !ok {
		return EffectivePolicy{}, ErrUnconfigured
	}
	return policy, nil
}

func policyCall() gateway.CallContext {
	return gateway.CallContext{RequestID: "request-1", Caller: gateway.CallerIdentity{SubjectID: "caller-1"}}
}

func TestAuthorizeUnconfiguredAllowsOfflineDefault(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)
	decision, err := service.Authorize(context.Background(), policyCall(), Request{Capability: CapabilityProcessing, SpaceIDs: []string{"space-1", "space-2"}})
	if err != nil || !decision.Allowed || decision.ReasonCode != ReasonDefaultAllow || decision.PolicyGeneration != 0 {
		t.Fatalf("Authorize() = %+v error = %v", decision, err)
	}
	if len(store.calls) != 2 {
		t.Fatalf("store calls = %v", store.calls)
	}
}

func TestAuthorizeExplicitDecisionWins(t *testing.T) {
	store := &fakeStore{policies: map[string]EffectivePolicy{
		"processing/space-1": {Capability: CapabilityProcessing, SpaceID: "space-1", Decision: "allow", ReasonCode: "space_assignment", PolicyGeneration: 3},
	}}
	service := NewService(store)
	decision, err := service.Authorize(context.Background(), policyCall(), Request{Capability: CapabilityProcessing, SpaceIDs: []string{"space-1"}})
	if err != nil || !decision.Allowed || decision.ReasonCode != ReasonAllowed || decision.PolicyGeneration != 3 {
		t.Fatalf("Authorize() = %+v error = %v", decision, err)
	}
}

func TestAuthorizeDeniesWhenAnyScopeDenies(t *testing.T) {
	store := &fakeStore{policies: map[string]EffectivePolicy{
		"embedding/space-1": {Capability: CapabilityEmbedding, SpaceID: "space-1", Decision: "allow", ReasonCode: "space_assignment", PolicyGeneration: 2},
		"embedding/space-2": {Capability: CapabilityEmbedding, SpaceID: "space-2", Decision: "deny", ReasonCode: "default_deny", PolicyGeneration: 2},
	}}
	service := NewService(store)
	decision, err := service.Authorize(context.Background(), policyCall(), Request{Capability: CapabilityEmbedding, SpaceIDs: []string{"space-1", "space-2"}})
	if err != nil || decision.Allowed || decision.ReasonCode != "default_deny" || decision.PolicyGeneration != 2 {
		t.Fatalf("Authorize() = %+v error = %v", decision, err)
	}
}

func TestAuthorizeValidatesRequest(t *testing.T) {
	service := NewService(&fakeStore{})
	cases := map[string]Request{
		"missing capability": {SpaceIDs: []string{"space-1"}},
		"unknown capability": {Capability: "unknown", SpaceIDs: []string{"space-1"}},
		"missing scope":      {Capability: CapabilityRecall},
		"blank scope":        {Capability: CapabilityRecall, SpaceIDs: []string{"  "}},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Authorize(context.Background(), policyCall(), request); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
				t.Fatalf("error = %v, want invalid_input", err)
			}
		})
	}
}

func TestAuthorizeClassifiesStoreFailures(t *testing.T) {
	store := &fakeStore{errs: map[string]error{"recall/space-1": errors.New("database unavailable")}}
	service := NewService(store)
	if _, err := service.Authorize(context.Background(), policyCall(), Request{Capability: CapabilityRecall, SpaceIDs: []string{"space-1"}}); !gateway.IsCode(err, gateway.ErrorInternal) {
		t.Fatalf("error = %v, want internal", err)
	}

	store.errs["recall/space-1"] = gateway.NewError(gateway.ErrorPermissionDenied, "denied by upstream")
	if _, err := service.Authorize(context.Background(), policyCall(), Request{Capability: CapabilityRecall, SpaceIDs: []string{"space-1"}}); !gateway.IsCode(err, gateway.ErrorPermissionDenied) {
		t.Fatalf("error = %v, want permission_denied passthrough", err)
	}
}

func TestAuthorizePropagatesCancellation(t *testing.T) {
	service := NewService(&fakeStore{respectCtx: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Authorize(ctx, policyCall(), Request{Capability: CapabilityRecall, SpaceIDs: []string{"space-1"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestFilterPartitionsDeniedItems(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)
	items := []DisclosureItem{
		{MemoryID: "memory-1", RevisionID: "revision-1", Sensitivity: "normal"},
		{MemoryID: "memory-2", RevisionID: "revision-2", Sensitivity: "sensitive"},
	}
	result, err := service.Filter(context.Background(), policyCall(), DisclosureRequest{SpaceID: "space-1", Items: items})
	if err != nil || len(result.Allowed) != 2 || len(result.Denied) != 0 {
		t.Fatalf("unconfigured Filter() = %+v error = %v", result, err)
	}

	store.policies = map[string]EffectivePolicy{
		"evidence_disclosure/space-1": {Capability: CapabilityEvidenceDisclosure, SpaceID: "space-1", Decision: "deny", ReasonCode: "space_assignment", PolicyGeneration: 1},
	}
	result, err = service.Filter(context.Background(), policyCall(), DisclosureRequest{SpaceID: "space-1", Items: items})
	if err != nil || len(result.Allowed) != 0 || len(result.Denied) != 2 {
		t.Fatalf("denied Filter() = %+v error = %v", result, err)
	}
	for _, denial := range result.Denied {
		if denial.ReasonCode != "space_assignment" {
			t.Fatalf("denial = %+v", denial)
		}
	}

	store.policies = map[string]EffectivePolicy{
		"sensitive_export/space-1": {Capability: CapabilitySensitiveExport, SpaceID: "space-1", Decision: "deny", ReasonCode: ReasonSensitiveDenied, PolicyGeneration: 1},
	}
	result, err = service.Filter(context.Background(), policyCall(), DisclosureRequest{SpaceID: "space-1", Items: items})
	if err != nil || len(result.Allowed) != 1 || len(result.Denied) != 1 {
		t.Fatalf("sensitive Filter() = %+v error = %v", result, err)
	}
	if result.Allowed[0].MemoryID != "memory-1" || result.Denied[0].Item.MemoryID != "memory-2" {
		t.Fatalf("sensitive partition = %+v", result)
	}
}

func TestFilterValidatesScope(t *testing.T) {
	service := NewService(&fakeStore{})
	if _, err := service.Filter(context.Background(), policyCall(), DisclosureRequest{}); !gateway.IsCode(err, gateway.ErrorInvalidInput) {
		t.Fatalf("error = %v, want invalid_input", err)
	}
}
