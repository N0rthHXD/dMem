package storage

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
	"strings"

	"github.com/N0rthHXD/dMem/internal/domain"
	"github.com/N0rthHXD/dMem/internal/retrieval"
)

const (
	graphFanOut       = 20
	graphMaxPaths     = 100
	graphMaxNeighbors = 100
	graphScoreCap     = 1.2
)

// graph-score-v2 weights. Signals are aggregated per family with caps so that
// duplicated authority evidence cannot inflate a candidate indefinitely.
const (
	weightDirectMention      = 1.0
	weightExactPredicateFact = 0.9
	weightOneHopFact         = 0.6
	weightMemoryRelation     = 0.4
	weightTwoHopPath         = 0.5
)

type graphRecallItem struct {
	memoryID, revisionID string
	signals              []domain.GraphSignal
	facts                []domain.EntityFact
	paths                []domain.GraphPath
	score                float64
	graphRank            int
}

// resolveQueryEntities performs deterministic query understanding: it finds
// the longest active alias matches inside a natural-language query and maps
// them to in-scope entities. Matching uses the stored normalized alias and
// respects word boundaries for non-CJK text. It never requires a provider.
func resolveQueryEntities(ctx context.Context, tx *sql.Tx, query string, spaces []string) ([]domain.ResolvedEntity, error) {
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
			// An ambiguous alias is never resolved by UUID order. Emit every
			// alternative with an ambiguity marker so graph expansion is
			// disabled unless context disambiguates it.
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
				key := alt.id + "\x00" + string(rune(cs.start))
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				result = append(result, domain.ResolvedEntity{EntityID: alt.id, EntityType: alt.typ, Alias: alias, Confidence: 0.5, MatchKind: "alias_ambiguous", StartRune: cs.start, EndRune: cs.end, Ambiguous: true, Alternatives: others})
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
			item.signals = append(item.signals, domain.GraphSignal{Kind: "direct_entity_mention", EntityID: entityID, Hop: 0, EntityConfidence: conf})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return result, err
		}
		rows.Close()
	}

	// 1-hop entity facts: outgoing + incoming facts from resolved entities.
	if len(entityIDs) > 0 {
		if err := r.graphFactHop(ctx, tx, byKey, entityIDs, confByEntity, predicates, spaces, params, policy.MinFactConfidence, limit); err != nil {
			return result, err
		}
	}

	// 2-hop bounded paths, only when the plan explicitly permits bounded paths
	// and predicate intent is present.
	if policy.Policy == retrieval.GraphBoundedPath && len(entityIDs) > 0 && len(predicates) > 0 {
		if err := r.graphSecondHop(ctx, tx, byKey, entityIDs, predicates, spaces, params, policy.MinFactConfidence, limit); err != nil {
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

// graphAggregateScore implements graph-score-v2: per-family maxima with small
// cross-evidence bonuses, capped so repeated evidence cannot dominate.
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
		weight := weightDirectMention * s.EntityConfidence
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
		weight := weightOneHopFact * s.FactConfidence * s.EntityConfidence
		if s.Kind == "exact_predicate_fact" {
			weight = weightExactPredicateFact * s.FactConfidence * s.EntityConfidence
			if weight > exactBest {
				exactBest = weight
			}
		} else if weight > oneBest {
			oneBest = weight
		}
	}
	factScore := max(exactBest, oneBest)
	if exactBest > 0 && oneBest > 0 {
		factScore += 0.15 * min(exactBest, oneBest)
	}

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
		weight := weightMemoryRelation * s.FactConfidence
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
		parts := make([]string, 0, len(path.Edges)+2)
		parts = append(parts, path.StartEntityID, path.EndEntityID)
		confidence := 1.0
		for _, edge := range path.Edges {
			parts = append(parts, edge.FactID)
			confidence *= edge.Confidence
		}
		key := strings.Join(parts, "\x00")
		if _, duplicate := seenPath[key]; duplicate {
			continue
		}
		seenPath[key] = struct{}{}
		if weight := weightTwoHopPath * confidence; weight > pathScore {
			pathScore = weight
		}
	}

	raw := mentionScore + factScore + relationScore + pathScore
	return min(1.0, raw/graphScoreCap)
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

