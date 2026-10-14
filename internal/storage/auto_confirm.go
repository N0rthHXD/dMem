package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/N0rthHXD/dMem/internal/gateway/review"
)

func (r *Repository) autoConfirmCandidate(ctx context.Context, tx *sql.Tx, candidateID string) (bool, error) {
	policy := r.autoConfirmPolicy()
	if !policy.Valid() {
		return false, nil
	}
	candidate, evidence, err := r.loadCandidateForConfirmation(ctx, tx, candidateID)
	if err != nil {
		return false, err
	}
	if decision := policy.Screen(candidate, candidate.Revision); !decision.Eligible {
		return false, nil
	}
	prepared, err := r.prepareCandidateConfirmation(ctx, tx, candidate, evidence)
	if err != nil {
		return false, err
	}
	publishes, _ := policy.PublishesOutcome(prepared.outcome.Action, prepared.outcome.Classification)
	if !publishes {
		return false, nil
	}
	limited, err := r.autoConfirmRateLimited(ctx, tx, candidate.SpaceID, policy)
	if err != nil {
		return false, err
	}
	if limited {
		return false, nil
	}
	note := policy.ReviewNote(prepared.revision, prepared.outcome.Action, prepared.outcome.Similarity)
	version := policy.Version
	identity := WriteIdentity{
		CallerID:    review.AutoConfirmReviewerID,
		Route:       string(review.OperationCandidateConfirm),
		RequestHash: candidate.SpecificationHash,
	}
	if _, err := r.applyCandidateConfirmation(ctx, tx, identity, prepared, ReviewCandidateParams{
		ReasonCode:        review.AutoConfirmReasonCode,
		Note:              &note,
		AutoReviewVersion: &version,
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Repository) autoConfirmRateLimited(ctx context.Context, tx *sql.Tx, spaceID string, policy review.AutoConfirmPolicy) (bool, error) {
	if policy.MaxPerSpacePerHour <= 0 {
		return true, nil
	}
	window := formatTime(r.runtime.Now().Add(-time.Hour))
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM candidate_reviews cr JOIN candidates c ON c.candidate_id=cr.candidate_id WHERE c.space_id=? AND cr.reviewer_id=? AND cr.created_at>=?`,
		spaceID, review.AutoConfirmReviewerID, window).Scan(&count)
	if err != nil {
		return false, err
	}
	return count >= policy.MaxPerSpacePerHour, nil
}
