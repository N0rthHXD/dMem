package storage

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

const (
	graphFanOut       = 20
	graphMaxPaths     = 100
	graphMaxNeighbors = 100
	graphScoreCap     = 1.2
)

// graph-score-v3 weights. Edge scores multiply predicate intent, entity and fact
// confidence, evidence strength, temporal compatibility, and an inverse-degree
// hub penalty, so a generic hub cannot dominate merely by having many edges.
// Signals are aggregated per family with caps so duplicated authority evidence
// cannot inflate a candidate indefinitely.
const (
	weightDirectMention      = 1.0
	weightExactPredicateFact = 0.9
	weightOneHopFact         = 0.6
	weightMemoryRelation     = 0.4
	weightTwoHopPath         = 0.5

	graphHopDecay        = 0.7
	graphIntentFloor     = 0.6
	graphEvidenceBase    = 0.8
	graphEvidencePerItem = 0.1
)

// graphIntentWeight marks an edge predicate that matches the query plan's
// target predicate intent. Edges outside the intent remain traversable but are
// down-weighted instead of excluded.
func graphIntentWeight(predicate string, predicates []string) float64 {
	if len(predicates) == 0 {
		return 1
	}
	if contains(predicates, predicate) {
		return 1
	}
	return graphIntentFloor
}

func graphOrNeutral(value float64) float64 {
	if value <= 0 {
		return 1
	}
	return min(1, value)
}

func graphEvidenceStrength(evidenceCount int) float64 {
	if evidenceCount <= 0 {
		return graphOrNeutral(0)
	}
	return min(1, graphEvidenceBase+graphEvidencePerItem*float64(evidenceCount))
}

func graphHopDecayFor(hop int) float64 {
	if hop <= 1 {
		return 1
	}
	decay := 1.0
	for i := 1; i < hop; i++ {
		decay *= graphHopDecay
	}
	return decay
}

type graphRecallItem struct {
	memoryID, revisionID string
	signals              []domain.GraphSignal
	facts                []domain.EntityFact
	paths                []domain.GraphPath
	score                float64
	graphRank            int
}

// entityResolutionHints carries query-plan context that may disambiguate an
// alias span. EntityTypes is the closed set of types compatible with the query
// intent; when exactly one alternative matches, the mention is resolved without
// falling back to UUID order.
type entityResolutionHints struct {
	EntityTypes []string
}

// resolveQueryEntities performs deterministic query understanding: it finds
// the longest active alias matches inside a natural-language query and maps
// them to in-scope entities. Matching uses the stored normalized alias and
// respects word boundaries for non-CJK text. It never requires a provider.
func resolveQueryEntities(ctx context.Context, tx *sql.Tx, query string, spaces []string) ([]domain.ResolvedEntity, error) {
	return resolveQueryEntitiesWithHints(ctx, tx, query, spaces, entityResolutionHints{})
}

