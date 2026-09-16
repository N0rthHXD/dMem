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

	"github.com/N0rthHXD/dMem/internal/storage"
)

var (
	ErrUnsupportedFormat         = errors.New("unsupported_format")
	ErrIncompatibleVersion       = errors.New("incompatible_version")
	ErrInvalidPackage            = errors.New("invalid_package")
	ErrImportInvalidRequest      = errors.New("invalid_request")
	ErrImportResourceExhausted   = errors.New("resource_exhausted")
	ErrImportIdempotencyConflict = errors.New("idempotency_conflict")
	ErrRevisionConflict          = errors.New("revision_conflict")
)

type ImportLimits struct {
	MaxFiles      int
	MaxRecords    int
	MaxFileBytes  int64
	MaxTotalBytes int64
	BatchSize     int
}

func DefaultImportLimits() ImportLimits {
	return ImportLimits{MaxFiles: 13, MaxRecords: 100000, MaxFileBytes: 64 << 20, MaxTotalBytes: 512 << 20, BatchSize: 500}
}

type ImportRequest struct {
	TargetSpaceID  string            `json:"target_space_id"`
	CreatedBy      string            `json:"created_by"`
	ObjectMappings map[string]string `json:"object_mappings,omitempty"`
}

type ImportPlanItem struct {
	Member             string `json:"member"`
	ObjectType         string `json:"object_type"`
	SourceID           string `json:"source_id"`
	LocalID            string `json:"local_id"`
	ObjectHash         string `json:"object_hash"`
	Action             string `json:"action"`
	ExpectedRevisionID string `json:"expected_revision_id,omitempty"`
	record             record
}

type ImportPlan struct {
	SourceIdentity string            `json:"source_identity"`
	ExportID       string            `json:"export_id"`
	TargetSpaceID  string            `json:"target_space_id"`
	PackageHash    string            `json:"package_hash"`
	PlanHash       string            `json:"plan_hash"`
	Items          []ImportPlanItem  `json:"items"`
	SpaceMappings  map[string]string `json:"space_mappings"`
	manifest       Manifest
}

type ImportRepository interface {
	BeginReadSnapshot(context.Context) (*sql.Tx, error)
	ExistingImportMappings(context.Context, string, string) (map[string]struct{ LocalID, Hash string }, error)
	FindMemoryByContentHash(context.Context, string, string) (string, string, error)
	CurrentRevision(context.Context, string) (string, string, error)
	StartImport(context.Context, storage.ImportOperationSpec) (storage.ImportOperationStatus, error)
	ClaimImport(context.Context, string) (int64, error)
	CommitImportBatch(context.Context, string, int64, int, []storage.ImportObject, string, string, string) error
	FinishImport(context.Context, string, int64) error
	InterruptImport(context.Context, string, int64) error
	FailImport(context.Context, string, int64, string, string) error
	CancelImport(context.Context, string) error
	ImportStatus(context.Context, string) (storage.ImportOperationStatus, error)
}

type Importer struct {
	repository ImportRepository
	limits     ImportLimits
}

func NewImporter(repository ImportRepository, limits ImportLimits) *Importer {
	if limits.MaxFiles == 0 {
		limits = DefaultImportLimits()
	}
	return &Importer{repository, limits}
}

