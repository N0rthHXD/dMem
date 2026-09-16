package portability

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const FormatVersion = 1

var ErrInvalidRequest = errors.New("invalid export request")

var dataMembers = []string{
	"spaces.jsonl", "raw_events.jsonl", "memories.jsonl", "revisions.jsonl",
	"evidence.jsonl", "entities.jsonl", "entity_mentions.jsonl", "relations.jsonl",
	"conflicts.jsonl", "feedback.jsonl", "aggregate_provenance.jsonl",
}

type SnapshotSource interface {
	BeginReadSnapshot(context.Context) (*sql.Tx, error)
}

type Request struct {
	SourceIdentity   string   `json:"source_identity"`
	SpaceIDs         []string `json:"space_ids"`
	IncludeAncestors bool     `json:"include_ancestors"`
	ClosurePolicy    string   `json:"closure_policy"`
	SensitivePolicy  string   `json:"sensitive_policy"`
	ReferenceTime    string   `json:"reference_time"`
	ExportIdentity   string   `json:"export_identity,omitempty"`
}

type ExternalReference struct {
	ObjectType   string `json:"object_type"`
	ObjectID     string `json:"object_id"`
	ReferencedBy string `json:"referenced_by"`
}

type SensitivitySummary struct {
	Included int `json:"included"`
	Omitted  int `json:"omitted"`
}

type Plan struct {
	ExportID         string              `json:"export_id"`
	CorpusGeneration int64               `json:"corpus_generation"`
	SpaceIDs         []string            `json:"space_ids"`
	ObjectCounts     map[string]int      `json:"object_counts"`
	EstimatedBytes   int64               `json:"estimated_bytes"`
	Sensitivity      SensitivitySummary  `json:"sensitivity"`
	ExternalRefs     []ExternalReference `json:"external_references"`
	RefusalReasons   []string            `json:"refusal_reasons"`
	objects          map[string][]record
}

type Manifest struct {
	FormatFamily       string              `json:"format_family"`
	FormatVersion      int                 `json:"format_version"`
	ExportID           string              `json:"export_id"`
	SourceIdentity     string              `json:"source_identity"`
	CreatedAt          string              `json:"created_at"`
	Selection          selection           `json:"selection"`
	ClosurePolicy      string              `json:"closure_policy"`
	SensitivePolicy    string              `json:"sensitive_policy"`
	ReferenceTime      string              `json:"reference_time"`
	CorpusGeneration   int64               `json:"corpus_generation"`
	ObjectCounts       map[string]int      `json:"object_counts"`
	Versions           versions            `json:"versions"`
	Sensitivity        SensitivitySummary  `json:"sensitivity"`
	ExternalReferences []ExternalReference `json:"external_references"`
	FileChecksums      map[string]string   `json:"file_checksums"`
	TotalSize          int64               `json:"total_size"`
}

type selection struct {
	SpaceIDs         []string `json:"space_ids"`
	IncludeAncestors bool     `json:"include_ancestors"`
}

type versions struct {
	Processing    []string `json:"processing"`
	Ranking       []string `json:"ranking"`
	Normalization []string `json:"normalization"`
}

type Checksums struct {
	Algorithm string            `json:"algorithm"`
	Files     map[string]string `json:"files"`
}

type record struct {
	ObjectID   string         `json:"object_id"`
	ObjectType string         `json:"object_type"`
	Data       map[string]any `json:"data"`
}

type ExportOptions struct {
	BeforePublish func(tempDirectory string) error
}

type Exporter struct{ source SnapshotSource }

func NewExporter(source SnapshotSource) *Exporter { return &Exporter{source: source} }

func (e *Exporter) DryRun(ctx context.Context, request Request) (Plan, error) {
	return e.plan(ctx, request)
}

