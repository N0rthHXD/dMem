package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/N0rthHXD/dMem/internal/domain"
)

var (
	ErrEntityProposalReviewed = errors.New("entity proposal has already been reviewed")
	ErrInvalidEntityProposal  = errors.New("invalid entity proposal")
	ErrFrozenRevision         = errors.New("memory is frozen")
)

const entityIndexName = "entity_recall"

type CreateEntityProposalParams struct {
	EventID, SpaceID, ProposalKind, EntityType, CanonicalName, Alias, NormalizationVersion string
	EntityID, MentionMemoryID, MentionRevisionID, MentionEventID                           *string
	StartOffset, EndOffset                                                                 *int
	MentionType, Sensitivity, ProcessorIdentity, ProcessingVersion                         string
	ProviderIdentity, ModelIdentity                                                        *string
}

type EntityProposalFilter struct {
	SpaceID, ReviewState, ProposalKind, Cursor string
	Limit                                      int
}

type ReviewEntityProposalParams struct {
	ReasonCode string
	Note       *string
}
type EntityFilter struct {
	SpaceID, State, EntityType, Cursor string
	Limit                              int
	IncludeMentions                    bool
}

const entityNormalizationVersion = "v1"
const entityNormalizationPolicy = "width-fold+case-fold+space-collapse+punct-strip"