func (i *Importer) DryRun(ctx context.Context, directory string, request ImportRequest) (ImportPlan, error) {
	if strings.TrimSpace(request.TargetSpaceID) == "" || strings.TrimSpace(request.CreatedBy) == "" {
		return ImportPlan{}, fmt.Errorf("%w: target_space_id and created_by are required", ErrImportInvalidRequest)
	}
	pkg, err := validatePackage(directory, i.limits)
	if err != nil {
		return ImportPlan{}, err
	}
	tx, err := i.repository.BeginReadSnapshot(ctx)
	if err != nil {
		return ImportPlan{}, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM memory_spaces WHERE space_id=?`, request.TargetSpaceID).Scan(&exists); err != nil {
		return ImportPlan{}, fmt.Errorf("%w: target space does not exist", ErrImportInvalidRequest)
	}
	mappings, err := i.repository.ExistingImportMappings(ctx, pkg.manifest.SourceIdentity, pkg.manifest.ExportID)
	if err != nil {
		return ImportPlan{}, err
	}
	spaceMappings := map[string]string{}
	for _, r := range pkg.records["spaces.jsonl"] {
		spaceMappings[r.ObjectID] = request.TargetSpaceID
	}
	local := map[string]string{}
	proposalMemories := map[string]bool{}
	proposalRevisions := map[string]bool{}
	items := make([]ImportPlanItem, 0, pkg.count)
	for _, member := range importOrder {
		for _, r := range pkg.records[member] {
			if r.ObjectType == "external_reference_stub" {
				continue
			}
			hash := hashRecord(r)
			key := r.ObjectType + "\x00" + r.ObjectID
			localID := deterministicLocalID(pkg.manifest.SourceIdentity, pkg.manifest.ExportID, r.ObjectType, r.ObjectID)
			action := "import"
			if explicit := request.ObjectMappings[key]; explicit != "" {
				localID = explicit
			}
			if existing, ok := mappings[key]; ok {
				if existing.Hash != hash {
					return ImportPlan{}, fmt.Errorf("%w: %s %s", ErrImportIdempotencyConflict, r.ObjectType, r.ObjectID)
				}
				localID = existing.LocalID
				action = "duplicate"
			}
			if r.ObjectType == "space" {
				localID = request.TargetSpaceID
				action = "duplicate"
			}
			local[key] = localID
			items = append(items, ImportPlanItem{Member: member, ObjectType: r.ObjectType, SourceID: r.ObjectID, LocalID: localID, ObjectHash: hash, Action: action, record: r})
		}
	}
	for n := range items {
		rewriteReferences(items[n].record.Data, local)
		if items[n].ObjectType == "raw_event" {
			items[n].record.Data["local_content_id"] = deterministicLocalID(pkg.manifest.SourceIdentity, pkg.manifest.ExportID, "raw_content", text(items[n].record.Data["content_id"]))
		}
		if items[n].ObjectType == "memory" && items[n].Action == "import" {
			currentSource := text(items[n].record.Data["current_revision_id"])
			revision := findRecord(pkg.records["revisions.jsonl"], "revision", currentSource)
			if revision.ObjectID != "" {
				if memoryID, revisionID, e := i.repository.FindMemoryByContentHash(ctx, request.TargetSpaceID, text(revision.Data["canonical_hash"])); e != nil {
					return ImportPlan{}, e
				} else if memoryID != "" {
					items[n].LocalID = memoryID
					items[n].Action = "duplicate"
					local["memory\x00"+items[n].SourceID] = memoryID
					local["revision\x00"+currentSource] = revisionID
					mapRevisionItem(items, currentSource, revisionID, "duplicate")
				}
			}
			if explicit := request.ObjectMappings["memory\x00"+items[n].SourceID]; explicit != "" {
				expected, hash, e := i.repository.CurrentRevision(ctx, explicit)
				if e != nil {
					return ImportPlan{}, e
				}
				if expected == "" {
					return ImportPlan{}, fmt.Errorf("%w: mapped memory does not exist", ErrImportInvalidRequest)
				}
				if revision.ObjectID != "" && hash != text(revision.Data["canonical_hash"]) {
					items[n].Action = "proposal"
					proposalMemories[items[n].SourceID] = true
					items[n].ExpectedRevisionID = expected
					items[n].LocalID = explicit
					local["memory\x00"+items[n].SourceID] = explicit
					for index := range items {
						if items[index].ObjectType == "revision" && text(items[index].record.Data["memory_id"]) == items[n].SourceID {
							items[index].Action = "duplicate"
							items[index].LocalID = expected
							local["revision\x00"+items[index].SourceID] = expected
							proposalRevisions[items[index].SourceID] = true
						}
					}
				}
			}
		}
	}
	proposalFacts := map[string]bool{}
	for _, item := range items {
		if item.ObjectType == "entity_fact" && referencesProposal(item.record.Data, proposalMemories, proposalRevisions, nil) {
			proposalFacts[item.SourceID] = true
		}
	}
	for n := range items {
		rewriteReferences(items[n].record.Data, local)
		if items[n].Action == "import" && referencesProposal(items[n].record.Data, proposalMemories, proposalRevisions, proposalFacts) {
			items[n].Action = "duplicate"
		}
		if items[n].ObjectType == "relation" && (text(items[n].record.Data["local_source_revision_id"]) == "" || text(items[n].record.Data["local_target_revision_id"]) == "") {
			items[n].Action = "duplicate"
		}
	}
	items = orderImportItems(items)
	encoded, _ := json.Marshal(struct {
		Target   string            `json:"target_space_id"`
		Mappings map[string]string `json:"object_mappings"`
		Package  string            `json:"package_hash"`
	}{request.TargetSpaceID, request.ObjectMappings, pkg.packageHash})
	sum := sha256.Sum256(encoded)
	return ImportPlan{SourceIdentity: pkg.manifest.SourceIdentity, ExportID: pkg.manifest.ExportID, TargetSpaceID: request.TargetSpaceID, PackageHash: pkg.packageHash, PlanHash: hex.EncodeToString(sum[:]), Items: items, SpaceMappings: spaceMappings, manifest: pkg.manifest}, nil
}

func (i *Importer) Import(ctx context.Context, directory string, request ImportRequest) (storage.ImportOperationStatus, error) {
	plan, err := i.DryRun(ctx, directory, request)
	if err != nil {
		return storage.ImportOperationStatus{}, err
	}
	opID := deterministicLocalID(plan.SourceIdentity, plan.ExportID, "import_operation", plan.TargetSpaceID)
	status, err := i.repository.StartImport(ctx, storage.ImportOperationSpec{ImportOperationID: opID, OperationID: opID, SourceID: deterministicLocalID(plan.SourceIdentity, "source"), SourceIdentity: plan.SourceIdentity, ExportID: plan.ExportID, TargetSpaceID: plan.TargetSpaceID, PackageHash: plan.PackageHash, PlanHash: plan.PlanHash, TotalObjects: len(plan.Items), CreatedBy: request.CreatedBy, SpaceMappings: plan.SpaceMappings})
	if err != nil {
		return status, mapImportError(err)
	}
	if status.State == "succeeded" {
		return status, nil
	}
	generation, err := i.repository.ClaimImport(ctx, opID)
	if err != nil {
		return status, err
	}
	committed := status.CommittedObjects
	batch := committed / i.limits.BatchSize
	for committed < len(plan.Items) {
		select {
		case <-ctx.Done():
			_ = i.repository.InterruptImport(context.Background(), opID, generation)
			return i.repository.ImportStatus(context.Background(), opID)
		default:
		}
		end := importBatchEnd(plan.Items, committed, i.limits.BatchSize)
		if end > len(plan.Items) {
			end = len(plan.Items)
		}
		objects := make([]storage.ImportObject, 0, end-committed)
		for _, item := range plan.Items[committed:end] {
			data := item.record.Data
			if item.Action == "proposal" {
				rev := findRecordByMemory(plan.Items, item.SourceID)
				data = cloneMap(rev.record.Data)
				data["target_memory_id"] = request.ObjectMappings["memory\x00"+item.SourceID]
				data["expected_revision_id"] = item.ExpectedRevisionID
				data["local_proposal_id"] = deterministicLocalID(plan.SourceIdentity, plan.ExportID, "proposal", item.SourceID)
				data["memory_type"] = rev.record.Data["kind"]
				data["sensitivity"] = item.record.Data["sensitivity"]
				data["local_event_id"] = firstLocal(plan.Items, "raw_event")
			}
			objects = append(objects, storage.ImportObject{SourceType: item.ObjectType, SourceID: item.SourceID, LocalID: item.LocalID, ObjectHash: item.ObjectHash, Data: data, Action: item.Action})
		}
		last := plan.Items[end-1]
		if err := i.repository.CommitImportBatch(ctx, opID, generation, batch, objects, last.Member, last.SourceID, request.CreatedBy); err != nil {
			code := "storage_unavailable"
			mapped := err
			if errors.Is(err, storage.ErrConflict) {
				code, mapped = "revision_conflict", ErrRevisionConflict
			}
			_ = i.repository.FailImport(context.Background(), opID, generation, code, err.Error())
			return storage.ImportOperationStatus{}, mapped
		}
		committed = end
		batch++
	}
	if err := i.repository.FinishImport(ctx, opID, generation); err != nil {
		return storage.ImportOperationStatus{}, err
	}
	return i.repository.ImportStatus(ctx, opID)
}
func (i *Importer) Status(ctx context.Context, id string) (storage.ImportOperationStatus, error) {
	return i.repository.ImportStatus(ctx, id)
}
func (i *Importer) Cancel(ctx context.Context, id string) error {
	return i.repository.CancelImport(ctx, id)
}

type validatedPackage struct {
	manifest    Manifest
	records     map[string][]record
	count       int
	packageHash string
}

var importOrder = []string{"spaces.jsonl", "raw_events.jsonl", "memories.jsonl", "revisions.jsonl", "evidence.jsonl", "entities.jsonl", "entity_mentions.jsonl", "relations.jsonl", "conflicts.jsonl", "feedback.jsonl", "aggregate_provenance.jsonl"}
var memberTypes = map[string]map[string]bool{
	"spaces.jsonl": {"space": true}, "raw_events.jsonl": {"raw_event": true}, "memories.jsonl": {"memory": true, "memory_state_event": true}, "revisions.jsonl": {"revision": true, "revision_subject_entity": true, "revision_object_entity": true}, "evidence.jsonl": {"memory_evidence": true, "relation_evidence": true, "entity_fact_evidence": true}, "entities.jsonl": {"entity": true, "entity_event": true, "entity_alias": true, "entity_alias_event": true}, "entity_mentions.jsonl": {"entity_mention": true}, "relations.jsonl": {"relation": true, "relation_state_event": true, "entity_fact": true, "entity_fact_state_event": true, "external_reference_stub": true}, "conflicts.jsonl": {"conflict_case": true, "conflict_participant": true, "conflict_relation": true, "conflict_resolution": true, "conflict_resolution_ref": true}, "feedback.jsonl": {"feedback_event": true}, "aggregate_provenance.jsonl": {"aggregate_descriptor": true},
}

func validatePackage(directory string, limits ImportLimits) (validatedPackage, error) {
	if err := validateInputPath(directory); err != nil {
		return validatedPackage{}, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return validatedPackage{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || !ownedByCurrentUser(info) {
		return validatedPackage{}, fmt.Errorf("%w: package directory", ErrInvalidPackage)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return validatedPackage{}, err
	}
	want := append(append([]string{}, dataMembers...), "manifest.json", "checksums.json")
	sort.Strings(want)
	if len(entries) != len(want) || len(entries) > limits.MaxFiles {
		return validatedPackage{}, fmt.Errorf("%w: exact members", ErrInvalidPackage)
	}
	names := make([]string, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "/") || entry.Name() == "." || entry.Name() == ".." {
			return validatedPackage{}, fmt.Errorf("%w: traversal", ErrInvalidPackage)
		}
		fi, e := entry.Info()
		if e != nil {
			return validatedPackage{}, e
		}
		if entry.Type()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0600 || !ownedByCurrentUser(fi) {
			return validatedPackage{}, fmt.Errorf("%w: unsafe member", ErrInvalidPackage)
		}
		if fi.Size() > limits.MaxFileBytes {
			return validatedPackage{}, ErrImportResourceExhausted
		}
		total += fi.Size()
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if strings.Join(names, "\x00") != strings.Join(want, "\x00") {
		return validatedPackage{}, fmt.Errorf("%w: exact members", ErrInvalidPackage)
	}
	if total > limits.MaxTotalBytes {
		return validatedPackage{}, ErrImportResourceExhausted
	}
	manifestData, e := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if e != nil {
		return validatedPackage{}, e
	}
	var manifest Manifest
	if e = strictJSON(manifestData, &manifest); e != nil {
		return validatedPackage{}, classifyManifest(manifestData, e)
	}
	if manifest.FormatFamily != "dmem-logical" {
		return validatedPackage{}, ErrUnsupportedFormat
	}
	if manifest.FormatVersion != 1 {
		return validatedPackage{}, ErrIncompatibleVersion
	}
	checksumData, e := os.ReadFile(filepath.Join(directory, "checksums.json"))
	if e != nil {
		return validatedPackage{}, e
	}
	var checksums Checksums
	if e = strictJSON(checksumData, &checksums); e != nil || checksums.Algorithm != "sha256" || len(checksums.Files) != len(dataMembers)+1 {
		return validatedPackage{}, fmt.Errorf("%w: checksums", ErrInvalidPackage)
	}
	records := map[string][]record{}
	global := map[string]bool{}
	count := 0
	packageHash := sha256.New()
	for _, name := range append(importOrder, "manifest.json") {
		data, e := os.ReadFile(filepath.Join(directory, name))
		if e != nil {
			return validatedPackage{}, e
		}
		packageHash.Write([]byte(name))
		packageHash.Write(data)
		sum := sha256.Sum256(data)
		if checksums.Files[name] != hex.EncodeToString(sum[:]) {
			return validatedPackage{}, fmt.Errorf("%w: checksum %s", ErrInvalidPackage, name)
		}
		if name == "manifest.json" {
			continue
		}
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		scanner.Buffer(make([]byte, 64*1024), int(limits.MaxFileBytes))
		last := ""
		for scanner.Scan() {
			count++
			if count > limits.MaxRecords {
				return validatedPackage{}, ErrImportResourceExhausted
			}
			line := scanner.Bytes()
			var r record
			if e := strictJSON(line, &r); e != nil {
				return validatedPackage{}, fmt.Errorf("%w: %s: %v", ErrInvalidPackage, name, e)
			}
			canonical, _ := json.Marshal(r)
			if string(canonical) != string(line) {
				return validatedPackage{}, fmt.Errorf("%w: non-canonical record", ErrInvalidPackage)
			}
			if r.ObjectID == "" || !memberTypes[name][r.ObjectType] || r.Data == nil {
				return validatedPackage{}, fmt.Errorf("%w: record identity", ErrInvalidPackage)
			}
			if !validDataFields(r.ObjectType, r.Data) {
				return validatedPackage{}, fmt.Errorf("%w: unknown %s field", ErrInvalidPackage, r.ObjectType)
			}
			order := r.ObjectID + "\x00" + r.ObjectType
			if order <= last {
				return validatedPackage{}, fmt.Errorf("%w: record order", ErrInvalidPackage)
			}
			last = order
			key := r.ObjectType + "\x00" + r.ObjectID
			if global[key] {
				return validatedPackage{}, fmt.Errorf("%w: duplicate ID", ErrInvalidPackage)
			}
			global[key] = true
			records[name] = append(records[name], r)
		}
		if e := scanner.Err(); e != nil {
			return validatedPackage{}, fmt.Errorf("%w: record limit", ErrImportResourceExhausted)
		}
		if manifest.ObjectCounts[name] != len(records[name]) {
			return validatedPackage{}, fmt.Errorf("%w: object count", ErrInvalidPackage)
		}
	}
	if manifest.TotalSize != sumRecordBytes(records) || manifest.FileChecksums == nil || !equalChecksums(manifest.FileChecksums, checksums.Files) {
		return validatedPackage{}, fmt.Errorf("%w: manifest integrity", ErrInvalidPackage)
	}
	if err := validateGraph(records, global); err != nil {
		return validatedPackage{}, err
	}
	return validatedPackage{manifest, records, count, hex.EncodeToString(packageHash.Sum(nil))}, nil
}

func validateInputPath(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for current := absolute; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: input path contains symlink", ErrInvalidPackage)
		}
		if current == filepath.Dir(current) {
			return nil
		}
	}
}

func strictJSON(data []byte, v any) error {
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}
func classifyManifest(data []byte, parse error) error {
	if strings.HasPrefix(string(data), "SQLite format 3") {
		return ErrUnsupportedFormat
	}
	var probe struct {
		FormatFamily  string `json:"format_family"`
		FormatVersion int    `json:"format_version"`
	}
	if json.Unmarshal(data, &probe) != nil || probe.FormatFamily != "dmem-logical" {
		return ErrUnsupportedFormat
	}
	if probe.FormatVersion != 1 {
		return ErrIncompatibleVersion
	}
	return fmt.Errorf("%w: manifest: %v", ErrInvalidPackage, parse)
}
func validateGraph(records map[string][]record, ids map[string]bool) error {
	memoryRevision := map[string]string{}
	parents := map[string]string{}
	for _, r := range records["memories.jsonl"] {
		if r.ObjectType == "memory" {
			memoryRevision[r.ObjectID] = text(r.Data["current_revision_id"])
		}
	}
	for _, r := range records["revisions.jsonl"] {
		if r.ObjectType != "revision" {
			continue
		}
		if !ids["memory\x00"+text(r.Data["memory_id"])] {
			return fmt.Errorf("%w: revision memory reference", ErrInvalidPackage)
		}
		if p := text(r.Data["parent_revision_id"]); p != "" {
			parents[r.ObjectID] = p
			if !ids["revision\x00"+p] {
				return fmt.Errorf("%w: revision parent", ErrInvalidPackage)
			}
		}
	}
	for m, r := range memoryRevision {
		if !ids["revision\x00"+r] {
			return fmt.Errorf("%w: current revision %s", ErrInvalidPackage, m)
		}
	}
	for id := range parents {
		seen := map[string]bool{}
		for p := id; p != ""; p = parents[p] {
			if seen[p] {
				return fmt.Errorf("%w: revision cycle", ErrInvalidPackage)
			}
			seen[p] = true
		}
	}
	for _, r := range records["relations.jsonl"] {
		if r.ObjectType != "relation" {
			continue
		}
		for _, field := range []string{"source_revision_id", "target_revision_id"} {
			id := text(r.Data[field])
			if !ids["revision\x00"+id] && !ids["external_reference_stub\x00"+id] {
				return fmt.Errorf("%w: relation endpoint", ErrInvalidPackage)
			}
		}
	}
	references := map[string]map[string]string{
		"raw_event":               {"space_id": "space"},
		"memory":                  {"space_id": "space", "current_revision_id": "revision"},
		"memory_state_event":      {"memory_id": "memory", "revision_id": "revision"},
		"memory_evidence":         {"revision_id": "revision", "event_id": "raw_event"},
		"entity":                  {"space_id": "space"},
		"entity_event":            {"entity_id": "entity"},
		"entity_alias":            {"entity_id": "entity"},
		"entity_alias_event":      {"alias_id": "entity_alias"},
		"entity_mention":          {"entity_id": "entity"},
		"revision_subject_entity": {"revision_id": "revision", "entity_id": "entity"},
		"revision_object_entity":  {"revision_id": "revision", "entity_id": "entity"},
		"relation_evidence":       {"relation_id": "relation"},
		"relation_state_event":    {"relation_id": "relation"},
		"entity_fact":             {"space_id": "space", "subject_entity_id": "entity", "source_memory_id": "memory", "source_revision_id": "revision"},
		"entity_fact_evidence":    {"fact_id": "entity_fact"},
		"entity_fact_state_event": {"fact_id": "entity_fact"},
		"conflict_case":           {"space_id": "space"},
		"conflict_participant":    {"memory_id": "memory", "revision_id": "revision"},
		"conflict_relation":       {"relation_id": "relation"},
		"conflict_resolution":     {"case_id": "conflict_case"},
		"conflict_resolution_ref": {"resolution_event_id": "conflict_resolution"},
		"feedback_event":          {"memory_id": "memory", "revision_id": "revision"},
		"aggregate_descriptor":    {"memory_id": "memory"},
	}
	for _, member := range dataMembers {
		for _, r := range records[member] {
			for field, targetType := range references[r.ObjectType] {
				id := text(r.Data[field])
				if id == "" || !ids[targetType+"\x00"+id] {
					return fmt.Errorf("%w: %s %s reference", ErrInvalidPackage, r.ObjectType, field)
				}
			}
			if r.ObjectType == "entity_mention" {
				revision, event := text(r.Data["revision_id"]), text(r.Data["event_id"])
				if revision != "" && !ids["revision\x00"+revision] || event != "" && !ids["raw_event\x00"+event] {
					return fmt.Errorf("%w: entity mention endpoint", ErrInvalidPackage)
				}
			}
			if r.ObjectType == "entity_fact" {
				if object := text(r.Data["object_entity_id"]); object != "" && !ids["entity\x00"+object] {
					return fmt.Errorf("%w: entity fact object", ErrInvalidPackage)
				}
			}
		}
	}
	return nil
}

func deterministicLocalID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	x := hex.EncodeToString(h.Sum(nil))
	return fmt.Sprintf("%s-%s-4%s-a%s-%s", x[:8], x[8:12], x[13:16], x[17:20], x[20:32])
}
func hashRecord(r record) string {
	b, _ := json.Marshal(r)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func rewriteReferences(data map[string]any, ids map[string]string) {
	types := map[string]string{"space_id": "space", "parent_space_id": "space", "memory_id": "memory", "source_memory_id": "memory", "target_memory_id": "memory", "revision_id": "revision", "current_revision_id": "revision", "parent_revision_id": "revision", "restored_from_revision_id": "revision", "source_revision_id": "revision", "target_revision_id": "revision", "event_id": "raw_event", "entity_id": "entity", "subject_entity_id": "entity", "object_entity_id": "entity", "relation_id": "relation", "alias_id": "entity_alias", "fact_id": "entity_fact"}
	for field, typ := range types {
		if id := text(data[field]); id != "" {
			if local := ids[typ+"\x00"+id]; local != "" {
				data["local_"+field] = local
			}
		}
	}
}
func findRecord(rs []record, typ, id string) record {
	for _, r := range rs {
		if r.ObjectType == typ && r.ObjectID == id {
			return r
		}
	}
	return record{}
}
func findRecordByMemory(items []ImportPlanItem, memory string) ImportPlanItem {
	for _, i := range items {
		if i.ObjectType == "revision" && text(i.record.Data["memory_id"]) == memory {
			return i
		}
	}
	return ImportPlanItem{}
}
func firstLocal(items []ImportPlanItem, typ string) string {
	for _, i := range items {
		if i.ObjectType == typ {
			return i.LocalID
		}
	}
	return ""
}
func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func mapRevisionItem(items []ImportPlanItem, sourceID, localID, action string) {
	for index := range items {
		if items[index].ObjectType == "revision" && items[index].SourceID == sourceID {
			items[index].LocalID = localID
			items[index].Action = action
			return
		}
	}
}
func referencesProposal(data map[string]any, memories, revisions, facts map[string]bool) bool {
	for _, field := range []string{"memory_id", "source_memory_id", "target_memory_id"} {
		if memories[text(data[field])] {
			return true
		}
	}
	for _, field := range []string{"revision_id", "source_revision_id", "target_revision_id"} {
		if revisions[text(data[field])] {
			return true
		}
	}
	if facts[text(data["fact_id"])] {
		return true
	}
	return false
}
func orderImportItems(items []ImportPlanItem) []ImportPlanItem {
	byMemory := map[string][]ImportPlanItem{}
	memories := []ImportPlanItem{}
	remaining := []ImportPlanItem{}
	for _, item := range items {
		switch item.ObjectType {
		case "memory":
			memories = append(memories, item)
		case "revision":
			byMemory[text(item.record.Data["memory_id"])] = append(byMemory[text(item.record.Data["memory_id"])], item)
		default:
			remaining = append(remaining, item)
		}
	}
	ordered := make([]ImportPlanItem, 0, len(items))
	for _, item := range remaining {
		if item.ObjectType == "space" || item.ObjectType == "raw_event" {
			ordered = append(ordered, item)
		}
	}
	for _, memory := range memories {
		ordered = append(ordered, memory)
		ordered = append(ordered, byMemory[memory.SourceID]...)
	}
	for _, item := range remaining {
		if item.ObjectType != "space" && item.ObjectType != "raw_event" && !deferredGraphAuthority(item.ObjectType) {
			ordered = append(ordered, item)
		}
	}
	for _, typ := range []string{"revision_subject_entity", "revision_object_entity", "entity_fact", "entity_fact_evidence", "entity_fact_state_event"} {
		for _, item := range remaining {
			if item.ObjectType == typ {
				ordered = append(ordered, item)
			}
		}
	}
	return ordered
}

func deferredGraphAuthority(typ string) bool {
	switch typ {
	case "revision_subject_entity", "revision_object_entity", "entity_fact", "entity_fact_evidence", "entity_fact_state_event":
		return true
	default:
		return false
	}
}
func importBatchEnd(items []ImportPlanItem, start, size int) int {
	end := start + size
	if end > len(items) {
		return len(items)
	}
	for index := start; index < end; index++ {
		if items[index].ObjectType != "memory" {
			continue
		}
		for end < len(items) && items[end].ObjectType == "revision" && text(items[end].record.Data["memory_id"]) == items[index].SourceID {
			end++
		}
	}
	return end
}
func sumRecordBytes(records map[string][]record) int64 {
	var n int64
	for _, name := range dataMembers {
		for _, r := range records[name] {
			b, _ := json.Marshal(r)
			n += int64(len(b) + 1)
		}
	}
	return n
}
func equalChecksums(manifest, all map[string]string) bool {
	if len(manifest) != len(dataMembers) {
		return false
	}
	for _, name := range dataMembers {
		if manifest[name] == "" || manifest[name] != all[name] {
			return false
		}
	}
	return true
}
func mapImportError(err error) error {
	if errors.Is(err, storage.ErrIdempotencyConflict) {
		return ErrImportIdempotencyConflict
	}
	return err
}

var objectDataFields = map[string]string{
	"space":                   "space_id parent_space_id space_type external_id display_name created_at",
	"raw_event":               "event_id operation_id space_id source_type source_name source_event_id caller_id occurred_at received_at processing_status metadata_json created_at source_stream_key sequence content_id content_kind content_text reference_uri media_type content_hash size_bytes declared_size declared_hash content_created_at",
	"memory":                  "memory_id space_id current_revision_id current_state frozen sensitivity created_at updated_at",
	"memory_state_event":      "state_event_id memory_id revision_id operation_id from_state to_state from_frozen to_frozen reason created_by created_at",
	"revision":                "revision_id memory_id parent_revision_id restored_from_revision_id valid_from valid_until confidence kind display_content canonical_content canonical_hash event_type event_time_start event_time_end time_precision time_source source_speaker language sensitivity specificity created_by processing_version processor_identity created_at",
	"revision_subject_entity": "revision_id entity_order entity_id",
	"revision_object_entity":  "revision_id entity_order entity_id",
	"memory_evidence":         "evidence_id revision_id event_id role start_offset end_offset created_at",
	"entity":                  "entity_id space_id entity_type canonical_name normalized_name normalization_version state sensitivity created_by processing_version created_at updated_at",
	"entity_event":            "entity_event_id entity_id operation_id event_type from_canonical_name to_canonical_name from_state to_state normalization_version reason created_by created_at",
	"entity_alias":            "alias_id entity_id alias normalized_alias normalization_version source state created_by created_at updated_at",
	"entity_alias_event":      "alias_event_id alias_id operation_id from_state to_state reason created_by created_at",
	"entity_mention":          "mention_id entity_id memory_id revision_id event_id start_offset end_offset mention_type source surface_text confidence created_by processing_version created_at",
	"relation":                "relation_id space_id relation_type source_memory_id source_revision_id target_memory_id target_revision_id state source confidence specification_hash created_by processing_version created_at updated_at",
	"relation_evidence":       "evidence_id relation_id evidence_type event_id memory_id revision_id start_offset end_offset note created_by created_at",
	"relation_state_event":    "state_event_id relation_id operation_id from_state to_state reason created_by created_at",
	"entity_fact":             "fact_id space_id subject_entity_id predicate object_entity_id object_literal object_literal_type source_memory_id source_revision_id confidence state source specification_hash created_by processing_version valid_from valid_until created_at updated_at",
	"entity_fact_evidence":    "evidence_id fact_id evidence_type event_id memory_id revision_id start_offset end_offset note created_by created_at",
	"entity_fact_state_event": "state_event_id fact_id operation_id from_state to_state reason created_by created_at",
	"external_reference_stub": "object_id object_type referenced_by",
	"feedback_event":          "feedback_id operation_id caller_id memory_id revision_id feedback_purpose feedback_type reason_code note search_operation_id query_hash query_type entity_ids_json ranking_identity feature_identity context_identity result_rank selection_state matched_signals_json answer_identity created_at",
	"aggregate_descriptor":    "aggregate_id memory_id aggregate_kind policy_version processing_version sensitivity created_by created_at",
	"conflict_case":           "case_id space_id state opened_by processing_version created_at closed_at",
	"conflict_participant":    "case_id participant_order memory_id revision_id input_state input_sensitivity created_at",
	"conflict_relation":       "case_id relation_id created_at",
	"conflict_resolution":     "resolution_event_id case_id operation_id resolution_type from_state to_state reason resolved_by created_at",
	"conflict_resolution_ref": "result_ref_id resolution_event_id result_type result_id created_at",
}

func validDataFields(typ string, data map[string]any) bool {
	fields, ok := objectDataFields[typ]
	if !ok {
		return false
	}
	allowed := makeSet(strings.Fields(fields))
	if len(data) != len(allowed) {
		return false
	}
	for key := range data {
		if !allowed[key] {
			return false
		}
	}
	return true
}
