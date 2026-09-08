package storage

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/N0rthHXD/dMem/internal/domain"
)

const (
	graphMaxHops = 2
	graphFanOut  = 20
)

type graphRecallItem struct {
	memoryID, revisionID string
	signals              []domain.GraphSignal
	facts                []domain.EntityFact
	paths                []domain.GraphPath
	score                float64
	graphRank            int
}

// resolveQueryEntities performs deterministic query understanding: it finds the
// longest active alias or canonical-name matches inside a natural-language
// query and maps them to in-scope entities. It never requires a provider.
func resolveQueryEntities(ctx context.Context, tx *sql.Tx, query string, spaces []string) []domain.ResolvedEntity {
	if len(spaces) == 0 || strings.TrimSpace(query) == "" {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.alias, a.normalized_alias, e.entity_id, e.entity_type FROM entity_aliases a JOIN entities e ON e.entity_id=a.entity_id WHERE e.space_id IN (`+placeholders(len(spaces))+`) AND e.state='active' AND a.state='active'`, appendSpaces(nil, spaces)...)
	if err != nil {
		return nil
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
			return nil
		}
		candidates = append(candidates, m)
	}
	lowerQuery := strings.ToLower(query)
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
			spans = append(spans, span{start: startRune, end: endRune, entityID: c.entityID, entityType: c.entityType, alias: c.alias})
			searchFrom = abs + 1
			if searchFrom >= len(lowerQuery) {
				break
			}
		}
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		if spans[i].end-spans[i].start != spans[j].end-spans[j].start {
			return spans[i].end-spans[i].start > spans[j].end-spans[j].start
		}
		return spans[i].entityID < spans[j].entityID
	})
	var chosen []span
	lastEnd := -1
	for _, s := range spans {
		if s.start < lastEnd {
			continue
		}
		chosen = append(chosen, s)
		lastEnd = s.end
	}
	result := make([]domain.ResolvedEntity, 0, len(chosen))
	seen := map[string]struct{}{}
	for _, s := range chosen {
		key := s.entityID + "\x00" + string(rune(s.start))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, domain.ResolvedEntity{EntityID: s.entityID, EntityType: s.entityType, Alias: s.alias, Confidence: 0.9, MatchKind: "alias", StartRune: s.start, EndRune: s.end})
	}
	return result
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

// predicateIntent deterministically maps common query phrasings to controlled
// predicates. It returns an empty slice when no predicate intent is present.
func predicateIntent(query string) []string {
	lower := strings.ToLower(query)
	var out []string
	mapping := []struct {
		phrase    string
		predicate string
	}{
		{"participates in", "participates_in"}, {"participate in", "participates_in"},
		{"uses database", "uses_database"}, {"use database", "uses_database"}, {"database is", "uses_database"},
		{"owned by", "owned_by"}, {"owns", "owned_by"},
		{"located in", "located_in"}, {"based in", "located_in"},
		{"depends on", "depends_on"},
	}
	for _, m := range mapping {
		if strings.Contains(lower, m.phrase) {
			out = append(out, m.predicate)
		}
	}
	return out
}

type graphChannelResult struct {
	items            []graphRecallItem
	resolvedEntities []domain.ResolvedEntity
}

func (r *Repository) graphChannelRecall(ctx context.Context, tx *sql.Tx, params SearchParams, spaces []string, resolvedEntities []domain.ResolvedEntity, seeds []string, limit int) (graphChannelResult, error) {
	if len(resolvedEntities) == 0 {
		resolvedEntities = resolveQueryEntities(ctx, tx, params.Query, spaces)
	}
	result := graphChannelResult{resolvedEntities: resolvedEntities}
	byKey := map[string]*graphRecallItem{}
	entityIDs := make([]string, 0, len(resolvedEntities))
	for _, re := range resolvedEntities {
		entityIDs = append(entityIDs, re.EntityID)
	}
	predicates := predicateIntent(params.Query)

	// 0-hop: direct entity mention revisions.
	if len(entityIDs) > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT em.memory_id, em.revision_id, em.entity_id FROM entity_mentions em JOIN memories m ON m.memory_id=em.memory_id AND m.current_revision_id=em.revision_id JOIN memory_projections p ON p.memory_id=em.memory_id AND p.revision_id=em.revision_id WHERE em.entity_id IN (`+placeholders(len(entityIDs))+`) AND p.searchable=1 AND m.current_state IN ('active','contested') AND p.space_id IN (`+placeholders(len(spaces))+`) ORDER BY em.memory_id, em.revision_id LIMIT ?`, appendStrings2(appendStrings2(appendStrings2(nil, entityIDs), spaces), []string{strconvItoa2(limit)})...)
		if err != nil {
			return result, err
		}
		confByEntity := map[string]float64{}
		for _, re := range resolvedEntities {
			confByEntity[re.EntityID] = re.Confidence
		}
		for rows.Next() {
			var memoryID, revisionID, entityID string
			if err := rows.Scan(&memoryID, &revisionID, &entityID); err != nil {
				rows.Close()
				return result, err
			}
			item := ensureGraphItem(byKey, memoryID, revisionID)
			conf := confByEntity[entityID]
			item.signals = append(item.signals, domain.GraphSignal{Kind: "direct_entity_mention", EntityID: entityID, Hop: 0, EntityConfidence: conf})
			item.score += 1.0 * conf
		}
		rows.Close()
	}

	// 1-hop entity facts: outgoing + incoming facts from resolved entities.
	if len(entityIDs) > 0 {
		if err := r.graphFactHop(ctx, tx, byKey, entityIDs, predicates, spaces, limit, params.ReferenceTime); err != nil {
			return result, err
		}
	}

	// 2-hop bounded paths, only when predicate intent is present.
	if len(entityIDs) > 0 && len(predicates) > 0 {
		if err := r.graphSecondHop(ctx, tx, byKey, entityIDs, predicates, spaces, limit); err != nil {
			return result, err
		}
	}

	// memory_relation expansion from the unified seed set.
	if len(seeds) > 0 {
		if err := r.graphMemoryRelationRecall(ctx, tx, byKey, seeds, spaces, limit); err != nil {
			return result, err
		}
	}

	items := make([]graphRecallItem, 0, len(byKey))
	for _, item := range byKey {
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

func ensureGraphItem(byKey map[string]*graphRecallItem, memoryID, revisionID string) *graphRecallItem {
	key := memoryID + "\x00" + revisionID
	item := byKey[key]
	if item == nil {
		item = &graphRecallItem{memoryID: memoryID, revisionID: revisionID}
		byKey[key] = item
	}
	return item
}

func (r *Repository) graphFactHop(ctx context.Context, tx *sql.Tx, byKey map[string]*graphRecallItem, entityIDs, predicates, spaces []string, limit int, referenceTime string) error {
	predFilter := ""
	if len(predicates) > 0 {
		predFilter = ` AND f.predicate IN (` + placeholders(len(predicates)) + `)`
	}
	entityFilter := `(f.subject_entity_id IN (` + placeholders(len(entityIDs)) + `) OR f.object_entity_id IN (` + placeholders(len(entityIDs)) + `))`
	query := `SELECT f.fact_id, f.subject_entity_id, f.predicate, f.object_entity_id, f.object_literal, f.confidence, f.source_memory_id, f.source_revision_id FROM entity_facts f JOIN memories m ON m.memory_id=f.source_memory_id AND m.current_revision_id=f.source_revision_id JOIN memory_projections p ON p.memory_id=f.source_memory_id AND p.revision_id=f.source_revision_id WHERE f.state='active' AND ` + entityFilter + predFilter + ` AND p.searchable=1 AND m.current_state IN ('active','contested') AND p.space_id IN (` + placeholders(len(spaces)) + `) AND (f.valid_from IS NULL OR f.valid_from<=?) AND (f.valid_until IS NULL OR f.valid_until>?) ORDER BY f.fact_id LIMIT ?`
	args := make([]any, 0, len(entityIDs)*2+len(predicates)+len(spaces)+3)
	args = append(args, appendStrings2(nil, entityIDs)...)
	args = append(args, appendStrings2(nil, entityIDs)...)
	if len(predicates) > 0 {
		args = append(args, appendStrings2(nil, predicates)...)
	}
	args = append(args, appendStrings2(nil, spaces)...)
	args = append(args, referenceTime, referenceTime, limit)
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
		conf := 0.0
		if confidence.Valid {
			conf = confidence.Float64
		}
		entityConf := resolvedConf(entityIDs, subjectID)
		kind := "one_hop_fact"
		if len(predicates) > 0 {
			kind = "exact_predicate_fact"
		}
		item.signals = append(item.signals, domain.GraphSignal{Kind: kind, EntityID: subjectID, Predicate: predicate, FactID: factID, Hop: 1, FactConfidence: conf, EntityConfidence: entityConf})
		weight := 0.6 * conf * entityConf
		if len(predicates) > 0 {
			weight = 0.9 * conf * entityConf
		}
		item.score += weight
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

func resolvedConf(entityIDs []string, entityID string) float64 {
	for _, id := range entityIDs {
		if id == entityID {
			return 0.9
		}
	}
	return 0.5
}

func (r *Repository) graphSecondHop(ctx context.Context, tx *sql.Tx, byKey map[string]*graphRecallItem, entityIDs, predicates, spaces []string, limit int) error {
	neighbors := map[string]struct{}{}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT f.object_entity_id FROM entity_facts f JOIN memories m ON m.memory_id=f.source_memory_id AND m.current_revision_id=f.source_revision_id JOIN memory_projections p ON p.memory_id=f.source_memory_id AND p.revision_id=f.source_revision_id WHERE f.state='active' AND f.subject_entity_id IN (`+placeholders(len(entityIDs))+`) AND f.object_entity_id IS NOT NULL AND f.predicate IN (`+placeholders(len(predicates))+`) AND p.searchable=1 AND p.space_id IN (`+placeholders(len(spaces))+`) LIMIT ?`, appendStrings2(appendStrings2(appendStrings2(nil, entityIDs), predicates), append(append([]string{}, spaces...), strconvItoa2(graphFanOut)))...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		neighbors[id] = struct{}{}
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
	// second hop: facts whose subject is one of the neighbors, object is another entity.
	rows, err = tx.QueryContext(ctx, `SELECT f.fact_id, f.subject_entity_id, f.object_entity_id, f.predicate, f.confidence, f.source_memory_id, f.source_revision_id FROM entity_facts f JOIN memories m ON m.memory_id=f.source_memory_id AND m.current_revision_id=f.source_revision_id JOIN memory_projections p ON p.memory_id=f.source_memory_id AND p.revision_id=f.source_revision_id WHERE f.state='active' AND f.subject_entity_id IN (`+placeholders(len(neighborIDs))+`) AND f.object_entity_id IS NOT NULL AND f.predicate IN (`+placeholders(len(predicates))+`) AND p.searchable=1 AND p.space_id IN (`+placeholders(len(spaces))+`) ORDER BY f.fact_id LIMIT ?`, appendStrings2(appendStrings2(appendStrings2(nil, neighborIDs), predicates), append(append([]string{}, spaces...), strconvItoa2(limit)))...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var factID, subjectID, objectID, predicate, sourceMemory, sourceRevision string
		var confidence sql.NullFloat64
		if err := rows.Scan(&factID, &subjectID, &objectID, &predicate, &confidence, &sourceMemory, &sourceRevision); err != nil {
			return err
		}
		item := ensureGraphItem(byKey, sourceMemory, sourceRevision)
		conf := 0.0
		if confidence.Valid {
			conf = confidence.Float64
		}
		item.signals = append(item.signals, domain.GraphSignal{Kind: "two_hop_path", EntityID: subjectID, Predicate: predicate, FactID: factID, Hop: 2, FactConfidence: conf})
		item.score += 0.2 * conf
		item.paths = append(item.paths, domain.GraphPath{SubjectEntityID: subjectID, Steps: []string{predicate}, ObjectEntityID: objectID, Hop: 2})
	}
	return rows.Err()
}

func (r *Repository) graphMemoryRelationRecall(ctx context.Context, tx *sql.Tx, byKey map[string]*graphRecallItem, seeds, spaces []string, limit int) error {
	query := `SELECT relation_id, relation_type, candidate_memory_id, candidate_revision_id, confidence, direction FROM (
		SELECT r.relation_id, r.relation_type, r.target_memory_id AS candidate_memory_id, r.target_revision_id AS candidate_revision_id, r.confidence, 'outgoing' AS direction
		FROM memory_relations r JOIN memory_projections p ON p.memory_id=r.target_memory_id AND p.revision_id=r.target_revision_id
		WHERE r.state='active' AND r.space_id IN (` + placeholders(len(spaces)) + `) AND r.source_revision_id IN (` + placeholders(len(seeds)) + `) AND p.searchable=1
		UNION ALL
		SELECT r.relation_id, r.relation_type, r.source_memory_id AS candidate_memory_id, r.source_revision_id AS candidate_revision_id, r.confidence, 'incoming' AS direction
		FROM memory_relations r JOIN memory_projections p ON p.memory_id=r.source_memory_id AND p.revision_id=r.source_revision_id
		WHERE r.state='active' AND r.space_id IN (` + placeholders(len(spaces)) + `) AND r.target_revision_id IN (` + placeholders(len(seeds)) + `) AND p.searchable=1
	) ORDER BY relation_id LIMIT ?`
	args := make([]any, 0, len(spaces)+len(seeds)+len(spaces)+len(seeds)+1)
	args = append(args, appendStrings2(nil, spaces)...)
	args = append(args, appendStrings2(nil, seeds)...)
	args = append(args, appendStrings2(nil, spaces)...)
	args = append(args, appendStrings2(nil, seeds)...)
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
		conf := 0.0
		if confidence.Valid {
			conf = confidence.Float64
		}
		item := ensureGraphItem(byKey, candidateMemory, candidateRevision)
		kind := "memory_relation"
		if relationType == "contradicts" {
			kind = "graph_conflict"
		}
		item.signals = append(item.signals, domain.GraphSignal{Kind: kind, RelationID: relationID, RelationType: relationType, RelationDirection: direction, Hop: 0, FactConfidence: conf})
		item.score += 0.4 * conf
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