func (r *Repository) graphFactHop(ctx context.Context, tx *sql.Tx, byKey map[string]*graphRecallItem, entityIDs []string, confByEntity map[string]float64, predicates, spaces []string, params SearchParams, minFactConfidence float64, limit int) error {
	predFilter := ""
	if len(predicates) > 0 {
		predFilter = ` AND f.predicate IN (` + placeholders(len(predicates)) + `)`
	}
	eligible, eligibleArgs := eligibilitySQL(params, spaces)
	query := `SELECT f.fact_id, f.subject_entity_id, f.predicate, f.object_entity_id, f.object_literal, f.confidence, f.source_memory_id, f.source_revision_id FROM entity_facts f JOIN memory_projections p ON p.memory_id=f.source_memory_id AND p.revision_id=f.source_revision_id JOIN memories m ON m.memory_id=f.source_memory_id AND m.current_revision_id=f.source_revision_id AND m.current_state=p.current_state JOIN memory_revisions rv ON rv.memory_id=f.source_memory_id AND rv.revision_id=f.source_revision_id WHERE f.state='active' AND EXISTS (SELECT 1 FROM entity_fact_evidence fe WHERE fe.fact_id=f.fact_id) AND (f.subject_entity_id IN (` + placeholders(len(entityIDs)) + `) OR f.object_entity_id IN (` + placeholders(len(entityIDs)) + `))` + predFilter + ` AND coalesce(f.confidence, 0.5) >= ? AND p.searchable=1 AND p.space_id IN (` + placeholders(len(spaces)) + `)` + eligible + ` AND (f.valid_from IS NULL OR f.valid_from<=?) AND (f.valid_until IS NULL OR f.valid_until>?) ORDER BY (f.predicate IN (` + placeholders(max(1, len(predicates))) + `)) DESC, coalesce(f.confidence, 0.5) DESC, rv.specificity DESC, p.updated_at DESC, f.fact_id ASC LIMIT ?`
	idArgs := appendStrings2(nil, entityIDs)
	args := make([]any, 0, len(entityIDs)*2+len(predicates)*2+len(eligibleArgs)+len(spaces)+5)
	args = append(args, idArgs...)
	args = append(args, idArgs...)
	if len(predicates) > 0 {
		args = append(args, appendStrings2(nil, predicates)...)
	}
	args = append(args, minFactConfidence)
	args = append(args, appendStrings2(nil, spaces)...)
	args = append(args, eligibleArgs...)
	args = append(args, params.ReferenceTime, params.ReferenceTime)
	if len(predicates) > 0 {
		args = append(args, appendStrings2(nil, predicates)...)
	} else {
		args = append(args, "")
	}
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var factID, subjectID, predicate, sourceMemory, sourceRevision string
		var objectEntityID, objectLiteral sql.NullString
		var confidence sql.NullFloat64
		if err := rows.Scan(&factID, &subjectID, &predicate, &objectEntityID, &objectLiteral, &confidence, &sourceMemory, &sourceRevision); err != nil {
			return err
		}
		item := ensureGraphItem(byKey, sourceMemory, sourceRevision)
		conf := 0.5
		if confidence.Valid {
			conf = confidence.Float64
		}
		// The resolved confidence must come from whichever side of the fact
		// matched the query entities.
		entityConf := confByEntity[subjectID]
		if entityConf == 0 {
			entityConf = confByEntity[objectEntityID.String]
		}
		if entityConf == 0 {
			continue
		}
		kind := "one_hop_fact"
		if len(predicates) > 0 {
			kind = "exact_predicate_fact"
		}
		item.signals = append(item.signals, domain.GraphSignal{Kind: kind, EntityID: subjectID, Predicate: predicate, FactID: factID, Hop: 1, FactConfidence: conf, EntityConfidence: entityConf})
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

func (r *Repository) graphSecondHop(ctx context.Context, tx *sql.Tx, byKey map[string]*graphRecallItem, startIDs, predicates, spaces []string, params SearchParams, minFactConfidence float64, limit int) error {
	eligible, eligibleArgs := eligibilitySQL(params, spaces)
	hopQuery := func(subjects []string) string {
		return `SELECT fact_id, subject_entity_id, object_entity_id, predicate, confidence, source_memory_id, source_revision_id FROM (
			SELECT f.fact_id, f.subject_entity_id, f.object_entity_id, f.predicate, f.confidence, f.source_memory_id, f.source_revision_id,
				row_number() OVER (PARTITION BY f.subject_entity_id, f.predicate ORDER BY coalesce(f.confidence,0.5) DESC, p.updated_at DESC, f.fact_id ASC) AS rn
			FROM entity_facts f
			JOIN memory_projections p ON p.memory_id=f.source_memory_id AND p.revision_id=f.source_revision_id
			JOIN memories m ON m.memory_id=f.source_memory_id AND m.current_revision_id=f.source_revision_id AND m.current_state=p.current_state
			WHERE f.state='active' AND EXISTS (SELECT 1 FROM entity_fact_evidence fe WHERE fe.fact_id=f.fact_id) AND coalesce(f.confidence,0.5)>=? AND f.subject_entity_id IN (` + placeholders(len(subjects)) + `) AND f.object_entity_id IS NOT NULL AND f.object_entity_id<>f.subject_entity_id AND f.predicate IN (` + placeholders(len(predicates)) + `) AND p.searchable=1 AND p.space_id IN (` + placeholders(len(spaces)) + `)` + eligible + ` AND (f.valid_from IS NULL OR f.valid_from<=?) AND (f.valid_until IS NULL OR f.valid_until>?)
		) WHERE rn<=` + strconv.Itoa(graphFanOut) + ` ORDER BY confidence DESC, fact_id ASC LIMIT ?`
	}
	hopArgs := func(subjects []string) []any {
		args := make([]any, 0, len(subjects)*2+len(predicates)+len(spaces)+len(eligibleArgs)+5)
		args = append(args, minFactConfidence)
		args = append(args, appendStrings2(nil, subjects)...)
		args = append(args, appendStrings2(nil, predicates)...)
		args = append(args, appendStrings2(nil, spaces)...)
		args = append(args, eligibleArgs...)
		args = append(args, params.ReferenceTime, params.ReferenceTime)
		return args
	}
	// First hop: facts leaving the resolved entities, fan-out capped per
	// (subject, predicate) pair.
	rows, err := tx.QueryContext(ctx, hopQuery(startIDs), append(hopArgs(startIDs), 2*graphMaxNeighbors)...)
	if err != nil {
		return err
	}
	type hopEdge struct {
		factID, subjectID, objectID, predicate, sourceMemory, sourceRevision string
		confidence                                                           float64
	}
	var firstEdges []hopEdge
	neighbors := map[string]struct{}{}
	for rows.Next() {
		var edge hopEdge
		var confidence sql.NullFloat64
		if err := rows.Scan(&edge.factID, &edge.subjectID, &edge.objectID, &edge.predicate, &confidence, &edge.sourceMemory, &edge.sourceRevision); err != nil {
			rows.Close()
			return err
		}
		edge.confidence = 0.5
		if confidence.Valid {
			edge.confidence = confidence.Float64
		}
		firstEdges = append(firstEdges, edge)
		neighbors[edge.objectID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(neighbors) == 0 {
		return nil
	}
	neighborIDs := make([]string, 0, len(neighbors))
	for id := range neighbors {
		neighborIDs = append(neighborIDs, id)
	}
	sort.Strings(neighborIDs)
	if len(neighborIDs) > graphMaxNeighbors {
		neighborIDs = neighborIDs[:graphMaxNeighbors]
	}
	byNeighbor := map[string][]hopEdge{}
	for _, edge := range firstEdges {
		byNeighbor[edge.objectID] = append(byNeighbor[edge.objectID], edge)
	}
	// Second hop: facts leaving the neighbors. Cycles are excluded by never
	// returning to a resolved start entity.
	rows, err = tx.QueryContext(ctx, hopQuery(neighborIDs), append(hopArgs(neighborIDs), 2*graphMaxNeighbors)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	produced := 0
	seenPath := map[string]struct{}{}
	for rows.Next() {
		var edge hopEdge
		var confidence sql.NullFloat64
		if err := rows.Scan(&edge.factID, &edge.subjectID, &edge.objectID, &edge.predicate, &confidence, &edge.sourceMemory, &edge.sourceRevision); err != nil {
			return err
		}
		edge.confidence = 0.5
		if confidence.Valid {
			edge.confidence = confidence.Float64
		}
		if contains(startIDs, edge.objectID) {
			continue
		}
		for _, first := range byNeighbor[edge.subjectID] {
			if first.subjectID == edge.objectID {
				continue
			}
			key := strings.Join([]string{first.subjectID, first.factID, edge.factID}, "\x00")
			if _, duplicate := seenPath[key]; duplicate {
				continue
			}
			seenPath[key] = struct{}{}
			item := ensureGraphItem(byKey, edge.sourceMemory, edge.sourceRevision)
			combined := first.confidence * edge.confidence
			item.signals = append(item.signals, domain.GraphSignal{Kind: "two_hop_path", EntityID: first.subjectID, Predicate: edge.predicate, FactID: edge.factID, Hop: 2, FactConfidence: combined})
			item.paths = append(item.paths, domain.GraphPath{StartEntityID: first.subjectID, Edges: []domain.GraphPathEdge{
				{FactID: first.factID, SubjectEntityID: first.subjectID, Predicate: first.predicate, ObjectEntityID: first.objectID, Confidence: first.confidence, SourceMemoryID: first.sourceMemory, SourceRevisionID: first.sourceRevision},
				{FactID: edge.factID, SubjectEntityID: edge.subjectID, Predicate: edge.predicate, ObjectEntityID: edge.objectID, Confidence: edge.confidence, SourceMemoryID: edge.sourceMemory, SourceRevisionID: edge.sourceRevision},
			}, EndEntityID: edge.objectID, Hop: 2})
			produced++
			if produced >= graphMaxPaths {
				return nil
			}
		}
	}
	return rows.Err()
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