func (e *Exporter) Export(ctx context.Context, request Request, destination string, options ExportOptions) (Plan, error) {
	canonical, err := validateRequest(request)
	if err != nil {
		return Plan{}, err
	}
	if destination == "" {
		return Plan{}, fmt.Errorf("%w: destination is required", ErrInvalidRequest)
	}
	parent := filepath.Dir(filepath.Clean(destination))
	if err := validateOutputDirectory(parent); err != nil {
		return Plan{}, err
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return Plan{}, fmt.Errorf("%w: destination already exists", ErrInvalidRequest)
		}
		return Plan{}, err
	}
	plan, err := e.plan(ctx, canonical)
	if err != nil {
		return Plan{}, err
	}
	if len(plan.RefusalReasons) != 0 {
		return Plan{}, fmt.Errorf("%w: %s", ErrInvalidRequest, strings.Join(plan.RefusalReasons, "; "))
	}
	temporary, err := os.MkdirTemp(parent, ".dmem-logical-*")
	if err != nil {
		return Plan{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := os.Chmod(temporary, 0o700); err != nil {
		return Plan{}, err
	}
	if err := writePackage(temporary, canonical, plan); err != nil {
		return Plan{}, err
	}
	if options.BeforePublish != nil {
		if err := options.BeforePublish(temporary); err != nil {
			return Plan{}, err
		}
	}
	if err := validateChecksums(temporary); err != nil {
		return Plan{}, err
	}
	if err := syncDirectory(temporary); err != nil {
		return Plan{}, err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return Plan{}, err
	}
	if err := syncDirectory(parent); err != nil {
		_ = os.RemoveAll(destination)
		_ = syncDirectory(parent)
		return Plan{}, err
	}
	published = true
	return plan, nil
}

func (e *Exporter) plan(ctx context.Context, request Request) (Plan, error) {
	canonical, err := validateRequest(request)
	if err != nil {
		return Plan{}, err
	}
	tx, err := e.source.BeginReadSnapshot(ctx)
	if err != nil {
		return Plan{}, err
	}
	defer tx.Rollback()
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT generation FROM corpus_metadata WHERE singleton=1`).Scan(&generation); err != nil {
		return Plan{}, err
	}
	spaces, err := selectedSpaces(ctx, tx, canonical.SpaceIDs, canonical.IncludeAncestors)
	if err != nil {
		return Plan{}, err
	}
	objects := make(map[string][]record, len(dataMembers))
	for _, member := range dataMembers {
		objects[member] = []record{}
	}
	spaceSet := makeSet(spaces)
	if err := collectFiltered(ctx, tx, &objects, "spaces.jsonl", "space", "space_id", `SELECT * FROM memory_spaces`, func(row map[string]any) bool { return spaceSet[text(row["space_id"])] }); err != nil {
		return Plan{}, err
	}

	memoryIDs, revisionIDs := map[string]bool{}, map[string]bool{}
	var sensitivity SensitivitySummary
	memories, err := queryMaps(ctx, tx, `SELECT * FROM memories ORDER BY memory_id`)
	if err != nil {
		return Plan{}, err
	}
	for _, row := range memories {
		if !spaceSet[text(row["space_id"])] {
			continue
		}
		if text(row["sensitivity"]) == "sensitive" {
			if canonical.SensitivePolicy == "exclude" {
				sensitivity.Omitted++
				continue
			}
			sensitivity.Included++
		}
		id := text(row["memory_id"])
		memoryIDs[id] = true
		objects["memories.jsonl"] = append(objects["memories.jsonl"], makeRecord("memory", id, row))
	}
	revisions, err := queryMaps(ctx, tx, `SELECT * FROM memory_revisions ORDER BY revision_id`)
	if err != nil {
		return Plan{}, err
	}
	for _, row := range revisions {
		if memoryIDs[text(row["memory_id"])] {
			id := text(row["revision_id"])
			revisionIDs[id] = true
			objects["revisions.jsonl"] = append(objects["revisions.jsonl"], makeRecord("revision", id, row))
		}
	}
	if err := collectFiltered(ctx, tx, &objects, "memories.jsonl", "memory_state_event", "state_event_id", `SELECT * FROM memory_state_events ORDER BY state_event_id`, func(r map[string]any) bool { return memoryIDs[text(r["memory_id"])] }); err != nil {
		return Plan{}, err
	}

	eventIDs := map[string]bool{}
	evidenceRows, err := queryMaps(ctx, tx, `SELECT * FROM memory_evidence ORDER BY evidence_id`)
	if err != nil {
		return Plan{}, err
	}
	for _, row := range evidenceRows {
		if revisionIDs[text(row["revision_id"])] {
			eventIDs[text(row["event_id"])] = true
			objects["evidence.jsonl"] = append(objects["evidence.jsonl"], makeRecord("memory_evidence", text(row["evidence_id"]), row))
		}
	}
	rawRows, err := queryMaps(ctx, tx, `SELECT e.event_id,e.operation_id,e.space_id,e.source_type,e.source_name,e.source_event_id,e.caller_id,e.occurred_at,e.received_at,e.processing_status,e.metadata_json,e.created_at,s.source_stream_key,e.sequence,c.content_id,c.content_kind,c.content_text,c.reference_uri,c.media_type,c.content_hash,c.size_bytes,c.declared_size,c.declared_hash,c.created_at AS content_created_at FROM raw_events e JOIN raw_contents c ON c.content_id=e.content_id LEFT JOIN source_streams s ON s.source_stream_id=e.source_stream_id ORDER BY e.event_id`)
	if err != nil {
		return Plan{}, err
	}
	eventSpaces := map[string]string{}
	for _, row := range rawRows {
		eventSpaces[text(row["event_id"])] = text(row["space_id"])
	}

	entityIDs := map[string]bool{}
	entities, err := queryMaps(ctx, tx, `SELECT * FROM entities ORDER BY entity_id`)
	if err != nil {
		return Plan{}, err
	}
	for _, row := range entities {
		if !spaceSet[text(row["space_id"])] {
			continue
		}
		if text(row["sensitivity"]) == "sensitive" {
			if canonical.SensitivePolicy == "exclude" {
				sensitivity.Omitted++
				continue
			}
			sensitivity.Included++
		}
		id := text(row["entity_id"])
		entityIDs[id] = true
		objects["entities.jsonl"] = append(objects["entities.jsonl"], makeRecord("entity", id, row))
	}
	for _, spec := range []struct{ typ, query string }{
		{"revision_subject_entity", `SELECT * FROM revision_subject_entities ORDER BY revision_id,entity_order`},
		{"revision_object_entity", `SELECT * FROM revision_object_entities ORDER BY revision_id,entity_order`},
	} {
		if err := collectFiltered(ctx, tx, &objects, "revisions.jsonl", spec.typ, "revision_id", spec.query, func(r map[string]any) bool {
			return revisionIDs[text(r["revision_id"])] && entityIDs[text(r["entity_id"])]
		}); err != nil {
			return Plan{}, err
		}
	}
	mentions, err := queryMaps(ctx, tx, `SELECT * FROM entity_mentions ORDER BY mention_id`)
	if err != nil {
		return Plan{}, err
	}
	for _, row := range mentions {
		eventID := text(row["event_id"])
		if entityIDs[text(row["entity_id"])] && (revisionIDs[text(row["revision_id"])] || spaceSet[eventSpaces[eventID]]) {
			if eventID != "" {
				eventIDs[eventID] = true
			}
			objects["entity_mentions.jsonl"] = append(objects["entity_mentions.jsonl"], makeRecord("entity_mention", text(row["mention_id"]), row))
		}
	}
	for _, spec := range []struct{ typ, id, query string }{
		{"entity_event", "entity_event_id", `SELECT * FROM entity_events ORDER BY entity_event_id`},
		{"entity_alias", "alias_id", `SELECT * FROM entity_aliases ORDER BY alias_id`},
	} {
		if err := collectFiltered(ctx, tx, &objects, "entities.jsonl", spec.typ, spec.id, spec.query, func(r map[string]any) bool { return entityIDs[text(r["entity_id"])] }); err != nil {
			return Plan{}, err
		}
	}
	aliasIDs := idsOf(objects["entities.jsonl"], "entity_alias")
	if err := collectFiltered(ctx, tx, &objects, "entities.jsonl", "entity_alias_event", "alias_event_id", `SELECT * FROM entity_alias_events ORDER BY alias_event_id`, func(r map[string]any) bool { return aliasIDs[text(r["alias_id"])] }); err != nil {
		return Plan{}, err
	}

	relationIDs := map[string]bool{}
	external := []ExternalReference{}
	relations, err := queryMaps(ctx, tx, `SELECT * FROM memory_relations ORDER BY relation_id`)
	if err != nil {
		return Plan{}, err
	}
	for _, row := range relations {
		sourceIn, targetIn := revisionIDs[text(row["source_revision_id"])], revisionIDs[text(row["target_revision_id"])]
		if !sourceIn && !targetIn {
			continue
		}
		id := text(row["relation_id"])
		relationIDs[id] = true
		objects["relations.jsonl"] = append(objects["relations.jsonl"], makeRecord("relation", id, row))
		if !sourceIn {
			external = append(external, ExternalReference{"revision", text(row["source_revision_id"]), id})
		}
		if !targetIn {
			external = append(external, ExternalReference{"revision", text(row["target_revision_id"]), id})
		}
	}
	if err := collectFiltered(ctx, tx, &objects, "relations.jsonl", "relation_state_event", "state_event_id", `SELECT * FROM relation_state_events ORDER BY state_event_id`, func(r map[string]any) bool { return relationIDs[text(r["relation_id"])] }); err != nil {
		return Plan{}, err
	}
	factIDs := map[string]bool{}
	facts, err := queryMaps(ctx, tx, `SELECT * FROM entity_facts ORDER BY fact_id`)
	if err != nil {
		return Plan{}, err
	}
	for _, row := range facts {
		objectEntityID := text(row["object_entity_id"])
		if revisionIDs[text(row["source_revision_id"])] && entityIDs[text(row["subject_entity_id"])] && (objectEntityID == "" || entityIDs[objectEntityID]) {
			id := text(row["fact_id"])
			factIDs[id] = true
			objects["relations.jsonl"] = append(objects["relations.jsonl"], makeRecord("entity_fact", id, row))
		}
	}
	if err := collectFiltered(ctx, tx, &objects, "relations.jsonl", "entity_fact_state_event", "state_event_id", `SELECT * FROM entity_fact_state_events ORDER BY state_event_id`, func(r map[string]any) bool { return factIDs[text(r["fact_id"])] }); err != nil {
		return Plan{}, err
	}
	relationEvidence, err := queryMaps(ctx, tx, `SELECT * FROM relation_evidence ORDER BY evidence_id`)
	if err != nil {
		return Plan{}, err
	}
	for _, row := range relationEvidence {
		if !relationIDs[text(row["relation_id"])] {
			continue
		}
		if eventID := text(row["event_id"]); eventID != "" {
			eventIDs[eventID] = true
		}
		if revisionID := text(row["revision_id"]); revisionID != "" && !revisionIDs[revisionID] {
			external = append(external, ExternalReference{"revision", revisionID, text(row["evidence_id"])})
		}
		objects["evidence.jsonl"] = append(objects["evidence.jsonl"], makeRecord("relation_evidence", text(row["evidence_id"]), row))
	}
	factEvidence, err := queryMaps(ctx, tx, `SELECT * FROM entity_fact_evidence ORDER BY evidence_id`)
	if err != nil {
		return Plan{}, err
	}
	for _, row := range factEvidence {
		if !factIDs[text(row["fact_id"])] {
			continue
		}
		if eventID := text(row["event_id"]); eventID != "" {
			eventIDs[eventID] = true
		}
		objects["evidence.jsonl"] = append(objects["evidence.jsonl"], makeRecord("entity_fact_evidence", text(row["evidence_id"]), row))
	}
	for _, row := range rawRows {
		if eventIDs[text(row["event_id"])] {
			objects["raw_events.jsonl"] = append(objects["raw_events.jsonl"], makeRecord("raw_event", text(row["event_id"]), row))
		}
	}

	caseIDs := map[string]bool{}
	participants, err := queryMaps(ctx, tx, `SELECT * FROM conflict_case_participants ORDER BY case_id,participant_order`)
	if err != nil {
		return Plan{}, err
	}
	caseRelations, err := queryMaps(ctx, tx, `SELECT * FROM conflict_case_relations ORDER BY case_id,relation_id`)
	if err != nil {
		return Plan{}, err
	}
	for _, row := range participants {
		if revisionIDs[text(row["revision_id"])] {
			caseIDs[text(row["case_id"])] = true
		}
	}
	for _, row := range caseRelations {
		if relationIDs[text(row["relation_id"])] {
			caseIDs[text(row["case_id"])] = true
		}
	}
	for _, row := range participants {
		if caseIDs[text(row["case_id"])] && !revisionIDs[text(row["revision_id"])] {
			external = append(external, ExternalReference{"revision", text(row["revision_id"]), text(row["case_id"])})
		}
	}
	for _, spec := range []struct{ typ, id, query string }{
		{"conflict_case", "case_id", `SELECT * FROM conflict_cases ORDER BY case_id`},
		{"conflict_participant", "case_id", `SELECT * FROM conflict_case_participants ORDER BY case_id,participant_order`},
		{"conflict_relation", "case_id", `SELECT * FROM conflict_case_relations ORDER BY case_id,relation_id`},
		{"conflict_resolution", "resolution_event_id", `SELECT * FROM conflict_resolution_events ORDER BY resolution_event_id`},
	} {
		if err := collectFiltered(ctx, tx, &objects, "conflicts.jsonl", spec.typ, spec.id, spec.query, func(r map[string]any) bool { return caseIDs[text(r["case_id"])] }); err != nil {
			return Plan{}, err
		}
	}
	resolutionIDs := idsOf(objects["conflicts.jsonl"], "conflict_resolution")
	if err := collectFiltered(ctx, tx, &objects, "conflicts.jsonl", "conflict_resolution_ref", "result_ref_id", `SELECT * FROM conflict_resolution_result_refs ORDER BY result_ref_id`, func(r map[string]any) bool { return resolutionIDs[text(r["resolution_event_id"])] }); err != nil {
		return Plan{}, err
	}
	if err := collectFiltered(ctx, tx, &objects, "feedback.jsonl", "feedback_event", "feedback_id", `SELECT * FROM feedback_events ORDER BY feedback_id`, func(r map[string]any) bool { return revisionIDs[text(r["revision_id"])] }); err != nil {
		return Plan{}, err
	}
	if err := collectFiltered(ctx, tx, &objects, "aggregate_provenance.jsonl", "aggregate_descriptor", "aggregate_id", `SELECT * FROM aggregate_descriptors ORDER BY aggregate_id`, func(r map[string]any) bool { return memoryIDs[text(r["memory_id"])] }); err != nil {
		return Plan{}, err
	}
	refusals := []string{}
	for _, relation := range objects["relations.jsonl"] {
		if relation.ObjectType == "relation" && text(relation.Data["relation_type"]) == "derived_from" && (!revisionIDs[text(relation.Data["source_revision_id"])] || !revisionIDs[text(relation.Data["target_revision_id"])]) {
			refusals = append(refusals, "aggregate input revision is outside the selected closure: "+relation.ObjectID)
		}
	}
	sort.Strings(refusals)

	sort.Slice(external, func(i, j int) bool {
		if external[i].ObjectID != external[j].ObjectID {
			return external[i].ObjectID < external[j].ObjectID
		}
		return external[i].ReferencedBy < external[j].ReferencedBy
	})
	external = uniqueExternalReferences(external)
	for _, stub := range externalStubs(external) {
		objects["relations.jsonl"] = append(objects["relations.jsonl"], stub)
	}
	for member := range objects {
		sortRecords(objects[member])
	}
	counts := map[string]int{}
	var estimated int64
	for _, member := range dataMembers {
		counts[member] = len(objects[member])
		for _, object := range objects[member] {
			data, _ := json.Marshal(object)
			estimated += int64(len(data) + 1)
		}
	}
	exportID := canonical.ExportIdentity
	if exportID == "" {
		identityInput := struct {
			FormatVersion    int     `json:"format_version"`
			SourceIdentity   string  `json:"source_identity"`
			Request          Request `json:"request"`
			ReferenceTime    string  `json:"reference_time"`
			CorpusGeneration int64   `json:"corpus_generation"`
		}{FormatVersion, canonical.SourceIdentity, canonical, canonical.ReferenceTime, generation}
		encoded, _ := json.Marshal(identityInput)
		sum := sha256.Sum256(encoded)
		exportID = hex.EncodeToString(sum[:])
	}
	return Plan{ExportID: exportID, CorpusGeneration: generation, SpaceIDs: spaces, ObjectCounts: counts, EstimatedBytes: estimated, Sensitivity: sensitivity, ExternalRefs: external, RefusalReasons: refusals, objects: objects}, nil
}

func validateRequest(request Request) (Request, error) {
	request.SourceIdentity = strings.TrimSpace(request.SourceIdentity)
	request.ClosurePolicy = strings.TrimSpace(request.ClosurePolicy)
	if request.SourceIdentity == "" || len(request.SpaceIDs) == 0 {
		return Request{}, fmt.Errorf("%w: source_identity and space_ids are required", ErrInvalidRequest)
	}
	if request.ClosurePolicy != "strict" {
		return Request{}, fmt.Errorf("%w: closure_policy must be strict", ErrInvalidRequest)
	}
	if request.SensitivePolicy != "exclude" && request.SensitivePolicy != "include" {
		return Request{}, fmt.Errorf("%w: sensitive_policy must be exclude or include", ErrInvalidRequest)
	}
	reference, err := time.Parse(time.RFC3339Nano, request.ReferenceTime)
	if err != nil {
		return Request{}, fmt.Errorf("%w: reference_time must be RFC3339", ErrInvalidRequest)
	}
	request.ReferenceTime = reference.UTC().Format(time.RFC3339Nano)
	sort.Strings(request.SpaceIDs)
	request.SpaceIDs = unique(request.SpaceIDs)
	return request, nil
}

func selectedSpaces(ctx context.Context, tx *sql.Tx, requested []string, ancestors bool) ([]string, error) {
	all, err := queryMaps(ctx, tx, `SELECT space_id,parent_space_id FROM memory_spaces ORDER BY space_id`)
	if err != nil {
		return nil, err
	}
	parents := map[string]string{}
	for _, row := range all {
		parents[text(row["space_id"])] = text(row["parent_space_id"])
	}
	selected := map[string]bool{}
	for _, id := range requested {
		if _, ok := parents[id]; !ok {
			return nil, fmt.Errorf("%w: selected space %s does not exist", ErrInvalidRequest, id)
		}
		for current := id; current != ""; current = parents[current] {
			selected[current] = true
			if !ancestors {
				break
			}
		}
	}
	result := make([]string, 0, len(selected))
	for id := range selected {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func writePackage(directory string, request Request, plan Plan) error {
	checksums := map[string]string{}
	var total int64
	for _, member := range dataMembers {
		path := filepath.Join(directory, member)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		hash := sha256.New()
		writer := bufio.NewWriter(io.MultiWriter(file, hash))
		for _, object := range plan.objects[member] {
			encoded, err := json.Marshal(object)
			if err != nil {
				file.Close()
				return err
			}
			if _, err := writer.Write(append(encoded, '\n')); err != nil {
				file.Close()
				return err
			}
			total += int64(len(encoded) + 1)
		}
		if err := writer.Flush(); err != nil {
			file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		checksums[member] = hex.EncodeToString(hash.Sum(nil))
	}
	manifest := Manifest{FormatFamily: "dmem-logical", FormatVersion: FormatVersion, ExportID: plan.ExportID, SourceIdentity: request.SourceIdentity, CreatedAt: request.ReferenceTime, Selection: selection{request.SpaceIDs, request.IncludeAncestors}, ClosurePolicy: request.ClosurePolicy, SensitivePolicy: request.SensitivePolicy, ReferenceTime: request.ReferenceTime, CorpusGeneration: plan.CorpusGeneration, ObjectCounts: plan.ObjectCounts, Versions: collectVersions(plan.objects), Sensitivity: plan.Sensitivity, ExternalReferences: plan.ExternalRefs, FileChecksums: checksums, TotalSize: total}
	manifestHash, _, err := writeCanonicalFile(filepath.Join(directory, "manifest.json"), manifest)
	if err != nil {
		return err
	}
	allChecksums := make(map[string]string, len(checksums)+1)
	for name, value := range checksums {
		allChecksums[name] = value
	}
	allChecksums["manifest.json"] = manifestHash
	_, _, err = writeCanonicalFile(filepath.Join(directory, "checksums.json"), Checksums{"sha256", allChecksums})
	return err
}

func writeCanonicalFile(path string, value any) (string, int64, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", 0, err
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	if _, err := file.Write(encoded); err != nil {
		file.Close()
		return "", 0, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return "", 0, err
	}
	if err := file.Close(); err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), int64(len(encoded)), nil
}

func validateChecksums(directory string) error {
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() || directoryInfo.Mode().Perm() != 0o700 || !ownedByCurrentUser(directoryInfo) {
		return errors.New("package directory must be an owned 0700 directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	wantMembers := append(append([]string{}, dataMembers...), "manifest.json", "checksums.json")
	sort.Strings(wantMembers)
	gotMembers := make([]string, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ownedByCurrentUser(info) {
			return errors.New("package member must be an owned regular 0600 file")
		}
		gotMembers = append(gotMembers, entry.Name())
	}
	sort.Strings(gotMembers)
	if strings.Join(gotMembers, "\x00") != strings.Join(wantMembers, "\x00") {
		return errors.New("package has missing or unknown members")
	}
	data, err := os.ReadFile(filepath.Join(directory, "checksums.json"))
	if err != nil {
		return err
	}
	var checksums Checksums
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&checksums); err != nil || checksums.Algorithm != "sha256" {
		return errors.New("invalid checksums.json")
	}
	for name, expected := range checksums.Files {
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != expected {
			return fmt.Errorf("checksum mismatch for %s", name)
		}
	}
	return nil
}

func validateOutputDirectory(path string) error {
	clean, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	current := clean
	for {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("output path must not contain symlinks")
		}
		if current == filepath.Dir(current) {
			break
		}
		current = filepath.Dir(current)
	}
	info, err := os.Stat(clean)
	if err != nil {
		return err
	}
	if !info.IsDir() || !ownedByCurrentUser(info) {
		return errors.New("output directory must be owned by the current user")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("output directory must not be accessible by group or other")
	}
	return nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

func syncDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func queryMaps(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]map[string]any, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			if bytes, ok := values[i].([]byte); ok {
				values[i] = string(bytes)
			}
			row[column] = values[i]
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func collectFiltered(ctx context.Context, tx *sql.Tx, objects *map[string][]record, member, typ, id, query string, include func(map[string]any) bool) error {
	rows, err := queryMaps(ctx, tx, query)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if include(row) {
			(*objects)[member] = append((*objects)[member], makeRecord(typ, compoundID(row, id), row))
		}
	}
	return nil
}
func makeRecord(typ, id string, row map[string]any) record { return record{id, typ, row} }
func compoundID(row map[string]any, id string) string {
	value := text(row[id])
	if value != "" && row["participant_order"] == nil && row["relation_id"] == nil {
		return value
	}
	keys := make([]string, 0, len(row))
	for key := range row {
		if strings.HasSuffix(key, "_id") || strings.HasSuffix(key, "_order") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, text(row[key]))
	}
	return strings.Join(parts, "/")
}
func sortRecords(records []record) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].ObjectID != records[j].ObjectID {
			return records[i].ObjectID < records[j].ObjectID
		}
		return records[i].ObjectType < records[j].ObjectType
	})
}
func text(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
func makeSet(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		result[value] = true
	}
	return result
}
func unique(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}
func idsOf(records []record, typ string) map[string]bool {
	result := map[string]bool{}
	for _, record := range records {
		if record.ObjectType == typ {
			result[record.ObjectID] = true
		}
	}
	return result
}
func collectVersions(objects map[string][]record) versions {
	processing, ranking, normalization := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, records := range objects {
		for _, object := range records {
			if value := text(object.Data["processing_version"]); value != "" {
				processing[value] = true
			}
			if value := text(object.Data["ranking_version"]); value != "" {
				ranking[value] = true
			}
			if value := text(object.Data["normalization_version"]); value != "" {
				normalization[value] = true
			}
		}
	}
	return versions{setKeys(processing), setKeys(ranking), setKeys(normalization)}
}
func setKeys(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func uniqueExternalReferences(values []ExternalReference) []ExternalReference {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func externalStubs(references []ExternalReference) []record {
	type stubKey struct{ objectType, objectID string }
	grouped := map[stubKey][]string{}
	for _, reference := range references {
		key := stubKey{reference.ObjectType, reference.ObjectID}
		grouped[key] = append(grouped[key], reference.ReferencedBy)
	}
	stubs := make([]record, 0, len(grouped))
	for key, referencedBy := range grouped {
		sort.Strings(referencedBy)
		stubs = append(stubs, makeRecord("external_reference_stub", key.objectID, map[string]any{
			"object_id": key.objectID, "object_type": key.objectType, "referenced_by": unique(referencedBy),
		}))
	}
	return stubs
}
