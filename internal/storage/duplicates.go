package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

const duplicateSemanticCosineThreshold = 0.94

type duplicateMember struct {
	memoryID      string
	revisionID    string
	spaceID       string
	state         string
	kind          string
	canonicalHash string
	validFrom     *time.Time
	validUntil    *time.Time
	evidenceCount int
	quality       float64
	evidenceKeys  []string
	aggregate     bool
}

// RebuildDuplicates recomputes deterministic duplicate clusters and their
// representative selection. Projections are rebuildable and never change
// eligibility.
func (r *Repository) RebuildDuplicates(ctx context.Context) error {
	return r.rebuildDuplicates(ctx, nil)
}

// RebuildDuplicatesClaimed fences a duplicate rebuild to a claimed maintenance
// job.
func (r *Repository) RebuildDuplicatesClaimed(ctx context.Context, claim ClaimedJob) error {
	return r.rebuildDuplicates(ctx, &claim)
}

func (r *Repository) rebuildDuplicates(ctx context.Context, claim *ClaimedJob) error {
	if claim != nil {
		if err := r.requireJobClaim(ctx, r.store.reader, *claim); err != nil {
			return err
		}
	}
	generation, err := r.projectionGeneration(ctx, "duplicate")
	if err != nil {
		return err
	}
	if err := r.setProjectionRebuilding(ctx, "duplicate"); err != nil {
		return err
	}
	members, err := r.duplicateMembers(ctx)
	if err != nil {
		r.markProjectionDegraded("duplicate")
		return err
	}
	index := map[string]int{}
	union := newUnionFind(len(members))
	for i := range members {
		index[members[i].revisionID] = i
	}
	r.unionCanonical(ctx, members, index, union)
	if err := r.unionSharedEvidence(ctx, members, index, union); err != nil {
		r.markProjectionDegraded("duplicate")
		return err
	}
	if err := r.unionAggregateProvenance(ctx, members, index, union); err != nil {
		r.markProjectionDegraded("duplicate")
		return err
	}
	if err := r.unionSemanticTimeCompatible(ctx, members, index, union); err != nil {
		r.markProjectionDegraded("duplicate")
		return err
	}

	groups := map[int][]int{}
	for i := range members {
		root := union.find(i)
		groups[root] = append(groups[root], i)
	}
	clusterIDs := make([]int, 0, len(groups))
	for root, ids := range groups {
		if len(ids) >= 2 {
			clusterIDs = append(clusterIDs, root)
		}
	}
	sort.Slice(clusterIDs, func(i, j int) bool {
		return duplicateSortKey(members, groups[clusterIDs[i]]) < duplicateSortKey(members, groups[clusterIDs[j]])
	})

	now := formatTime(r.runtime.Now())
	tx, err := r.store.writer.BeginTx(ctx, nil)
	if err != nil {
		r.markProjectionDegraded("duplicate")
		return err
	}
	defer tx.Rollback()
	if err := r.requireProjectionGeneration(ctx, tx, generation); err != nil {
		return err
	}
	if claim != nil {
		if err := r.requireJobClaim(ctx, tx, *claim); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM duplicate_cluster_members`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM duplicate_clusters`); err != nil {
		return err
	}
	for _, root := range clusterIDs {
		ids := groups[root]
		spaceID, clusterID := duplicateClusterID(members, ids)
		representative := selectDuplicateRepresentative(members, ids)
		if _, err := tx.ExecContext(ctx, `INSERT INTO duplicate_clusters(cluster_id,space_id,representative_memory_id,representative_revision_id,generation,member_count,algorithm_version,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			clusterID, spaceID, members[representative].memoryID, members[representative].revisionID, generation, len(ids), duplicateAlgorithmVersion, now, now); err != nil {
			return err
		}
		for _, id := range ids {
			reason := duplicateReason(members, ids, id)
			var similarity any
			if reason == "semantic_time_compatible" {
				similarity = duplicateSemanticCosineThreshold
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO duplicate_cluster_members(cluster_id,memory_id,revision_id,reason_code,similarity,created_at) VALUES (?,?,?,?,?,?)`,
				clusterID, members[id].memoryID, members[id].revisionID, reason, similarity, now); err != nil {
				return err
			}
		}
	}
	if err := r.setProjectionState(ctx, tx, "duplicate", "ready", now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) duplicateMembers(ctx context.Context) ([]duplicateMember, error) {
	rows, err := r.store.reader.QueryContext(ctx, `SELECT p.memory_id, p.revision_id, p.space_id, p.current_state, p.memory_type, rv.canonical_hash, rv.valid_from, rv.valid_until,
		(SELECT count(*) FROM memory_evidence me WHERE me.revision_id=p.revision_id),
		coalesce(q.quality, 0)
		FROM memory_projections p
		JOIN memory_revisions rv ON rv.memory_id=p.memory_id AND rv.revision_id=p.revision_id
		LEFT JOIN memory_quality q ON q.memory_id=p.memory_id AND q.revision_id=p.revision_id
		WHERE p.searchable=1
		ORDER BY p.memory_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []duplicateMember
	revisionIndex := map[string]int{}
	for rows.Next() {
		var item duplicateMember
		var validFrom, validUntil sql.NullString
		if err := rows.Scan(&item.memoryID, &item.revisionID, &item.spaceID, &item.state, &item.kind, &item.canonicalHash, &validFrom, &validUntil, &item.evidenceCount, &item.quality); err != nil {
			return nil, err
		}
		if item.validFrom, err = parseTimePointer(validFrom); err != nil {
			return nil, err
		}
		if item.validUntil, err = parseTimePointer(validUntil); err != nil {
			return nil, err
		}
		revisionIndex[item.revisionID] = len(members)
		members = append(members, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := r.loadDuplicateEvidence(ctx, members, revisionIndex); err != nil {
		return nil, err
	}
	if err := r.loadDuplicateAggregateFlags(ctx, members, revisionIndex); err != nil {
		return nil, err
	}
	return members, nil
}

func (r *Repository) loadDuplicateEvidence(ctx context.Context, members []duplicateMember, index map[string]int) error {
	rows, err := r.store.reader.QueryContext(ctx, `SELECT revision_id, event_id, start_offset, end_offset FROM memory_evidence WHERE role='direct' ORDER BY revision_id, event_id, start_offset`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var revisionID, eventID string
		var start, end sql.NullInt64
		if err := rows.Scan(&revisionID, &eventID, &start, &end); err != nil {
			return err
		}
		id, ok := index[revisionID]
		if !ok {
			continue
		}
		members[id].evidenceKeys = append(members[id].evidenceKeys, eventID+"\x00"+nullIntString(start)+"\x00"+nullIntString(end))
	}
	return rows.Err()
}

func (r *Repository) loadDuplicateAggregateFlags(ctx context.Context, members []duplicateMember, index map[string]int) error {
	rows, err := r.store.reader.QueryContext(ctx, `SELECT result_memory_id FROM aggregate_proposals WHERE review_state='confirmed' AND result_memory_id IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	aggregate := map[string]bool{}
	for rows.Next() {
		var memoryID string
		if err := rows.Scan(&memoryID); err != nil {
			return err
		}
		aggregate[memoryID] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = r.store.reader.QueryContext(ctx, `SELECT memory_id FROM aggregate_descriptors`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var memoryID string
		if err := rows.Scan(&memoryID); err != nil {
			return err
		}
		aggregate[memoryID] = true
	}
	for i := range members {
		members[i].aggregate = aggregate[members[i].memoryID]
	}
	return rows.Err()
}

func (r *Repository) unionCanonical(ctx context.Context, members []duplicateMember, index map[string]int, union *unionFind) {
	groups := map[string]int{}
	for i := range members {
		key := members[i].spaceID + "\x00" + members[i].kind + "\x00" + members[i].canonicalHash
		if root, ok := groups[key]; ok {
			union.union(root, i)
		} else {
			groups[key] = i
		}
	}
}

func (r *Repository) unionSharedEvidence(ctx context.Context, members []duplicateMember, index map[string]int, union *unionFind) error {
	rows, err := r.store.reader.QueryContext(ctx, `SELECT p.space_id, me.event_id, me.start_offset, me.end_offset, me.revision_id
		FROM memory_evidence me
		JOIN memory_revisions rv ON rv.revision_id=me.revision_id
		JOIN memory_projections p ON p.memory_id=rv.memory_id AND p.revision_id=rv.revision_id
		WHERE me.role='direct' AND p.searchable=1
		ORDER BY p.space_id, me.event_id, me.start_offset`)
	if err != nil {
		return err
	}
	defer rows.Close()
	groups := map[string]int{}
	for rows.Next() {
		var spaceID, eventID, revisionID string
		var start, end sql.NullInt64
		if err := rows.Scan(&spaceID, &eventID, &start, &end, &revisionID); err != nil {
			return err
		}
		id, ok := index[revisionID]
		if !ok {
			continue
		}
		key := spaceID + "\x00" + eventID + "\x00" + nullIntString(start) + "\x00" + nullIntString(end)
		if root, ok := groups[key]; ok {
			union.union(root, id)
		} else {
			groups[key] = id
		}
	}
	return rows.Err()
}

func (r *Repository) unionAggregateProvenance(ctx context.Context, members []duplicateMember, index map[string]int, union *unionFind) error {
	rows, err := r.store.reader.QueryContext(ctx, `SELECT ap.result_memory_id, api.memory_id
		FROM aggregate_proposals ap
		JOIN aggregate_proposal_inputs api ON api.proposal_id=ap.proposal_id
		WHERE ap.review_state='confirmed' AND ap.result_memory_id IS NOT NULL
		ORDER BY ap.result_memory_id, api.memory_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var aggregateMemory, inputMemory string
		if err := rows.Scan(&aggregateMemory, &inputMemory); err != nil {
			return err
		}
		left, okLeft := index[currentRevisionForMemory(members, aggregateMemory)]
		right, okRight := index[currentRevisionForMemory(members, inputMemory)]
		if okLeft && okRight {
			union.union(left, right)
		}
	}
	return rows.Err()
}

func (r *Repository) unionSemanticTimeCompatible(ctx context.Context, members []duplicateMember, index map[string]int, union *unionFind) error {
	rows, err := r.store.reader.QueryContext(ctx, `SELECT a.memory_id, b.memory_id
		FROM memory_embeddings a
		JOIN memory_embeddings b ON a.memory_id < b.memory_id AND a.compatibility_identity=b.compatibility_identity AND a.dimension=b.dimension
		JOIN memory_projections pa ON pa.memory_id=a.memory_id AND pa.revision_id=a.revision_id
		JOIN memory_projections pb ON pb.memory_id=b.memory_id AND pb.revision_id=b.revision_id
		WHERE pa.space_id=pb.space_id AND pa.memory_type=pb.memory_type
		  AND pa.embedding_state='current' AND pb.embedding_state='current'
		  AND a.content_hash=pa.content_hash AND b.content_hash=pb.content_hash
		  AND vec_distance_cosine(a.vector_blob, b.vector_blob) <= ?
		ORDER BY a.memory_id, b.memory_id
		LIMIT 4096`, 1-duplicateSemanticCosineThreshold)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var leftMemory, rightMemory string
		if err := rows.Scan(&leftMemory, &rightMemory); err != nil {
			return err
		}
		left, okLeft := index[currentRevisionForMemory(members, leftMemory)]
		right, okRight := index[currentRevisionForMemory(members, rightMemory)]
		if !okLeft || !okRight {
			continue
		}
		if !intervalsOverlap(members[left].validFrom, members[left].validUntil, members[right].validFrom, members[right].validUntil) {
			continue
		}
		union.union(left, right)
	}
	return rows.Err()
}

func currentRevisionForMemory(members []duplicateMember, memoryID string) string {
	for i := range members {
		if members[i].memoryID == memoryID {
			return members[i].revisionID
		}
	}
	return ""
}

func nullIntString(value sql.NullInt64) string {
	if !value.Valid {
		return "null"
	}
	return formatInt(value.Int64)
}

func formatInt(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

func duplicateSortKey(members []duplicateMember, ids []int) string {
	var builder strings.Builder
	for _, id := range ids {
		builder.WriteString(members[id].spaceID)
		builder.WriteString("\x00")
		builder.WriteString(members[id].memoryID)
		builder.WriteString("\x00")
	}
	return builder.String()
}

func duplicateClusterID(members []duplicateMember, ids []int) (string, string) {
	spaceID := members[ids[0]].spaceID
	revisions := make([]string, len(ids))
	for i, id := range ids {
		revisions[i] = members[id].revisionID
	}
	sort.Strings(revisions)
	hash := sha256.Sum256([]byte(spaceID + "\x00" + strings.Join(revisions, "\x00")))
	return spaceID, "cluster-" + hex.EncodeToString(hash[:])
}

func selectDuplicateRepresentative(members []duplicateMember, ids []int) int {
	best := ids[0]
	for _, id := range ids[1:] {
		candidate, current := members[id], members[best]
		if representativeLess(current, candidate) {
			best = id
		}
	}
	return best
}

func representativeLess(a, b duplicateMember) bool {
	if (a.state == "active") != (b.state == "active") {
		return b.state == "active"
	}
	if a.evidenceCount != b.evidenceCount {
		return b.evidenceCount > a.evidenceCount
	}
	if a.quality != b.quality {
		return b.quality > a.quality
	}
	return b.memoryID < a.memoryID
}

func duplicateReason(members []duplicateMember, ids []int, id int) string {
	target := members[id]
	if target.aggregate {
		return "aggregate_provenance"
	}
	canonical, evidence := false, false
	for _, other := range ids {
		if other == id {
			continue
		}
		peer := members[other]
		if peer.spaceID == target.spaceID && peer.kind == target.kind && peer.canonicalHash == target.canonicalHash {
			canonical = true
		}
		if sharesEvidenceKey(target.evidenceKeys, peer.evidenceKeys) {
			evidence = true
		}
	}
	switch {
	case canonical:
		return "equivalent_canonical_tuple"
	case evidence:
		return "shared_primary_evidence"
	default:
		return "semantic_time_compatible"
	}
}

func sharesEvidenceKey(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	known := make(map[string]struct{}, len(a))
	for _, key := range a {
		known[key] = struct{}{}
	}
	for _, key := range b {
		if _, ok := known[key]; ok {
			return true
		}
	}
	return false
}

type unionFind struct {
	parent []int
}

func newUnionFind(size int) *unionFind {
	parent := make([]int, size)
	for i := range parent {
		parent[i] = i
	}
	return &unionFind{parent: parent}
}

func (u *unionFind) find(value int) int {
	for u.parent[value] != value {
		u.parent[value] = u.parent[u.parent[value]]
		value = u.parent[value]
	}
	return value
}

func (u *unionFind) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return
	}
	if ra < rb {
		u.parent[rb] = ra
	} else {
		u.parent[ra] = rb
	}
}
