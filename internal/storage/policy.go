package storage

import (
	"context"
	"database/sql"
	"errors"
)

type EffectiveSpacePolicy struct {
	Capability, SpaceID, Decision, SourceAssignmentID, SourceSpaceID, ReasonCode string
	PolicyGeneration                                                             int
	InheritanceDepth                                                             *int
}

type policyAssignment struct {
	id, selector, spaceID, spaceType, externalID, effect string
	includeDescendants                                   bool
	priority                                             int
}

func (r *Repository) EffectivePolicy(ctx context.Context, capability, spaceID string) (EffectiveSpacePolicy, error) {
	state, err := r.currentCapabilityState(ctx, r.store.reader, capability)
	if err != nil {
		return EffectiveSpacePolicy{}, err
	}
	return r.resolveEffectivePolicy(ctx, r.store.reader, capability, spaceID, state.PolicyIdentity, state.PolicyGeneration)
}

func (r *Repository) RequireEffectivePolicy(ctx context.Context, capability, spaceID string) error {
	policy, err := r.EffectivePolicy(ctx, capability, spaceID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if policy.Decision != "allow" {
		return ErrPolicyDenied
	}
	return nil
}

func (r *Repository) RequireSemanticQueryPolicy(ctx context.Context, spaceID string) error {
	state, err := r.currentCapabilityState(ctx, r.store.reader, "embedding")
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !state.Enabled || state.PolicyIdentity == "" {
		return ErrPolicyDenied
	}
	var allowed int
	err = r.store.reader.QueryRowContext(ctx, `SELECT query_allowed FROM provider_policies WHERE capability='embedding' AND policy_identity=?`, state.PolicyIdentity).Scan(&allowed)
	if err != nil || allowed != 1 {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPolicyDenied
		}
		return err
	}
	return r.RequireEffectivePolicy(ctx, "embedding", spaceID)
}

func (r *Repository) authorizeSemanticQueryScope(ctx context.Context, q policyQuerier, spaceIDs []string) (int, error) {
	state, err := r.currentCapabilityState(ctx, q, "embedding")
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !state.Enabled || state.PolicyIdentity == "" || state.PolicyGeneration < 1 {
		return 0, ErrPolicyDenied
	}
	var allowed int
	if err := q.QueryRowContext(ctx, `SELECT query_allowed FROM provider_policies WHERE capability='embedding' AND policy_identity=?`, state.PolicyIdentity).Scan(&allowed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrPolicyDenied
		}
		return 0, err
	}
	if allowed != 1 {
		return 0, ErrPolicyDenied
	}
	for _, spaceID := range spaceIDs {
		policy, err := r.resolveEffectivePolicy(ctx, q, "embedding", spaceID, state.PolicyIdentity, state.PolicyGeneration)
		if err != nil {
			return 0, err
		}
		if policy.Decision != "allow" {
			return 0, ErrPolicyDenied
		}
	}
	return state.PolicyGeneration, nil
}

func (r *Repository) persistCurrentSpacePolicies(ctx context.Context, tx *sql.Tx, spaceID string) error {
	for _, capability := range []string{"processing", "embedding"} {
		state, err := r.currentCapabilityState(ctx, tx, capability)
		if errors.Is(err, ErrNotFound) || !state.Enabled {
			continue
		}
		if err != nil {
			return err
		}
		decision, err := r.resolveEffectivePolicy(ctx, tx, capability, spaceID, state.PolicyIdentity, state.PolicyGeneration)
		if err != nil {
			return err
		}
		if err := r.persistEffectivePolicy(ctx, tx, decision); err != nil {
			return err
		}
	}
	return nil
}

type policyQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (r *Repository) currentCapabilityState(ctx context.Context, q policyQuerier, capability string) (CapabilityStateRecord, error) {
	var state CapabilityStateRecord
	var enabled int
	err := q.QueryRowContext(ctx, `SELECT enabled, policy_identity, policy_generation FROM capability_states WHERE capability=?`, capability).Scan(&enabled, &state.PolicyIdentity, &state.PolicyGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return state, ErrNotFound
	}
	if err != nil {
		return state, err
	}
	state.Capability, state.Enabled = capability, enabled == 1
	return state, nil
}