// resolveQueryEntitiesWithHints is resolveQueryEntities with optional query-plan
// hints. Ambiguous spans remain explicit alternatives with normalized
// probabilities and never resolve by UUID order.
func resolveQueryEntitiesWithHints(ctx context.Context, tx *sql.Tx, query string, spaces []string, hints entityResolutionHints) ([]domain.ResolvedEntity, error) {
	if len(spaces) == 0 || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.alias, a.normalized_alias, e.entity_id, e.entity_type FROM entity_aliases a JOIN entities e ON e.entity_id=a.entity_id WHERE e.space_id IN (`+placeholders(len(spaces))+`) AND e.state='active' AND a.state='active'`, appendSpaces(nil, spaces)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type aliasMatch struct {
		alias, normalized string
		entityID          string
		entityType        string
	}
	var candidates []aliasMatch
	for rows.Next() {
		var m aliasMatch
		if err := rows.Scan(&m.alias, &m.normalized, &m.entityID, &m.entityType); err != nil {
			return nil, err
		}
		if m.normalized != "" {
			candidates = append(candidates, m)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	// Mention count is a deterministic inverse-frequency prior used to
	// normalize alternative probabilities. It is never a UUID tie-break.
	mentionCount := map[string]int{}
	priorIDs := map[string]struct{}{}
	for _, c := range candidates {
		priorIDs[c.entityID] = struct{}{}
	}
	if len(priorIDs) > 0 {
		ids := make([]string, 0, len(priorIDs))
		for id := range priorIDs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		priorRows, err := tx.QueryContext(ctx, `SELECT entity_id, count(*) FROM entity_mentions WHERE entity_id IN (`+placeholders(len(ids))+`) GROUP BY entity_id`, appendStrings2(nil, ids)...)
		if err != nil {
			return nil, err
		}
		for priorRows.Next() {
			var id string
			var count int
			if err := priorRows.Scan(&id, &count); err != nil {
				priorRows.Close()
				return nil, err
			}
			mentionCount[id] = count
		}
		if err := priorRows.Err(); err != nil {
			priorRows.Close()
			return nil, err
		}
		priorRows.Close()
	}

	lowerQuery := strings.ToLower(query)
	runes := []rune(lowerQuery)
	lowerRunes := make([]rune, 0, len(runes))
	lowerRunes = append(lowerRunes, runes...)
	type span struct {
		start, end int
		entityID   string
		entityType string
		alias      string
	}
	var spans []span
	for _, c := range candidates {
		key := strings.ToLower(c.alias)
		if key == "" {
			continue
		}
		searchFrom := 0
		for {
			pos := strings.Index(lowerQuery[searchFrom:], key)
			if pos < 0 {
				break
			}
			abs := searchFrom + pos
			startRune := utf8Len(lowerQuery[:abs])
			endRune := startRune + utf8Len(key)
			if aliasBoundary(lowerRunes, startRune, endRune, key) {
				spans = append(spans, span{start: startRune, end: endRune, entityID: c.entityID, entityType: c.entityType, alias: c.alias})
			}
			searchFrom = abs + 1
			if searchFrom >= len(lowerQuery) {
				break
			}
		}
	}
	// Deterministic longest-match selection with ambiguity bookkeeping: the
	// same span resolving to multiple entities lowers confidence instead of
	// silently preferring a UUID order.
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		if spans[i].end != spans[j].end {
			return spans[i].end > spans[j].end
		}
		return spans[i].entityID < spans[j].entityID
	})
	type spanKey struct {
		start, end int
	}
	spansBySpan := map[spanKey][]span{}
	for _, s := range spans {
		key := spanKey{s.start, s.end}
		spansBySpan[key] = append(spansBySpan[key], s)
	}
	type chosenSpan struct {
		start, end int
		group      []span
	}
	var chosen []chosenSpan
	lastEnd := -1
	for _, s := range spans {
		if s.start < lastEnd {
			continue
		}
		group := spansBySpan[spanKey{s.start, s.end}]
		chosen = append(chosen, chosenSpan{start: s.start, end: s.end, group: group})
		lastEnd = s.end
	}
	result := make([]domain.ResolvedEntity, 0, len(chosen))
	seen := map[string]struct{}{}
	for _, cs := range chosen {
		type alternative struct{ id, typ string }
		var alternatives []alternative
		seenEntity := map[string]struct{}{}
		for _, s := range cs.group {
			if _, duplicate := seenEntity[s.entityID]; duplicate {
				continue
			}
			seenEntity[s.entityID] = struct{}{}
			alternatives = append(alternatives, alternative{s.entityID, s.entityType})
		}
		sort.Slice(alternatives, func(i, j int) bool { return alternatives[i].id < alternatives[j].id })
		alias := cs.group[0].alias
		if len(alternatives) > 1 {
			// Query-plan type hints may disambiguate when exactly one
			// alternative has a compatible type. This is deterministic type
			// evidence, never UUID order.
			if len(hints.EntityTypes) > 0 {
				var matched []alternative
				for _, alt := range alternatives {
					if contains(hints.EntityTypes, alt.typ) {
						matched = append(matched, alt)
					}
				}
				if len(matched) == 1 {
					alt := matched[0]
					key := alt.id + "\x00" + strconvItoa2(cs.start)
					if _, ok := seen[key]; !ok {
						seen[key] = struct{}{}
						result = append(result, domain.ResolvedEntity{EntityID: alt.id, EntityType: alt.typ, Alias: alias, Confidence: 0.85, MatchKind: "alias_type", StartRune: cs.start, EndRune: cs.end})
					}
					continue
				}
			}
			// An ambiguous alias is never resolved by UUID order. Emit every
			// alternative with a normalized probability and an ambiguity marker
			// so graph expansion stays disabled unless context disambiguates it.
			total := 0
			for _, alt := range alternatives {
				total += mentionCount[alt.id] + 1
			}
			sort.Slice(alternatives, func(i, j int) bool {
				left, right := mentionCount[alternatives[i].id], mentionCount[alternatives[j].id]
				if left != right {
					return left > right
				}
				return alternatives[i].id < alternatives[j].id
			})
			ids := make([]string, 0, len(alternatives))
			for _, alt := range alternatives {
				ids = append(ids, alt.id)
			}
			for _, alt := range alternatives {
				others := make([]string, 0, len(ids)-1)
				for _, id := range ids {
					if id != alt.id {
						others = append(others, id)
					}
				}
				key := alt.id + "\x00" + strconvItoa2(cs.start)
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				confidence := float64(mentionCount[alt.id]+1) / float64(total)
				result = append(result, domain.ResolvedEntity{EntityID: alt.id, EntityType: alt.typ, Alias: alias, Confidence: confidence, MatchKind: "alias_ambiguous", StartRune: cs.start, EndRune: cs.end, Ambiguous: true, Alternatives: others})
			}
			continue
		}
		alt := alternatives[0]
		key := alt.id + "\x00" + string(rune(cs.start))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, domain.ResolvedEntity{EntityID: alt.id, EntityType: alt.typ, Alias: alias, Confidence: 0.9, MatchKind: "alias", StartRune: cs.start, EndRune: cs.end})
	}
	return result, nil
}

// aliasBoundary rejects substring matches that run into surrounding
// latin word characters (e.g. "Ann" inside "annual"). CJK text has no word
// boundaries, so adjacent CJK runes never reject a match.
func aliasBoundary(runes []rune, start, end int, alias string) bool {
	aliasRunes := []rune(alias)
	if start > 0 && isLatinWordRune(runes[start-1]) && isLatinWordRune(aliasRunes[0]) {
		return false
	}
	if end < len(runes) && isLatinWordRune(runes[end]) && isLatinWordRune(aliasRunes[len(aliasRunes)-1]) {
		return false
	}
	return true
}

func isLatinWordRune(value rune) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_'
}

func appendSpaces(dst []any, spaces []string) []any {
	for _, s := range spaces {
		dst = append(dst, s)
	}
	return dst
}

func utf8Len(value string) int {
	return len([]rune(value))
}

type graphChannelResult struct {
	items            []graphRecallItem
	resolvedEntities []domain.ResolvedEntity
}

// graphRecallPolicy captures the query-local graph gate. allowNewCandidates is
// false for features_only and true only for direct or bounded_path. primary is
// the set of already-established primary candidate keys; relation-only and
// expanded candidates may never enter outside that set.
type graphRecallPolicy struct {
	Policy              retrieval.GraphPolicy
	MinEntityConfidence float64
	MinFactConfidence   float64
	AllowNewCandidates  bool
	Predicates          []string
	Primary             map[string]struct{}
}

// graphChannelRecall runs the graph retrieval channel. resolvedEntities must
// come from a single upfront query-understanding pass; seeds must be raw
// primary-channel candidates, never relation- or graph-expanded candidates.
// Ambiguous mentions are excluded from expansion.
func (r *Repository) graphChannelRecall(ctx context.Context, tx *sql.Tx, params SearchParams, spaces []string, policy graphRecallPolicy, resolvedEntities []domain.ResolvedEntity, seeds []string, limit int) (graphChannelResult, error) {
	result := graphChannelResult{resolvedEntities: resolvedEntities}
	byKey := map[string]*graphRecallItem{}
	predicates := policy.Predicates

	entityIDs := make([]string, 0, len(resolvedEntities))
	confByEntity := map[string]float64{}
	for _, re := range resolvedEntities {
		if re.Ambiguous || re.Confidence < policy.MinEntityConfidence {
			continue
		}
		if _, duplicate := confByEntity[re.EntityID]; duplicate {
			continue
		}
		confByEntity[re.EntityID] = re.Confidence
		entityIDs = append(entityIDs, re.EntityID)
	}
	// The rebuildable inverse-degree projection supplies the hub penalty. A
	// missing row is neutral so graph retrieval never fails closed to zero.
	hubs := r.entityHubPenalties(ctx, tx, entityIDs)

	// 0-hop: direct entity mention revisions. Mention confidence is persisted and
	// consumed, but graph-derived revisions never become relation seeds.
	if len(entityIDs) > 0 {
		eligible, eligibleArgs := eligibilitySQL(params, spaces)
		query := `SELECT em.memory_id, em.revision_id, em.entity_id, em.confidence, em.created_at FROM entity_mentions em JOIN memory_projections p ON p.memory_id=em.memory_id AND p.revision_id=em.revision_id JOIN memories m ON m.memory_id=em.memory_id AND m.current_revision_id=em.revision_id AND m.current_state=p.current_state WHERE em.entity_id IN (` + placeholders(len(entityIDs)) + `) AND em.memory_id IS NOT NULL AND coalesce(em.confidence,0.5)>=? AND p.searchable=1 AND p.space_id IN (` + placeholders(len(spaces)) + `)` + eligible + ` ORDER BY coalesce(em.confidence,0.5) DESC, p.updated_at DESC, em.memory_id ASC, em.revision_id ASC LIMIT ?`
		args := make([]any, 0, len(entityIDs)+len(spaces)+len(eligibleArgs)+1)
		args = append(args, appendStrings2(nil, entityIDs)...)
		args = append(args, policy.MinEntityConfidence)
		args = append(args, appendStrings2(nil, spaces)...)
		args = append(args, eligibleArgs...)
		args = append(args, limit)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return result, err
		}
		seenMention := map[string]struct{}{}
		for rows.Next() {
			var memoryID, revisionID, entityID string
			var mentionConfidence sql.NullFloat64
			var createdAt string
			if err := rows.Scan(&memoryID, &revisionID, &entityID, &mentionConfidence, &createdAt); err != nil {
				rows.Close()
				return result, err
			}
			if _, duplicate := seenMention[memoryID+"\x00"+revisionID+"\x00"+entityID]; duplicate {
				continue
			}
			seenMention[memoryID+"\x00"+revisionID+"\x00"+entityID] = struct{}{}
			item := ensureGraphItem(byKey, memoryID, revisionID)
			conf := confByEntity[entityID]
			if mentionConfidence.Valid {
				conf = min(conf, mentionConfidence.Float64)
			}
			item.signals = append(item.signals, domain.GraphSignal{Kind: "direct_entity_mention", EntityID: entityID, Hop: 0, EntityConfidence: conf, HubPenalty: hubs[entityID]})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return result, err
		}
		rows.Close()
	}

	// 1-hop typed entity facts: outgoing + incoming facts from resolved entities.
	if len(entityIDs) > 0 {
		if err := r.graphFactHop(ctx, tx, byKey, entityIDs, confByEntity, hubs, predicates, spaces, params, policy.MinFactConfidence, limit); err != nil {
			return result, err
		}
	}

	// 2-hop registry-approved heterogeneous paths. Only bounded_path intent
	// reaches this stage; every hop is filtered by predicate path position,
	// scope, state, current revision, valid time, and confidence before fan-out.
	if policy.Policy == retrieval.GraphBoundedPath && len(entityIDs) > 0 {
		if err := r.graphSecondHop(ctx, tx, byKey, entityIDs, confByEntity, hubs, predicates, spaces, params, policy.MinFactConfidence, limit); err != nil {
			return result, err
		}
	}

	// memory_relation expansion from primary seeds. Relations contribute
	// features to existing candidates; they never establish a candidate.
	unified := make([]string, 0, len(seeds))
	seenSeeds := map[string]struct{}{}
	for _, seed := range seeds {
		if seed == "" {
			continue
		}
		if _, duplicate := seenSeeds[seed]; duplicate {
			continue
		}
		seenSeeds[seed] = struct{}{}
		unified = append(unified, seed)
	}
	if len(unified) > 0 {
		if err := r.graphMemoryRelationRecall(ctx, tx, byKey, unified, spaces, params, limit); err != nil {
			return result, err
		}
	}

	items := make([]graphRecallItem, 0, len(byKey))
	for key, item := range byKey {
		item.score = graphAggregateScore(item.signals, item.paths)
		if item.score <= 0 {
			continue
		}
		relationOnly := graphRelationOnly(item.signals)
		if _, isPrimary := policy.Primary[key]; !isPrimary {
			if !policy.AllowNewCandidates || relationOnly {
				continue
			}
		}
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		if items[i].memoryID != items[j].memoryID {
			return items[i].memoryID < items[j].memoryID
		}
		return items[i].revisionID < items[j].revisionID
	})
	if len(items) > limit {
		items = items[:limit]
	}
	for i := range items {
		items[i].graphRank = i + 1
	}
	result.items = items
	return result, nil
}

func graphRelationOnly(signals []domain.GraphSignal) bool {
	hasRelation := false
	for _, signal := range signals {
		switch signal.Kind {
		case "memory_relation", "graph_conflict":
			hasRelation = true
		case "direct_entity_mention", "exact_predicate_fact", "one_hop_fact", "two_hop_path":
			return false
		}
	}
	return hasRelation
}

// graphAggregateScore implements graph-score-v3: per-family maxima over full
// edge-confidence products with small, capped cross-evidence bonuses. Every
// edge contributes predicate intent, entity and fact confidence, evidence
// strength, temporal compatibility, and the inverse-degree hub penalty.
func graphAggregateScore(signals []domain.GraphSignal, paths []domain.GraphPath) float64 {
	mentionBest, mentionExtra := 0.0, 0
	seenEntity := map[string]struct{}{}
	for _, s := range signals {
		if s.Kind != "direct_entity_mention" {
			continue
		}
		if _, duplicate := seenEntity[s.EntityID]; duplicate {
			continue
		}
		seenEntity[s.EntityID] = struct{}{}
		weight := weightDirectMention * clamp01(s.EntityConfidence) * graphOrNeutral(s.HubPenalty)
		switch {
		case weight > mentionBest:
			if mentionBest > 0 {
				mentionExtra++
			}
			mentionBest = weight
		case mentionBest > 0:
			mentionExtra++
		}
	}
	mentionScore := mentionBest + 0.1*float64(min(mentionExtra, 2))

	exactBest, oneBest := 0.0, 0.0
	seenFact := map[string]struct{}{}
	for _, s := range signals {
		if s.Kind != "exact_predicate_fact" && s.Kind != "one_hop_fact" {
			continue
		}
		if _, duplicate := seenFact[s.FactID]; duplicate {
			continue
		}
		seenFact[s.FactID] = struct{}{}
		weight := clamp01(s.FactConfidence) * clamp01(s.EntityConfidence) *
			graphOrNeutral(s.EvidenceStrength) * graphOrNeutral(s.TemporalCompatibility) *
			graphOrNeutral(s.HubPenalty) * graphOrNeutral(s.PredicateIntent)
		if s.Kind == "exact_predicate_fact" {
			weight *= weightExactPredicateFact
			if weight > exactBest {
				exactBest = weight
			}
		} else {
			weight *= weightOneHopFact
			if weight > oneBest {
				oneBest = weight
			}
		}
	}
	factScore := max(exactBest, oneBest)
	if exactBest > 0 && oneBest > 0 {
		factScore += 0.15 * min(exactBest, oneBest)
	}
	// Capped corroboration: independent facts add at most two small bonuses so
	// repeated evidence cannot increase score linearly with degree.
	factScore += 0.05 * float64(min(max(len(seenFact)-1, 0), 2))

	relationBest, conflictBest := 0.0, 0.0
	seenRelation := map[string]struct{}{}
	for _, s := range signals {
		if s.Kind != "memory_relation" && s.Kind != "graph_conflict" {
			continue
		}
		if _, duplicate := seenRelation[s.RelationID]; duplicate {
			continue
		}
		seenRelation[s.RelationID] = struct{}{}
		weight := weightMemoryRelation * clamp01(s.FactConfidence) * graphOrNeutral(s.EvidenceStrength)
		if s.Kind == "graph_conflict" {
			if weight > conflictBest {
				conflictBest = weight
			}
		} else if weight > relationBest {
			relationBest = weight
		}
	}
	relationScore := max(relationBest, conflictBest)
	if relationBest > 0 && conflictBest > 0 {
		relationScore += 0.1 * min(relationBest, conflictBest)
	}

	pathScore := 0.0
	seenPath := map[string]struct{}{}
	for _, path := range paths {
		if len(path.Edges) == 0 {
			continue
		}
		parts := make([]string, 0, len(path.Edges)+2)
		parts = append(parts, path.StartEntityID, path.EndEntityID)
		product := 1.0
		for _, edge := range path.Edges {
			parts = append(parts, edge.FactID)
			product *= graphOrNeutral(edge.PredicateIntent) * clamp01(edge.Confidence) *
				graphOrNeutral(edge.EvidenceStrength) * graphOrNeutral(edge.HubPenalty) *
				graphOrNeutral(edge.TemporalCompatibility)
		}
		key := strings.Join(parts, "\x00")
		if _, duplicate := seenPath[key]; duplicate {
			continue
		}
		seenPath[key] = struct{}{}
		weight := weightTwoHopPath * product * graphHopDecayFor(len(path.Edges))
		if weight > pathScore {
			pathScore = weight
		}
	}

	raw := mentionScore + factScore + relationScore + pathScore
	return min(1.0, raw/graphScoreCap)
}

// entityHubPenalties loads the rebuildable inverse-degree projection for the
// resolved entities. A missing row is neutral so an unavailable statistic does
// not zero a candidate.
func (r *Repository) entityHubPenalties(ctx context.Context, tx *sql.Tx, entityIDs []string) map[string]float64 {
	penalties := make(map[string]float64, len(entityIDs))
	if len(entityIDs) == 0 {
		return penalties
	}
	rows, err := tx.QueryContext(ctx, `SELECT entity_id, inverse_degree FROM entity_graph_statistics WHERE entity_id IN (`+placeholders(len(entityIDs))+`)`, appendStrings2(nil, entityIDs)...)
	if err != nil {
		return penalties
	}
	defer rows.Close()
	for rows.Next() {
		var entityID string
		var inverseDegree sql.NullFloat64
		if err := rows.Scan(&entityID, &inverseDegree); err != nil {
			return penalties
		}
		if inverseDegree.Valid && inverseDegree.Float64 > 0 && inverseDegree.Float64 < 1 {
			penalties[entityID] = inverseDegree.Float64
		}
	}
	return penalties
}

// eligibilitySQL builds the projection eligibility conditions (requested
// lifecycle states, memory types, and validity window) that every recall
// query must apply before its LIMIT.
func eligibilitySQL(params SearchParams, spaces []string) (string, []any) {
	sql := ` AND p.current_state IN (` + placeholders(len(params.States)) + `)`
	args := appendStrings2(nil, params.States)
	if params.MemoryTypesSet {
		sql += ` AND p.memory_type IN (` + placeholders(len(params.MemoryTypes)) + `)`
		args = appendStrings2(args, params.MemoryTypes)
	}
	sql += ` AND (p.valid_from IS NULL OR p.valid_from<=?) AND (p.valid_until IS NULL OR p.valid_until>?)`
	args = append(args, params.ReferenceTime, params.ReferenceTime)
	return sql, args
}

func ensureGraphItem(byKey map[string]*graphRecallItem, memoryID, revisionID string) *graphRecallItem {
	key := memoryID + "\x00" + revisionID
	item := byKey[key]
	if item == nil {
		item = &graphRecallItem{memoryID: memoryID, revisionID: revisionID}
		byKey[key] = item
	}
	return item
}

// graphFactHop performs query-plan-directed direct fact lookup: typed incoming
// and outgoing one-hop facts from the resolved entities. Predicate intent
// restricts the SQL window before LIMIT when present, and a registry path
// position check runs before the row contributes.
func (r *Repository) graphFactHop(ctx context.Context, tx *sql.Tx, byKey map[string]*graphRecallItem, entityIDs []string, confByEntity map[string]float64, hubs map[string]float64, predicates, spaces []string, params SearchParams, minFactConfidence float64, limit int) error {
	predFilter := ""
	var predArgs []string
	if len(predicates) > 0 {
		allowed := intersectStrings(domain.PredicatePathNames(domain.PredicatePathFirst), predicates)
		if len(allowed) == 0 {
			return nil
		}
		predFilter = ` AND f.predicate IN (` + placeholders(len(allowed)) + `)`
		predArgs = allowed
	}
	eligible, eligibleArgs := eligibilitySQL(params, spaces)
	query := `SELECT f.fact_id, f.subject_entity_id, f.predicate, f.object_entity_id, f.object_literal, f.confidence, f.source_memory_id, f.source_revision_id, f.valid_from, f.valid_until, (SELECT count(*) FROM entity_fact_evidence fe WHERE fe.fact_id=f.fact_id) FROM entity_facts f JOIN memory_projections p ON p.memory_id=f.source_memory_id AND p.revision_id=f.source_revision_id JOIN memories m ON m.memory_id=f.source_memory_id AND m.current_revision_id=f.source_revision_id AND m.current_state=p.current_state JOIN memory_revisions rv ON rv.memory_id=f.source_memory_id AND rv.revision_id=f.source_revision_id WHERE f.state='active' AND (f.subject_entity_id IN (` + placeholders(len(entityIDs)) + `) OR f.object_entity_id IN (` + placeholders(len(entityIDs)) + `))` + predFilter + ` AND coalesce(f.confidence, 0.5) >= ? AND p.searchable=1 AND p.space_id IN (` + placeholders(len(spaces)) + `)` + eligible + ` AND (f.valid_from IS NULL OR f.valid_from<=?) AND (f.valid_until IS NULL OR f.valid_until>?) ORDER BY coalesce(f.confidence, 0.5) DESC, rv.specificity DESC, p.updated_at DESC, f.fact_id ASC LIMIT ?`
	idArgs := appendStrings2(nil, entityIDs)
	args := make([]any, 0, len(entityIDs)*2+len(predArgs)+len(eligibleArgs)+len(spaces)+6)
	args = append(args, idArgs...)
	args = append(args, idArgs...)
	args = append(args, appendStrings2(nil, predArgs)...)
	args = append(args, minFactConfidence)
	args = append(args, appendStrings2(nil, spaces)...)
	args = append(args, eligibleArgs...)
	args = append(args, params.ReferenceTime, params.ReferenceTime)
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var factID, subjectID, predicate, sourceMemory, sourceRevision string
		var objectEntityID, objectLiteral, validFrom, validUntil sql.NullString
		var confidence sql.NullFloat64
		var evidenceCount int
		if err := rows.Scan(&factID, &subjectID, &predicate, &objectEntityID, &objectLiteral, &confidence, &sourceMemory, &sourceRevision, &validFrom, &validUntil, &evidenceCount); err != nil {
			return err
		}
		if _, ok := domain.LookupPredicate(predicate); !ok {
			continue
		}
		// The matched endpoint is whichever side of the fact is a resolved query
		// entity. It carries the entity-resolution and hub evidence.
		matched, direction := "", ""
		if _, ok := confByEntity[subjectID]; ok {
			matched, direction = subjectID, "outgoing"
		} else if objectEntityID.Valid {
			if _, ok := confByEntity[objectEntityID.String]; ok {
				matched, direction = objectEntityID.String, "incoming"
			}
		}
		if matched == "" {
			continue
		}
		entityConf := confByEntity[matched]
		conf := 0.5
		if confidence.Valid {
			conf = confidence.Float64
		}
		kind := "one_hop_fact"
		if len(predicates) > 0 && contains(predicates, predicate) {
			kind = "exact_predicate_fact"
		}
		item := ensureGraphItem(byKey, sourceMemory, sourceRevision)
		item.signals = append(item.signals, domain.GraphSignal{
			Kind: kind, EntityID: matched, Predicate: predicate, FactID: factID, Hop: 1,
			FactConfidence: conf, EntityConfidence: entityConf, EdgeDirection: direction,
			EvidenceStrength: graphEvidenceStrength(evidenceCount), TemporalCompatibility: factTemporalCompatibility(params, validFrom, validUntil),
			HubPenalty: hubPenaltyOf(hubs, matched), PredicateIntent: graphIntentWeight(predicate, predicates),
		})
		fact := domain.EntityFact{FactID: factID, SubjectEntityID: subjectID, Predicate: predicate, SourceMemoryID: sourceMemory, SourceRevisionID: sourceRevision, Confidence: &conf, State: "active"}
		if objectEntityID.Valid {
			fact.ObjectEntityID = &objectEntityID.String
		} else if objectLiteral.Valid {
			fact.ObjectLiteral = &objectLiteral.String
		}
		item.facts = append(item.facts, fact)
	}
	return rows.Err()
}

// traversalEdge is one registry-approved typed edge observed while expanding a
// bounded path. nearID is the endpoint on the already-visited side and farID is
// the endpoint that may continue the path.
type traversalEdge struct {
	factID, subjectID, objectID, predicate, sourceMemory, sourceRevision string
	confidence                                                           float64
	evidenceStrength, temporalCompatibility, hubPenalty, predicateIntent float64
	nearID, farID, direction                                             string
}

// traversalEdges loads one typed expansion window. Every pre-expansion filter
// (scope, state, current revision, valid time, predicate path position, and
// confidence) is applied in SQL before the bounded LIMIT.
func (r *Repository) traversalEdges(ctx context.Context, tx *sql.Tx, anchors, allowed, intent, spaces []string, params SearchParams, minFactConfidence float64, hubs map[string]float64, limit int) ([]traversalEdge, error) {
	if len(anchors) == 0 || len(allowed) == 0 {
		return nil, nil
	}
	eligible, eligibleArgs := eligibilitySQL(params, spaces)
	query := `SELECT f.fact_id, f.subject_entity_id, f.object_entity_id, f.predicate, f.confidence, f.source_memory_id, f.source_revision_id, f.valid_from, f.valid_until, (SELECT count(*) FROM entity_fact_evidence fe WHERE fe.fact_id=f.fact_id) FROM entity_facts f JOIN memory_projections p ON p.memory_id=f.source_memory_id AND p.revision_id=f.source_revision_id JOIN memories m ON m.memory_id=f.source_memory_id AND m.current_revision_id=f.source_revision_id AND m.current_state=p.current_state JOIN memory_revisions rv ON rv.memory_id=f.source_memory_id AND rv.revision_id=f.source_revision_id WHERE f.state='active' AND f.object_entity_id IS NOT NULL AND f.object_entity_id<>f.subject_entity_id AND f.predicate IN (` + placeholders(len(allowed)) + `) AND (f.subject_entity_id IN (` + placeholders(len(anchors)) + `) OR f.object_entity_id IN (` + placeholders(len(anchors)) + `)) AND coalesce(f.confidence, 0.5) >= ? AND p.searchable=1 AND p.space_id IN (` + placeholders(len(spaces)) + `)` + eligible + ` AND (f.valid_from IS NULL OR f.valid_from<=?) AND (f.valid_until IS NULL OR f.valid_until>?) ORDER BY coalesce(f.confidence, 0.5) DESC, rv.specificity DESC, p.updated_at DESC, f.fact_id ASC LIMIT ?`
	anchorArgs := appendStrings2(nil, anchors)
	args := make([]any, 0, len(allowed)+len(anchors)*2+len(spaces)+len(eligibleArgs)+6)
	args = append(args, appendStrings2(nil, allowed)...)
	args = append(args, anchorArgs...)
	args = append(args, anchorArgs...)
	args = append(args, minFactConfidence)
	args = append(args, appendStrings2(nil, spaces)...)
	args = append(args, eligibleArgs...)
	args = append(args, params.ReferenceTime, params.ReferenceTime)
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	anchorSet := make(map[string]struct{}, len(anchors))
	for _, id := range anchors {
		anchorSet[id] = struct{}{}
	}
	var edges []traversalEdge
	seen := map[string]struct{}{}
	for rows.Next() {
		var factID, subjectID, objectID, predicate, sourceMemory, sourceRevision string
		var confidence sql.NullFloat64
		var validFrom, validUntil sql.NullString
		var evidenceCount int
		if err := rows.Scan(&factID, &subjectID, &objectID, &predicate, &confidence, &sourceMemory, &sourceRevision, &validFrom, &validUntil, &evidenceCount); err != nil {
			return nil, err
		}
		conf := 0.5
		if confidence.Valid {
			conf = confidence.Float64
		}
		base := traversalEdge{
			factID: factID, subjectID: subjectID, objectID: objectID, predicate: predicate,
			sourceMemory: sourceMemory, sourceRevision: sourceRevision, confidence: conf,
			evidenceStrength: graphEvidenceStrength(evidenceCount), temporalCompatibility: factTemporalCompatibility(params, validFrom, validUntil),
			predicateIntent: graphIntentWeight(predicate, intent),
		}
		appendEdge := func(near, far, direction string) {
			key := near + "\x00" + direction + "\x00" + factID
			if _, duplicate := seen[key]; duplicate {
				return
			}
			seen[key] = struct{}{}
			edge := base
			edge.nearID, edge.farID, edge.direction = near, far, direction
			edge.hubPenalty = min(hubPenaltyOf(hubs, near), hubPenaltyOf(hubs, far))
			edges = append(edges, edge)
		}
		if _, ok := anchorSet[subjectID]; ok {
			appendEdge(subjectID, objectID, "outgoing")
		}
		if _, ok := anchorSet[objectID]; ok {
			appendEdge(objectID, subjectID, "incoming")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Bound fan-out per anchor. The SQL window is already confidence ordered, so
	// a stable sort by anchor preserves that order while capping each node.
	sort.SliceStable(edges, func(i, j int) bool { return edges[i].nearID < edges[j].nearID })
	countByNear := map[string]int{}
	bounded := edges[:0]
	for _, edge := range edges {
		if countByNear[edge.nearID] >= graphFanOut {
			continue
		}
		countByNear[edge.nearID]++
		bounded = append(bounded, edge)
	}
	return bounded, nil
}

// graphSecondHop composes registry-approved heterogeneous two-hop paths. The
// first and second hop may use different predicates, and each hop is checked
// against its allowed path position before expansion. Cycles never return to a
// resolved start entity.
func (r *Repository) graphSecondHop(ctx context.Context, tx *sql.Tx, byKey map[string]*graphRecallItem, startIDs []string, confByEntity map[string]float64, hubs map[string]float64, predicates, spaces []string, params SearchParams, minFactConfidence float64, limit int) error {
	firstAllowed := domain.PredicatePathNames(domain.PredicatePathFirst)
	secondAllowed := domain.PredicatePathNames(domain.PredicatePathSecond)
	if len(predicates) > 0 {
		firstAllowed = intersectStrings(firstAllowed, predicates)
		secondAllowed = intersectStrings(secondAllowed, predicates)
	}
	if len(firstAllowed) == 0 || len(secondAllowed) == 0 {
		return nil
	}
	// First hop: every typed edge touching a resolved entity, bounded by fan-out.
	firstEdges, err := r.traversalEdges(ctx, tx, startIDs, firstAllowed, predicates, spaces, params, minFactConfidence, hubs, 2*graphMaxNeighbors)
	if err != nil {
		return err
	}
	middleSet := map[string]struct{}{}
	for _, edge := range firstEdges {
		middleSet[edge.farID] = struct{}{}
	}
	if len(middleSet) == 0 {
		return nil
	}
	middleIDs := make([]string, 0, len(middleSet))
	for id := range middleSet {
		middleIDs = append(middleIDs, id)
	}
	sort.Strings(middleIDs)
	if len(middleIDs) > graphMaxNeighbors {
		middleIDs = middleIDs[:graphMaxNeighbors]
	}
	// Load hub penalties for the intermediate entities; a generic hub must not
	// dominate a bounded path merely by having many edges.
	middleHubs := r.entityHubPenalties(ctx, tx, middleIDs)
	for id, penalty := range hubs {
		if _, ok := middleHubs[id]; !ok {
			middleHubs[id] = penalty
		}
	}
	secondEdges, err := r.traversalEdges(ctx, tx, middleIDs, secondAllowed, predicates, spaces, params, minFactConfidence, middleHubs, 4*graphMaxPaths)
	if err != nil {
		return err
	}
	byMiddle := map[string][]traversalEdge{}
	for _, edge := range firstEdges {
		byMiddle[edge.farID] = append(byMiddle[edge.farID], edge)
	}
	startSet := make(map[string]struct{}, len(startIDs))
	for _, id := range startIDs {
		startSet[id] = struct{}{}
	}
	produced := 0
	seenPath := map[string]struct{}{}
	for _, second := range secondEdges {
		for _, first := range byMiddle[second.nearID] {
			if second.farID == first.nearID || second.farID == second.nearID {
				continue
			}
			if _, cycle := startSet[second.farID]; cycle {
				continue
			}
			key := first.factID + "\x00" + second.factID
			if _, duplicate := seenPath[key]; duplicate {
				continue
			}
			seenPath[key] = struct{}{}
			firstEdge := graphPathEdge(first)
			secondEdge := graphPathEdge(second)
			item := ensureGraphItem(byKey, second.sourceMemory, second.sourceRevision)
			item.signals = append(item.signals, domain.GraphSignal{
				Kind: "two_hop_path", EntityID: first.nearID, Predicate: second.predicate, FactID: second.factID, Hop: 2,
				FactConfidence:  first.confidence * second.confidence,
				PredicateIntent: max(graphOrNeutral(first.predicateIntent), graphOrNeutral(second.predicateIntent)),
				HubPenalty:      min(first.hubPenalty, second.hubPenalty),
			})
			item.paths = append(item.paths, domain.GraphPath{StartEntityID: first.nearID, Edges: []domain.GraphPathEdge{firstEdge, secondEdge}, EndEntityID: second.farID, Hop: 2})
			produced++
			if produced >= graphMaxPaths {
				return nil
			}
		}
	}
	return nil
}

func graphPathEdge(edge traversalEdge) domain.GraphPathEdge {
	return domain.GraphPathEdge{
		FactID: edge.factID, SubjectEntityID: edge.subjectID, Predicate: edge.predicate, ObjectEntityID: edge.objectID,
		Confidence: edge.confidence, SourceMemoryID: edge.sourceMemory, SourceRevisionID: edge.sourceRevision, Direction: edge.direction,
		EvidenceStrength: edge.evidenceStrength, TemporalCompatibility: edge.temporalCompatibility, HubPenalty: edge.hubPenalty, PredicateIntent: edge.predicateIntent,
	}
}

func hubPenaltyOf(hubs map[string]float64, entityID string) float64 {
	if penalty, ok := hubs[entityID]; ok && penalty > 0 && penalty < 1 {
		return penalty
	}
	return 1
}

// factTemporalCompatibility scores how well a fact's valid-time window matches
// the query time. Fixed facts without a window are fully compatible; when a
// query time exists, TemporalMatch supplies the interval score.
func factTemporalCompatibility(params SearchParams, validFrom, validUntil sql.NullString) float64 {
	if !params.QueryTimeSet {
		return 1
	}
	if !validFrom.Valid && !validUntil.Valid {
		return 1
	}
	interval := retrieval.TimeInterval{Set: true, Precision: "valid"}
	if validFrom.Valid {
		if value, err := time.Parse(time.RFC3339Nano, validFrom.String); err == nil {
			interval.Start = value
		}
	}
	if validUntil.Valid {
		if value, err := time.Parse(time.RFC3339Nano, validUntil.String); err == nil {
			interval.End = value
		}
	}
	if score, compatible := retrieval.TemporalMatch(params.QueryTime, interval); compatible {
		return score
	}
	return graphOrNeutral(0)
}

// intersectStrings returns the sorted, deduplicated intersection of two sets.
func intersectStrings(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, value := range right {
		rightSet[value] = struct{}{}
	}
	var out []string
	for _, value := range uniqueSortedStrings(left) {
		if _, ok := rightSet[value]; ok {
			out = append(out, value)
		}
	}
	return out
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (r *Repository) graphMemoryRelationRecall(ctx context.Context, tx *sql.Tx, byKey map[string]*graphRecallItem, seeds, spaces []string, params SearchParams, limit int) error {
	eligible, eligibleArgs := eligibilitySQL(params, spaces)
	query := `SELECT relation_id, relation_type, candidate_memory_id, candidate_revision_id, confidence, direction FROM (
		SELECT r.relation_id, r.relation_type, r.target_memory_id AS candidate_memory_id, r.target_revision_id AS candidate_revision_id, r.confidence, 'outgoing' AS direction
		FROM memory_relations r JOIN memory_projections p ON p.memory_id=r.target_memory_id AND p.revision_id=r.target_revision_id JOIN memories m ON m.memory_id=r.target_memory_id AND m.current_revision_id=r.target_revision_id AND m.current_state=p.current_state
		WHERE r.state='active' AND r.space_id IN (` + placeholders(len(spaces)) + `) AND r.source_revision_id IN (` + placeholders(len(seeds)) + `) AND p.searchable=1` + eligible + `
		UNION ALL
		SELECT r.relation_id, r.relation_type, r.source_memory_id AS candidate_memory_id, r.source_revision_id AS candidate_revision_id, r.confidence, 'incoming' AS direction
		FROM memory_relations r JOIN memory_projections p ON p.memory_id=r.source_memory_id AND p.revision_id=r.source_revision_id JOIN memories m ON m.memory_id=r.source_memory_id AND m.current_revision_id=r.source_revision_id AND m.current_state=p.current_state
		WHERE r.state='active' AND r.space_id IN (` + placeholders(len(spaces)) + `) AND r.target_revision_id IN (` + placeholders(len(seeds)) + `) AND p.searchable=1` + eligible + `
	) ORDER BY coalesce(confidence,0.5) DESC, relation_id ASC LIMIT ?`
	args := make([]any, 0, 2*(len(spaces)+len(seeds)+len(eligibleArgs))+1)
	args = append(args, appendStrings2(nil, spaces)...)
	args = append(args, appendStrings2(nil, seeds)...)
	args = append(args, eligibleArgs...)
	args = append(args, appendStrings2(nil, spaces)...)
	args = append(args, appendStrings2(nil, seeds)...)
	args = append(args, eligibleArgs...)
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var relationID, relationType, candidateMemory, candidateRevision, direction string
		var confidence sql.NullFloat64
		if err := rows.Scan(&relationID, &relationType, &candidateMemory, &candidateRevision, &confidence, &direction); err != nil {
			return err
		}
		conf := 0.5
		if confidence.Valid {
			conf = confidence.Float64
		}
		item := ensureGraphItem(byKey, candidateMemory, candidateRevision)
		kind := "memory_relation"
		if relationType == "contradicts" {
			kind = "graph_conflict"
		}
		item.signals = append(item.signals, domain.GraphSignal{Kind: kind, RelationID: relationID, RelationType: relationType, RelationDirection: direction, Hop: 0, FactConfidence: conf})
	}
	return rows.Err()
}

func appendStrings2(dst []any, values []string) []any {
	for _, v := range values {
		dst = append(dst, v)
	}
	return dst
}

func strconvItoa2(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	n := len(digits)
	for value > 0 {
		n--
		digits[n] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[n:])
}