// normalizeEntityName is the deterministic, versioned entity normalization
// policy: width-fold compatibility characters, case-fold, strip punctuation and
// symbols, and collapse whitespace. It never merges identities by itself;
// normalization collisions create merge proposals instead.
func normalizeEntityName(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	pendingSpace := false
	for _, r := range foldDiacritics(strings.ToLower(foldFullWidth(value))) {
		switch {
		case unicode.IsSpace(r):
			if builder.Len() > 0 {
				pendingSpace = true
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if pendingSpace {
				builder.WriteRune(' ')
				pendingSpace = false
			}
			builder.WriteRune(r)
		default:
			// Drop punctuation, symbols, and control characters.
		}
	}
	return strings.TrimSpace(builder.String())
}

// foldFullWidth maps full-width compatibility characters to their ASCII
// equivalents so that ＡＢＣ and ABC normalize identically.
func foldFullWidth(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		switch {
		case r == '\u3000':
			builder.WriteRune(' ')
		case r >= '\uFF01' && r <= '\uFF5E':
			builder.WriteRune(r - 0xFEE0)
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// foldDiacritics maps common precomposed Latin letters to their ASCII base so
// accented and unaccented spellings of a name normalize identically. The Go
// standard library has no Unicode normalization tables, so this conservative
// table covers the letters that appear in entity names rather than all of NFKC.
func foldDiacritics(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		if folded, ok := diacriticFolds[r]; ok {
			builder.WriteString(folded)
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

var diacriticFolds = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a",
	'ç': "c", 'č': "c", 'ć': "c",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ę': "e", 'ě': "e",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ı': "i",
	'ñ': "n", 'ń': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ů': "u",
	'ý': "y", 'ÿ': "y",
	'ß': "ss", 'æ': "ae", 'œ': "oe",
	'đ': "d", 'ł': "l", 'ř': "r", 'š': "s", 'ş': "s", 'ž': "z", 'ź': "z", 'ż': "z", 'ģ': "g",
}

// nonAuthoritativeAliasTerms are pronouns, generic nouns, and common terms that
// must never auto-confirm as entity aliases. They may still be proposed for
// review with lower confidence.
var nonAuthoritativeAliasTerms = map[string]struct{}{
	"he": {}, "she": {}, "they": {}, "them": {}, "it": {}, "its": {}, "this": {}, "that": {},
	"these": {}, "those": {}, "i": {}, "me": {}, "we": {}, "us": {}, "you": {}, "him": {}, "her": {},
	"his": {}, "their": {}, "the": {}, "a": {}, "an": {}, "person": {}, "people": {}, "someone": {},
	"anyone": {}, "everyone": {}, "company": {}, "organization": {}, "organisation": {}, "team": {},
	"group": {}, "thing": {}, "stuff": {}, "project": {}, "product": {}, "place": {}, "event": {},
	"concept": {}, "other": {}, "他": {}, "她": {}, "它": {}, "他们": {}, "这个": {}, "那个": {}, "人": {},
}

// aliasPublishable is the versioned alias publication policy. An alias may
// auto-confirm only when it is long enough, is not a pronoun or common term,
// matches the entity type's language expectations, and meets the confidence
// threshold. Short CJK aliases are allowed because they carry more information
// per rune than latin initials.
func aliasPublishable(alias, entityType string, confidence float64) bool {
	if !validEntityType(entityType) {
		return false
	}
	normalized := normalizeEntityName(alias)
	if normalized == "" {
		return false
	}
	if _, blocked := nonAuthoritativeAliasTerms[normalized]; blocked {
		return false
	}
	runes := []rune(normalized)
	minimum := 3
	if isCJKRunes(runes) {
		minimum = 2
	}
	if len(runes) < minimum {
		return false
	}
	if confidence < autoConfirmAliasConfidence {
		return false
	}
	return true
}

// entityPublishable applies the versioned entity publication policy: a valid
// closed type, a non-generic canonical name, sufficient length, and confidence.
func entityPublishable(name, entityType string, confidence float64) bool {
	if !validEntityType(entityType) {
		return false
	}
	normalized := normalizeEntityName(name)
	if normalized == "" {
		return false
	}
	if _, blocked := nonAuthoritativeAliasTerms[normalized]; blocked {
		return false
	}
	runes := []rune(normalized)
	minimum := 3
	if isCJKRunes(runes) {
		minimum = 2
	}
	if len(runes) < minimum {
		return false
	}
	return confidence >= autoConfirmEntityConfidence
}

func isCJKRunes(runes []rune) bool {
	for _, r := range runes {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			return true
		}
	}
	return false
}

func entityProposalHash(p CreateEntityProposalParams) string {
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (r *Repository) CreateEntityProposal(ctx context.Context, identity WriteIdentity, p CreateEntityProposalParams) (domain.EntityProposal, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.EntityProposal{}, false, err
	}
	defer tx.Rollback()
	if status, body, found, err := readIdempotency(ctx, tx, identity); err != nil {
		return domain.EntityProposal{}, false, err
	} else if found {
		var result domain.EntityProposal
		if status != 201 || json.Unmarshal([]byte(body), &result) != nil {
			return result, false, errors.New("invalid stored idempotency response")
		}
		return result, true, nil
	}
	if err := r.requireDiskWritable(); err != nil {
		return domain.EntityProposal{}, false, err
	}
	if err := validateEntityProposalSource(ctx, tx, p); err != nil {
		return domain.EntityProposal{}, false, err
	}
	hash := entityProposalHash(p)
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT proposal_id FROM entity_proposals WHERE event_id=? AND processing_version=? AND proposal_kind=? AND specification_hash=?`, p.EventID, p.ProcessingVersion, p.ProposalKind, hash).Scan(&existing)
	if err == nil {
		result, getErr := getEntityProposal(ctx, tx, existing)
		return result, true, getErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.EntityProposal{}, false, err
	}
	ids, err := r.newIDs(3)
	if err != nil {
		return domain.EntityProposal{}, false, err
	}
	now := formatTime(r.runtime.Now())
	_, err = tx.ExecContext(ctx, `INSERT INTO entity_proposals(proposal_id,event_id,space_id,proposal_kind,entity_id,entity_type,canonical_name,alias,normalization_version,mention_memory_id,mention_revision_id,mention_event_id,start_offset,end_offset,mention_type,sensitivity,processor_identity,provider_identity,model_identity,processing_version,specification_hash,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, ids[0], p.EventID, p.SpaceID, p.ProposalKind, p.EntityID, nullableString(p.EntityType), nullableString(strings.TrimSpace(p.CanonicalName)), nullableString(strings.TrimSpace(p.Alias)), nullableString(p.NormalizationVersion), p.MentionMemoryID, p.MentionRevisionID, p.MentionEventID, p.StartOffset, p.EndOffset, nullableString(p.MentionType), p.Sensitivity, p.ProcessorIdentity, p.ProviderIdentity, p.ModelIdentity, p.ProcessingVersion, hash, now)
	if err != nil {
		return domain.EntityProposal{}, false, fmt.Errorf("%w: %v", ErrInvalidEntityProposal, err)
	}
	result, err := getEntityProposal(ctx, tx, ids[0])
	if err != nil {
		return result, false, err
	}
	body, _ := json.Marshal(result)
	if err = writeIdempotency(ctx, tx, identity, ids[1], 201, body, now); err != nil {
		return result, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,created_at) VALUES (?,?,?,?,'create_entity_proposal','entity_proposal',?,'succeeded',?)`, ids[2], ids[1], identity.RequestID, identity.CallerID, ids[0], now); err != nil {
		return result, false, err
	}
	if err = tx.Commit(); err != nil {
		return result, false, err
	}
	return result, false, nil
}

func validateEntityProposalSource(ctx context.Context, tx *sql.Tx, p CreateEntityProposalParams) error {
	var eventSpace string
	if err := tx.QueryRowContext(ctx, `SELECT space_id FROM raw_events WHERE event_id=?`, p.EventID).Scan(&eventSpace); err != nil || eventSpace != p.SpaceID {
		return ErrInvalidEntityProposal
	}
	switch p.ProposalKind {
	case "entity":
		if p.EntityID != nil || p.CanonicalName == "" || !validEntityType(p.EntityType) {
			return ErrInvalidEntityProposal
		}
		if (p.Alias == "") != (p.NormalizationVersion == "") {
			return ErrInvalidEntityProposal
		}
		if p.MentionMemoryID != nil || p.MentionRevisionID != nil || p.MentionEventID != nil || p.StartOffset != nil || p.EndOffset != nil || p.MentionType != "" {
			if err := validateMentionSpecification(ctx, tx, p); err != nil {
				return err
			}
		}
	case "alias":
		if p.EntityID == nil || p.EntityType != "" || p.CanonicalName != "" || p.Alias == "" || p.NormalizationVersion == "" || p.MentionMemoryID != nil || p.MentionRevisionID != nil || p.MentionEventID != nil || p.StartOffset != nil || p.EndOffset != nil || p.MentionType != "" {
			return ErrInvalidEntityProposal
		}
		if !entityInSpace(ctx, tx, *p.EntityID, p.SpaceID) {
			return ErrInvalidEntityProposal
		}
	case "mention":
		if p.EntityID == nil || p.EntityType != "" || p.CanonicalName != "" || p.Alias != "" || p.NormalizationVersion != "" || !entityInSpace(ctx, tx, *p.EntityID, p.SpaceID) {
			return ErrInvalidEntityProposal
		}
		return validateMentionSpecification(ctx, tx, p)
	default:
		return ErrInvalidEntityProposal
	}
	return nil
}

func validateMentionSpecification(ctx context.Context, tx *sql.Tx, p CreateEntityProposalParams) error {
	if p.StartOffset == nil || p.EndOffset == nil || *p.StartOffset < 0 || *p.EndOffset <= *p.StartOffset || (p.MentionType != "explicit" && p.MentionType != "implicit") {
		return ErrInvalidEntityProposal
	}
	text, err := mentionSourceText(ctx, tx, p.SpaceID, p.MentionMemoryID, p.MentionRevisionID, p.MentionEventID, false)
	if err != nil || *p.EndOffset > utf8.RuneCountInString(text) {
		return ErrInvalidEntityProposal
	}
	return nil
}

func validEntityType(v string) bool {
	return contains([]string{"person", "organization", "place", "project", "product", "event", "concept", "other"}, v)
}
func entityInSpace(ctx context.Context, q candidateQueryer, entityID, spaceID string) bool {
	var n int
	return q.QueryRowContext(ctx, `SELECT 1 FROM entities WHERE entity_id=? AND space_id=?`, entityID, spaceID).Scan(&n) == nil
}

func mentionSourceText(ctx context.Context, q candidateQueryer, space string, memoryID, revisionID, eventID *string, requireCurrent bool) (string, error) {
	var text, sourceSpace string
	if memoryID != nil && revisionID != nil && eventID == nil {
		query := `SELECT r.display_content,m.space_id FROM memory_revisions r JOIN memories m ON m.memory_id=r.memory_id WHERE r.memory_id=? AND r.revision_id=?`
		if requireCurrent {
			query += ` AND m.current_revision_id=r.revision_id AND m.current_state IN ('active','contested')`
		}
		if err := q.QueryRowContext(ctx, query, *memoryID, *revisionID).Scan(&text, &sourceSpace); err != nil {
			return "", ErrInvalidEntityProposal
		}
	} else if eventID != nil && memoryID == nil && revisionID == nil {
		if err := q.QueryRowContext(ctx, `SELECT c.content_text,e.space_id FROM raw_events e JOIN raw_contents c ON c.content_id=e.content_id WHERE e.event_id=? AND c.content_kind IN ('text','json')`, *eventID).Scan(&text, &sourceSpace); err != nil {
			return "", ErrInvalidEntityProposal
		}
	} else {
		return "", ErrInvalidEntityProposal
	}
	if sourceSpace != space {
		return "", ErrInvalidEntityProposal
	}
	return text, nil
}

func (r *Repository) GetEntityProposal(ctx context.Context, id string) (domain.EntityProposal, error) {
	return getEntityProposal(ctx, r.store.reader, id)
}
func getEntityProposal(ctx context.Context, q candidateQueryer, id string) (domain.EntityProposal, error) {
	var p domain.EntityProposal
	err := q.QueryRowContext(ctx, `SELECT proposal_id,event_id,space_id,proposal_kind,entity_id,entity_type,canonical_name,alias,normalization_version,mention_memory_id,mention_revision_id,mention_event_id,start_offset,end_offset,mention_type,sensitivity,processor_identity,provider_identity,model_identity,processing_version,specification_hash,review_state,result_entity_id,result_canonical_alias_id,result_alias_id,result_mention_id,created_at,reviewed_at FROM entity_proposals WHERE proposal_id=?`, id).Scan(&p.ProposalID, &p.EventID, &p.SpaceID, &p.ProposalKind, &p.EntityID, &p.EntityType, &p.CanonicalName, &p.Alias, &p.NormalizationVersion, &p.MentionMemoryID, &p.MentionRevisionID, &p.MentionEventID, &p.StartOffset, &p.EndOffset, &p.MentionType, &p.Sensitivity, &p.ProcessorIdentity, &p.ProviderIdentity, &p.ModelIdentity, &p.ProcessingVersion, &p.SpecificationHash, &p.ReviewState, &p.ResultEntityID, &p.ResultCanonicalAliasID, &p.ResultAliasID, &p.ResultMentionID, &p.CreatedAt, &p.ReviewedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.Reviews = []domain.EntityProposalReview{}
	rows, err := q.QueryContext(ctx, `SELECT review_id,from_state,to_state,reason_code,note,reviewer_id,result_entity_id,result_canonical_alias_id,result_alias_id,result_mention_id,created_at FROM entity_proposal_reviews WHERE proposal_id=? ORDER BY created_at DESC,review_id DESC`, id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var v domain.EntityProposalReview
		if err := rows.Scan(&v.ReviewID, &v.FromState, &v.ToState, &v.ReasonCode, &v.Note, &v.ReviewerID, &v.ResultEntityID, &v.ResultCanonicalAliasID, &v.ResultAliasID, &v.ResultMentionID, &v.CreatedAt); err != nil {
			return p, err
		}
		p.Reviews = append(p.Reviews, v)
	}
	return p, rows.Err()
}

func (r *Repository) ListEntityProposals(ctx context.Context, f EntityProposalFilter) (domain.EntityProposalPage, error) {
	query, args := `SELECT proposal_id FROM entity_proposals WHERE space_id=?`, []any{f.SpaceID}
	if f.ReviewState != "" {
		query += ` AND review_state=?`
		args = append(args, f.ReviewState)
	}
	if f.ProposalKind != "" {
		query += ` AND proposal_kind=?`
		args = append(args, f.ProposalKind)
	}
	if f.Cursor != "" {
		query += ` AND proposal_id<?`
		args = append(args, f.Cursor)
	}
	query += ` ORDER BY proposal_id DESC LIMIT ?`
	args = append(args, f.Limit+1)
	rows, err := r.store.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.EntityProposalPage{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err != nil {
		return domain.EntityProposalPage{}, err
	}
	page := domain.EntityProposalPage{Items: []domain.EntityProposal{}}
	for _, id := range ids {
		p, e := r.GetEntityProposal(ctx, id)
		if e != nil {
			return page, e
		}
		page.Items = append(page.Items, p)
	}
	if len(page.Items) > f.Limit {
		next := page.Items[f.Limit-1].ProposalID
		page.NextCursor = &next
		page.Items = page.Items[:f.Limit]
	}
	return page, nil
}

func (r *Repository) ConfirmEntityProposal(ctx context.Context, identity WriteIdentity, id string, review ReviewEntityProposalParams) (domain.EntityProposal, bool, error) {
	return r.reviewEntityProposal(ctx, identity, id, review, true)
}
func (r *Repository) RejectEntityProposal(ctx context.Context, identity WriteIdentity, id string, review ReviewEntityProposalParams) (domain.EntityProposal, bool, error) {
	return r.reviewEntityProposal(ctx, identity, id, review, false)
}

func (r *Repository) reviewEntityProposal(ctx context.Context, identity WriteIdentity, id string, review ReviewEntityProposalParams, confirm bool) (domain.EntityProposal, bool, error) {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return domain.EntityProposal{}, false, err
	}
	defer tx.Rollback()
	statusCode := 200
	if confirm {
		statusCode = 201
	}
	if status, body, found, e := readIdempotency(ctx, tx, identity); e != nil {
		return domain.EntityProposal{}, false, e
	} else if found {
		var p domain.EntityProposal
		if status != statusCode || json.Unmarshal([]byte(body), &p) != nil {
			return p, false, errors.New("invalid stored idempotency response")
		}
		return p, true, nil
	}
	p, err := getEntityProposal(ctx, tx, id)
	if err != nil {
		return p, false, err
	}
	if p.ReviewState != "pending" {
		return p, false, ErrEntityProposalReviewed
	}
	if err = r.requireDiskWritable(); err != nil {
		return p, false, err
	}
	ids, err := r.newIDs(13)
	if err != nil {
		return p, false, err
	}
	operationID, reviewID, auditID := ids[0], ids[1], ids[2]
	now := formatTime(r.runtime.Now())
	var entityID, canonicalAliasID, aliasID, mentionID *string
	toState := "rejected"
	if confirm {
		toState = "confirmed"
		switch p.ProposalKind {
		case "entity":
			eid, aid := ids[3], ids[4]
			entityID, canonicalAliasID = &eid, &aid
			norm := normalizeEntityName(*p.CanonicalName)
			version := "v1"
			if _, err = tx.ExecContext(ctx, `INSERT INTO entities(entity_id,space_id,entity_type,canonical_name,normalized_name,normalization_version,state,sensitivity,created_by,processing_version,created_at,updated_at) VALUES (?,?,?,?,?,?,'active',?,?,?,?,?)`, eid, p.SpaceID, *p.EntityType, *p.CanonicalName, norm, version, p.Sensitivity, identity.CallerID, p.ProcessingVersion, now, now); err != nil {
				return p, false, err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO entity_events(entity_event_id,entity_id,operation_id,event_type,to_canonical_name,to_state,normalization_version,reason,created_by,created_at) VALUES (?,?,?,'created',?,'active',?,?,?,?)`, ids[5], eid, operationID, *p.CanonicalName, version, review.ReasonCode, identity.CallerID, now); err != nil {
				return p, false, err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO entity_aliases(alias_id,entity_id,alias,normalized_alias,normalization_version,source,state,created_by,created_at,updated_at) VALUES (?,?,?,?,?,'human_confirmation','active',?,?,?)`, aid, eid, *p.CanonicalName, norm, version, identity.CallerID, now, now); err != nil {
				return p, false, err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO entity_alias_events(alias_event_id,alias_id,operation_id,to_state,reason,created_by,created_at) VALUES (?,?,?,'active',?,?,?)`, ids[6], aid, operationID, review.ReasonCode, identity.CallerID, now); err != nil {
				return p, false, err
			}
			if p.Alias != nil {
				extraAliasID := ids[7]
				aliasID = &extraAliasID
				extraNorm := normalizeEntityName(*p.Alias)
				if _, err = tx.ExecContext(ctx, `INSERT INTO entity_aliases(alias_id,entity_id,alias,normalized_alias,normalization_version,source,state,created_by,created_at,updated_at) VALUES (?,?,?,?,?,'human_confirmation','active',?,?,?)`, extraAliasID, eid, *p.Alias, extraNorm, *p.NormalizationVersion, identity.CallerID, now, now); err != nil {
					return p, false, err
				}
				if _, err = tx.ExecContext(ctx, `INSERT INTO entity_alias_events(alias_event_id,alias_id,operation_id,to_state,reason,created_by,created_at) VALUES (?,?,?,'active',?,?,?)`, ids[8], extraAliasID, operationID, review.ReasonCode, identity.CallerID, now); err != nil {
					return p, false, err
				}
				var other string
				if e := tx.QueryRowContext(ctx, `SELECT a.entity_id FROM entity_aliases a JOIN entities e ON e.entity_id=a.entity_id WHERE e.space_id=? AND a.normalization_version=? AND a.normalized_alias=? AND a.state='active' AND a.entity_id<>? ORDER BY a.entity_id LIMIT 1`, p.SpaceID, *p.NormalizationVersion, extraNorm, eid).Scan(&other); e == nil {
					h := sha256.Sum256([]byte(other + "\x00" + eid + "\x00" + extraNorm))
					if _, err = tx.ExecContext(ctx, `INSERT INTO entity_merge_proposals(merge_proposal_id,space_id,source_entity_id,target_entity_id,reason,processing_version,specification_hash,created_by,created_at) VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, ids[9], p.SpaceID, eid, other, "alias_normalization_collision", p.ProcessingVersion, hex.EncodeToString(h[:]), identity.CallerID, now); err != nil {
						return p, false, err
					}
				}
			}
			if p.MentionRevisionID != nil || p.MentionEventID != nil {
				mid := ids[10]
				mentionID = &mid
				if err = insertConfirmedMention(ctx, tx, p, eid, mid, identity.CallerID, now); err != nil {
					return p, false, err
				}
			}
		case "alias":
			entityID = p.EntityID
			aid := ids[3]
			aliasID = &aid
			norm := normalizeEntityName(*p.Alias)
			if _, err = tx.ExecContext(ctx, `INSERT INTO entity_aliases(alias_id,entity_id,alias,normalized_alias,normalization_version,source,state,created_by,created_at,updated_at) VALUES (?,?,?,?,?,'human_confirmation','active',?,?,?)`, aid, *entityID, *p.Alias, norm, *p.NormalizationVersion, identity.CallerID, now, now); err != nil {
				return p, false, err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO entity_alias_events(alias_event_id,alias_id,operation_id,to_state,reason,created_by,created_at) VALUES (?,?,?,'active',?,?,?)`, ids[5], aid, operationID, review.ReasonCode, identity.CallerID, now); err != nil {
				return p, false, err
			}
			var other string
			if e := tx.QueryRowContext(ctx, `SELECT a.entity_id FROM entity_aliases a JOIN entities e ON e.entity_id=a.entity_id WHERE e.space_id=? AND a.normalization_version=? AND a.normalized_alias=? AND a.state='active' AND a.entity_id<>? ORDER BY a.entity_id LIMIT 1`, p.SpaceID, *p.NormalizationVersion, norm, *entityID).Scan(&other); e == nil {
				h := sha256.Sum256([]byte(other + "\x00" + *entityID + "\x00" + norm))
				_, err = tx.ExecContext(ctx, `INSERT INTO entity_merge_proposals(merge_proposal_id,space_id,source_entity_id,target_entity_id,reason,processing_version,specification_hash,created_by,created_at) VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, ids[6], p.SpaceID, *entityID, other, "alias_normalization_collision", p.ProcessingVersion, hex.EncodeToString(h[:]), identity.CallerID, now)
				if err != nil {
					return p, false, err
				}
			}
		case "mention":
			entityID = p.EntityID
			mid := ids[3]
			mentionID = &mid
			if err = insertConfirmedMention(ctx, tx, p, *entityID, mid, identity.CallerID, now); err != nil {
				return p, false, err
			}
		}
	}
	updated, err := tx.ExecContext(ctx, `UPDATE entity_proposals SET review_state=?,result_entity_id=?,result_canonical_alias_id=?,result_alias_id=?,result_mention_id=?,reviewed_at=? WHERE proposal_id=? AND review_state='pending'`, toState, entityID, canonicalAliasID, aliasID, mentionID, now, id)
	if err != nil {
		return p, false, err
	}
	if !oneRow(updated) {
		return p, false, ErrEntityProposalReviewed
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO entity_proposal_reviews(review_id,proposal_id,operation_id,from_state,to_state,reason_code,note,reviewer_id,result_entity_id,result_canonical_alias_id,result_alias_id,result_mention_id,created_at) VALUES (?,?,?,'pending',?,?,?,?,?,?,?,?,?)`, reviewID, id, operationID, toState, review.ReasonCode, review.Note, identity.CallerID, entityID, canonicalAliasID, aliasID, mentionID, now); err != nil {
		return p, false, err
	}
	if confirm {
		if _, err = enqueueEntityIndexJob(ctx, tx, ids[12], operationID, r.processingVersion, r.maxAttempts, now); err != nil {
			return p, false, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE entity_index_metadata SET state='stale',updated_at=? WHERE singleton=1`, now); err != nil {
			return p, false, err
		}
	}
	p, err = getEntityProposal(ctx, tx, id)
	if err != nil {
		return p, false, err
	}
	body, _ := json.Marshal(p)
	if err = writeIdempotency(ctx, tx, identity, operationID, statusCode, body, now); err != nil {
		return p, false, err
	}
	action := "reject_entity_proposal"
	if confirm {
		action = "confirm_entity_proposal"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(audit_id,operation_id,request_id,caller_id,action,target_type,target_id,outcome,reason,created_at) VALUES (?,?,?,?,?,'entity_proposal',?,'succeeded',?,?)`, auditID, operationID, identity.RequestID, identity.CallerID, action, id, review.ReasonCode, now); err != nil {
		return p, false, err
	}
	if err = tx.Commit(); err != nil {
		return p, false, err
	}
	return p, false, nil
}

func insertConfirmedMention(ctx context.Context, tx *sql.Tx, p domain.EntityProposal, entityID, mentionID, callerID, now string) error {
	text, err := mentionSourceText(ctx, tx, p.SpaceID, p.MentionMemoryID, p.MentionRevisionID, p.MentionEventID, false)
	if err != nil {
		return err
	}
	if p.MentionMemoryID != nil {
		var current string
		var frozen bool
		if err = tx.QueryRowContext(ctx, `SELECT current_revision_id,frozen FROM memories WHERE memory_id=? AND space_id=?`, *p.MentionMemoryID, p.SpaceID).Scan(&current, &frozen); err != nil || current != *p.MentionRevisionID {
			return ErrRevisionConflict
		}
		if frozen {
			return fmt.Errorf("%w: %w", ErrRevisionConflict, ErrFrozenRevision)
		}
	}
	runes := []rune(text)
	if *p.StartOffset < 0 || *p.EndOffset > len(runes) || *p.EndOffset <= *p.StartOffset {
		return ErrInvalidEntityProposal
	}
	surface := string(runes[*p.StartOffset:*p.EndOffset])
	_, err = tx.ExecContext(ctx, `INSERT INTO entity_mentions(mention_id,entity_id,memory_id,revision_id,event_id,start_offset,end_offset,mention_type,source,surface_text,created_by,processing_version,created_at) VALUES (?,?,?,?,?,?,?,?, 'human_confirmation',?,?,?,?)`, mentionID, entityID, p.MentionMemoryID, p.MentionRevisionID, p.MentionEventID, *p.StartOffset, *p.EndOffset, *p.MentionType, surface, callerID, p.ProcessingVersion, now)
	return err
}

func enqueueEntityIndexJob(ctx context.Context, tx *sql.Tx, jobID, operationID, version string, maxAttempts int, now string) (bool, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO jobs(job_id,operation_id,job_type,target_type,target_id,processing_version,status,max_attempts,next_run_at,created_at) VALUES (?,?,'rebuild_entity_index','index',?,?,'pending',?,?,?) ON CONFLICT DO NOTHING`, jobID, operationID, entityIndexName, version, maxAttempts, now, now)
	return oneRow(res), err
}

func (r *Repository) GetEntity(ctx context.Context, id string, mentions bool) (domain.Entity, error) {
	return getEntity(ctx, r.store.reader, id, mentions)
}

func (r *Repository) GetEntityInSpace(ctx context.Context, id, spaceID string, mentions bool) (domain.Entity, error) {
	entity, err := getEntity(ctx, r.store.reader, id, mentions)
	if err == nil && entity.SpaceID != spaceID {
		return domain.Entity{}, ErrNotFound
	}
	return entity, err
}

func getEntity(ctx context.Context, q candidateQueryer, id string, mentions bool) (domain.Entity, error) {
	var e domain.Entity
	err := q.QueryRowContext(ctx, `SELECT entity_id,space_id,entity_type,canonical_name,normalized_name,normalization_version,state,sensitivity,created_by,processing_version,created_at,updated_at FROM entities WHERE entity_id=?`, id).Scan(&e.EntityID, &e.SpaceID, &e.EntityType, &e.CanonicalName, &e.NormalizedName, &e.NormalizationVersion, &e.State, &e.Sensitivity, &e.CreatedBy, &e.ProcessingVersion, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	if err != nil {
		return e, err
	}
	e.Aliases = []domain.EntityAlias{}
	rows, err := q.QueryContext(ctx, `SELECT alias_id,entity_id,alias,normalized_alias,normalization_version,source,state,created_by,created_at FROM entity_aliases WHERE entity_id=? ORDER BY alias_id`, id)
	if err != nil {
		return e, err
	}
	for rows.Next() {
		var a domain.EntityAlias
		if err = rows.Scan(&a.AliasID, &a.EntityID, &a.Alias, &a.NormalizedAlias, &a.NormalizationVersion, &a.Source, &a.State, &a.CreatedBy, &a.CreatedAt); err != nil {
			rows.Close()
			return e, err
		}
		e.Aliases = append(e.Aliases, a)
	}
	rows.Close()
	if mentions {
		e.Mentions, err = listEntityMentions(ctx, q, id, 100)
	}
	return e, err
}
func listEntityMentions(ctx context.Context, q candidateQueryer, id string, limit int) ([]domain.EntityMention, error) {
	rows, err := q.QueryContext(ctx, `SELECT mention_id,entity_id,memory_id,revision_id,event_id,start_offset,end_offset,mention_type,source,surface_text,created_by,processing_version,created_at FROM entity_mentions WHERE entity_id=? ORDER BY created_at DESC,mention_id DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.EntityMention{}
	for rows.Next() {
		var m domain.EntityMention
		if err = rows.Scan(&m.MentionID, &m.EntityID, &m.MemoryID, &m.RevisionID, &m.EventID, &m.StartOffset, &m.EndOffset, &m.MentionType, &m.Source, &m.SurfaceText, &m.CreatedBy, &m.ProcessingVersion, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (r *Repository) ListEntities(ctx context.Context, f EntityFilter) (domain.EntityPage, error) {
	query, args := `SELECT entity_id FROM entities WHERE space_id=?`, []any{f.SpaceID}
	if f.State != "" {
		query += ` AND state=?`
		args = append(args, f.State)
	}
	if f.EntityType != "" {
		query += ` AND entity_type=?`
		args = append(args, f.EntityType)
	}
	if f.Cursor != "" {
		query += ` AND entity_id>?`
		args = append(args, f.Cursor)
	}
	query += ` ORDER BY entity_id LIMIT ?`
	args = append(args, f.Limit+1)
	rows, err := r.store.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.EntityPage{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	page := domain.EntityPage{Items: []domain.Entity{}}
	for _, id := range ids {
		e, x := r.GetEntity(ctx, id, f.IncludeMentions)
		if x != nil {
			return page, x
		}
		page.Items = append(page.Items, e)
	}
	if len(page.Items) > f.Limit {
		next := page.Items[f.Limit-1].EntityID
		page.NextCursor = &next
		page.Items = page.Items[:f.Limit]
	}
	return page, nil
}

func (r *Repository) EntityIndexHealth(ctx context.Context) (string, int64) {
	var state string
	var version int64
	if err := r.store.reader.QueryRowContext(ctx, `SELECT state,active_version FROM entity_index_metadata WHERE singleton=1`).Scan(&state, &version); err != nil {
		return "unavailable", 0
	}
	return state, version
}

func (r *Repository) RebuildEntityIndexClaimed(ctx context.Context, claim ClaimedJob) error {
	return r.rebuildEntityIndex(ctx, &claim)
}

func (r *Repository) RebuildEntityIndex(ctx context.Context) error {
	return r.rebuildEntityIndex(ctx, nil)
}

func (r *Repository) rebuildEntityIndex(ctx context.Context, claim *ClaimedJob) error {
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if claim != nil {
		if err = r.requireJobClaim(ctx, tx, *claim); err != nil {
			return err
		}
	}
	now := formatTime(r.runtime.Now())
	if _, err = tx.ExecContext(ctx, `UPDATE entity_index_metadata SET state='rebuilding',updated_at=? WHERE singleton=1`, now); err != nil {
		return err
	}
	var entities, mentions int
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM entities),(SELECT count(*) FROM entity_mentions)`).Scan(&entities, &mentions); err != nil {
		return err
	}
	if claim != nil {
		if err = r.requireJobClaim(ctx, tx, *claim); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE entity_index_metadata SET active_version=active_version+1,generation=generation+1,state='ready',source_entity_count=?,source_mention_count=?,updated_at=? WHERE singleton=1`, entities, mentions, now); err != nil {
		return err
	}
	return tx.Commit()
}