func (r *Repository) resolveEffectivePolicy(ctx context.Context, q policyQuerier, capability, spaceID, identity string, generation int) (EffectiveSpacePolicy, error) {
	result := EffectiveSpacePolicy{Capability: capability, SpaceID: spaceID, PolicyGeneration: generation, Decision: "deny", ReasonCode: "default_deny"}
	if identity == "" || generation < 1 {
		return result, nil
	}
	spaces, err := policySpaceLineage(ctx, q, spaceID)
	if err != nil {
		return result, err
	}
	assignments, err := policyAssignments(ctx, q, capability, identity)
	if err != nil {
		return result, err
	}
	for depth, space := range spaces {
		candidates := make([]policyAssignment, 0)
		for _, assignment := range assignments {
			if assignment.selector == "space" && assignment.spaceID == space.id && (depth == 0 || assignment.includeDescendants) {
				candidates = append(candidates, assignment)
			}
		}
		if len(candidates) > 0 {
			return policyDecision(result, candidates, space.id, depth, "space_assignment"), nil
		}
	}
	current := spaces[0]
	candidates := make([]policyAssignment, 0)
	for _, assignment := range assignments {
		if assignment.selector == "space_type" && assignment.spaceType == current.spaceType || assignment.selector == "external_id" && assignment.externalID == current.externalID {
			candidates = append(candidates, assignment)
		}
	}
	if len(candidates) > 0 {
		return policyDecision(result, candidates, "", -1, "attribute_assignment"), nil
	}
	return result, nil
}

type policySpace struct{ id, spaceType, externalID string }

func policySpaceLineage(ctx context.Context, q policyQuerier, id string) ([]policySpace, error) {
	var result []policySpace
	for id != "" {
		var space policySpace
		var parent sql.NullString
		err := q.QueryRowContext(ctx, `SELECT space_id, parent_space_id, space_type, external_id FROM memory_spaces WHERE space_id=?`, id).Scan(&space.id, &parent, &space.spaceType, &space.externalID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		result = append(result, space)
		id = parent.String
	}
	return result, nil
}

func policyAssignments(ctx context.Context, q policyQuerier, capability, identity string) ([]policyAssignment, error) {
	rows, err := q.QueryContext(ctx, `SELECT assignment_id, selector_kind, space_id, space_type, external_id, effect, include_descendants, priority FROM policy_assignments WHERE capability=? AND policy_identity=?`, capability, identity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []policyAssignment
	for rows.Next() {
		var assignment policyAssignment
		var spaceID, spaceType, externalID sql.NullString
		var descendants int
		if err := rows.Scan(&assignment.id, &assignment.selector, &spaceID, &spaceType, &externalID, &assignment.effect, &descendants, &assignment.priority); err != nil {
			return nil, err
		}
		assignment.spaceID, assignment.spaceType, assignment.externalID, assignment.includeDescendants = spaceID.String, spaceType.String, externalID.String, descendants == 1
		result = append(result, assignment)
	}
	return result, rows.Err()
}

func policyDecision(result EffectiveSpacePolicy, candidates []policyAssignment, sourceSpace string, depth int, reason string) EffectiveSpacePolicy {
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.priority > best.priority || candidate.priority == best.priority && candidate.effect == "deny" && best.effect != "deny" || candidate.priority == best.priority && candidate.effect == best.effect && candidate.id < best.id {
			best = candidate
		}
	}
	result.Decision, result.SourceAssignmentID, result.SourceSpaceID, result.ReasonCode = best.effect, best.id, sourceSpace, reason
	if depth >= 0 {
		result.InheritanceDepth = &depth
	}
	return result
}

func (r *Repository) persistEffectivePolicy(ctx context.Context, tx *sql.Tx, policy EffectiveSpacePolicy) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO effective_space_policies(capability, space_id, policy_generation, decision, source_assignment_id, source_space_id, inheritance_depth, reason_code, evaluated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(capability, space_id, policy_generation) DO NOTHING`, policy.Capability, policy.SpaceID, policy.PolicyGeneration, policy.Decision, nullableString(policy.SourceAssignmentID), nullableString(policy.SourceSpaceID), policy.InheritanceDepth, policy.ReasonCode, formatTime(r.runtime.Now()))
	return err
}
