package vector

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const FormatVersion = 1

var (
	ErrInvalidArgument  = errors.New("invalid vector index argument")
	ErrVersionNotFound  = errors.New("vector index version not found")
	ErrNoActiveVersion  = errors.New("no active vector index version")
	ErrIdentityMismatch = errors.New("vector index identity mismatch")
)

type Metadata struct {
	MemoryID     string `json:"memory_id"`
	RevisionID   string `json:"revision_id"`
	SpaceID      string `json:"space_id"`
	ContentHash  string `json:"content_hash"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Dimension    int    `json:"dimension"`
	IndexVersion int    `json:"index_version"`
}

type Record struct {
	Metadata Metadata  `json:"metadata"`
	Vector   []float64 `json:"vector"`
}

type Snapshot struct {
	Provider  string
	Model     string
	Dimension int
	Records   []Record
}

type Result struct {
	Metadata Metadata
	Score    float64
}

type Identity struct {
	MemoryID, RevisionID, ContentHash string
}

type State string

const (
	StateAbsent   State = "absent"
	StateBuilding State = "building"
	StateReady    State = "ready"
	StateDegraded State = "degraded"
	StateStale    State = "stale"
)

type Status struct {
	State         State
	Version       int
	ActiveVersion int
	FormatVersion int
	Provider      string
	Model         string
	Dimension     int
	RecordCount   int
	Stale         bool
	Err           error
}

type Index struct {
	mu            sync.RWMutex
	dir           string
	versions      map[int]*indexFile
	versionErrors map[int]error
	activeVersion int
	activeErr     error
	building      map[int]bool
}

type indexFile struct {
	FormatVersion int          `json:"format_version"`
	IndexVersion  int          `json:"index_version"`
	Provider      string       `json:"provider"`
	Model         string       `json:"model"`
	Dimension     int          `json:"dimension"`
	Records       []diskRecord `json:"records"`
}

type diskRecord struct {
	MemoryID    string    `json:"memory_id"`
	RevisionID  string    `json:"revision_id"`
	SpaceID     string    `json:"space_id"`
	ContentHash string    `json:"content_hash"`
	Vector      []float64 `json:"vector"`
}

type activeFile struct {
	FormatVersion int `json:"format_version"`
	IndexVersion  int `json:"index_version"`
}

func Open(dir string) (*Index, error) {
	if dir == "" {
		return nil, fmt.Errorf("%w: directory is required", ErrInvalidArgument)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create vector index directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("inspect vector index directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("vector index path must be a directory, not a symlink")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secure vector index directory: %w", err)
	}

	idx := &Index{
		dir:           dir,
		versions:      make(map[int]*indexFile),
		versionErrors: make(map[int]error),
		building:      make(map[int]bool),
	}
	active, err := readJSON[activeFile](filepath.Join(dir, "active.json"))
	if errors.Is(err, os.ErrNotExist) {
		return idx, nil
	}
	if err != nil {
		idx.activeErr = fmt.Errorf("read active vector index metadata: %w", err)
		return idx, nil
	}
	if active.FormatVersion != FormatVersion || active.IndexVersion < 1 {
		idx.activeErr = errors.New("invalid active vector index metadata")
		return idx, nil
	}
	idx.activeVersion = active.IndexVersion
	if _, err := idx.loadVersionLocked(active.IndexVersion); err != nil {
		idx.activeErr = err
	}
	return idx, nil
}

func (i *Index) Upsert(memoryID, revisionID string, vector []float64, metadata Metadata) error {
	if memoryID != metadata.MemoryID || revisionID != metadata.RevisionID {
		return fmt.Errorf("%w: metadata IDs do not match arguments", ErrInvalidArgument)
	}
	if err := validateMetadata(metadata); err != nil {
		return err
	}
	if err := validateVector(vector, metadata.Dimension); err != nil {
		return err
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	if i.building[metadata.IndexVersion] {
		return fmt.Errorf("%w: version %d is building", ErrInvalidArgument, metadata.IndexVersion)
	}
	file, err := i.loadVersionLocked(metadata.IndexVersion)
	if errors.Is(err, ErrVersionNotFound) {
		file = &indexFile{FormatVersion: FormatVersion, IndexVersion: metadata.IndexVersion, Provider: metadata.Provider, Model: metadata.Model, Dimension: metadata.Dimension, Records: []diskRecord{}}
	} else if err != nil {
		return err
	} else {
		file = cloneIndexFile(file)
	}
	if file.Provider != metadata.Provider || file.Model != metadata.Model || file.Dimension != metadata.Dimension {
		return ErrIdentityMismatch
	}
	record := toDiskRecord(metadata, vector)
	replaced := false
	for n := range file.Records {
		if file.Records[n].MemoryID == memoryID && file.Records[n].RevisionID == revisionID {
			file.Records[n] = record
			replaced = true
			break
		}
	}
	if !replaced {
		file.Records = append(file.Records, record)
	}
	sortRecords(file.Records)
	if err := i.writeVersionLocked(file); err != nil {
		return err
	}
	return nil
}

func (i *Index) Delete(memoryID, revisionID string) error {
	if memoryID == "" || revisionID == "" {
		return fmt.Errorf("%w: memory and revision IDs are required", ErrInvalidArgument)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.activeErr != nil {
		return i.activeErr
	}
	if i.activeVersion == 0 {
		return ErrNoActiveVersion
	}
	file, err := i.loadVersionLocked(i.activeVersion)
	if err != nil {
		return err
	}
	file = cloneIndexFile(file)
	for n := range file.Records {
		if file.Records[n].MemoryID == memoryID && file.Records[n].RevisionID == revisionID {
			file.Records = append(file.Records[:n], file.Records[n+1:]...)
			return i.writeVersionLocked(file)
		}
	}
	return nil
}

func (i *Index) Search(vector []float64, allowedSpaceIDs []string, eligible []Identity, limit, indexVersion int) ([]Result, error) {
	if limit < 1 || indexVersion < 1 {
		return nil, fmt.Errorf("%w: positive limit and index version are required", ErrInvalidArgument)
	}
	i.mu.Lock()
	file, err := i.loadVersionLocked(indexVersion)
	if err != nil {
		i.mu.Unlock()
		return nil, err
	}
	copyFile := cloneIndexFile(file)
	i.mu.Unlock()
	if err := validateVector(vector, copyFile.Dimension); err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(allowedSpaceIDs))
	for _, id := range allowedSpaceIDs {
		if id == "" {
			return nil, fmt.Errorf("%w: allowed space ID is empty", ErrInvalidArgument)
		}
		allowed[id] = struct{}{}
	}
	identities := make(map[string]struct{}, len(eligible))
	for _, identity := range eligible {
		if identity.MemoryID == "" || identity.RevisionID == "" || identity.ContentHash == "" {
			return nil, fmt.Errorf("%w: eligible identity is incomplete", ErrInvalidArgument)
		}
		identities[identity.MemoryID+"\x00"+identity.RevisionID+"\x00"+identity.ContentHash] = struct{}{}
	}
	results := make([]Result, 0, min(limit, len(copyFile.Records)))
	for _, record := range copyFile.Records {
		if _, ok := allowed[record.SpaceID]; !ok {
			continue
		}
		if _, ok := identities[record.MemoryID+"\x00"+record.RevisionID+"\x00"+record.ContentHash]; !ok {
			continue
		}
		score := cosine(vector, record.Vector)
		results = append(results, Result{Metadata: recordMetadata(copyFile, record), Score: score})
	}
	sort.Slice(results, func(a, b int) bool {
		if results[a].Score != results[b].Score {
			return results[a].Score > results[b].Score
		}
		if results[a].Metadata.MemoryID != results[b].Metadata.MemoryID {
			return results[a].Metadata.MemoryID < results[b].Metadata.MemoryID
		}
		return results[a].Metadata.RevisionID < results[b].Metadata.RevisionID
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (i *Index) Build(version int, snapshot Snapshot) (err error) {
	if version < 1 || snapshot.Provider == "" || snapshot.Model == "" || snapshot.Dimension < 1 {
		return fmt.Errorf("%w: invalid build identity", ErrInvalidArgument)
	}
	i.mu.Lock()
	if version == i.activeVersion {
		i.mu.Unlock()
		return fmt.Errorf("%w: cannot build active version", ErrInvalidArgument)
	}
	if i.building[version] {
		i.mu.Unlock()
		return fmt.Errorf("%w: version %d is already building", ErrInvalidArgument, version)
	}
	i.building[version] = true
	i.mu.Unlock()
	defer func() {
		i.mu.Lock()
		delete(i.building, version)
		i.mu.Unlock()
	}()

	file := &indexFile{FormatVersion: FormatVersion, IndexVersion: version, Provider: snapshot.Provider, Model: snapshot.Model, Dimension: snapshot.Dimension, Records: make([]diskRecord, 0, len(snapshot.Records))}
	seen := make(map[string]struct{}, len(snapshot.Records))
	for _, record := range snapshot.Records {
		metadata := record.Metadata
		if err := validateMetadata(metadata); err != nil {
			return err
		}
		if metadata.IndexVersion != version || metadata.Provider != snapshot.Provider || metadata.Model != snapshot.Model || metadata.Dimension != snapshot.Dimension {
			return ErrIdentityMismatch
		}
		if err := validateVector(record.Vector, snapshot.Dimension); err != nil {
			return err
		}
		key := metadata.MemoryID + "\x00" + metadata.RevisionID
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%w: duplicate memory revision", ErrInvalidArgument)
		}
		seen[key] = struct{}{}
		file.Records = append(file.Records, toDiskRecord(metadata, record.Vector))
	}
	sortRecords(file.Records)

	i.mu.Lock()
	defer i.mu.Unlock()
	return i.writeVersionLocked(file)
}

func (i *Index) Activate(version int) error {
	if version < 1 {
		return fmt.Errorf("%w: positive index version is required", ErrInvalidArgument)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.building[version] {
		return fmt.Errorf("%w: version %d is building", ErrInvalidArgument, version)
	}
	delete(i.versions, version)
	delete(i.versionErrors, version)
	if _, err := i.loadVersionLocked(version); err != nil {
		return err
	}
	if err := writeJSONAtomic(i.dir, "active.json", activeFile{FormatVersion: FormatVersion, IndexVersion: version}); err != nil {
		return fmt.Errorf("activate vector index version %d: %w", version, err)
	}
	i.activeVersion = version
	i.activeErr = nil
	return nil
}

func (i *Index) Deactivate(version int) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.activeVersion != version {
		return nil
	}
	if err := os.Remove(filepath.Join(i.dir, "active.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	i.activeVersion = 0
	i.activeErr = nil
	return nil
}

func (i *Index) Drop(version int) error {
	if version < 1 {
		return fmt.Errorf("%w: positive index version is required", ErrInvalidArgument)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.building[version] {
		return fmt.Errorf("%w: version %d is building", ErrInvalidArgument, version)
	}
	if version == i.activeVersion {
		return fmt.Errorf("%w: cannot drop active version", ErrInvalidArgument)
	}
	err := os.Remove(i.versionPath(version))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("drop vector index version %d: %w", version, err)
	}
	delete(i.versions, version)
	delete(i.versionErrors, version)
	return syncDir(i.dir)
}

func (i *Index) Status(version int) Status {
	i.mu.Lock()
	defer i.mu.Unlock()
	status := Status{Version: version, ActiveVersion: i.activeVersion, FormatVersion: FormatVersion}
	if version < 1 {
		status.State = StateDegraded
		status.Err = fmt.Errorf("%w: positive index version is required", ErrInvalidArgument)
		return status
	}
	if i.building[version] {
		status.State = StateBuilding
		return status
	}
	if i.activeErr != nil && version == i.activeVersion {
		status.State = StateDegraded
		status.Err = i.activeErr
		return status
	}
	file, err := i.loadVersionLocked(version)
	if errors.Is(err, ErrVersionNotFound) {
		status.State = StateAbsent
		return status
	}
	if err != nil {
		status.State = StateDegraded
		status.Err = err
		return status
	}
	status.Provider = file.Provider
	status.Model = file.Model
	status.Dimension = file.Dimension
	status.RecordCount = len(file.Records)
	status.Stale = i.activeVersion > version
	if status.Stale {
		status.State = StateStale
	} else {
		status.State = StateReady
	}
	return status
}

func (i *Index) writeVersionLocked(file *indexFile) error {
	if err := validateIndexFile(file); err != nil {
		return err
	}
	name := filepath.Base(i.versionPath(file.IndexVersion))
	if err := writeJSONAtomic(i.dir, name, file); err != nil {
		return fmt.Errorf("write vector index version %d: %w", file.IndexVersion, err)
	}
	loaded, err := readIndexFile(i.versionPath(file.IndexVersion), file.IndexVersion)
	if err != nil {
		return fmt.Errorf("validate vector index version %d: %w", file.IndexVersion, err)
	}
	i.versions[file.IndexVersion] = loaded
	delete(i.versionErrors, file.IndexVersion)
	return nil
}

func (i *Index) loadVersionLocked(version int) (*indexFile, error) {
	if file := i.versions[version]; file != nil {
		return file, nil
	}
	if err := i.versionErrors[version]; err != nil {
		return nil, err
	}
	file, err := readIndexFile(i.versionPath(version), version)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrVersionNotFound
	}
	if err != nil {
		err = fmt.Errorf("load vector index version %d: %w", version, err)
		i.versionErrors[version] = err
		return nil, err
	}
	i.versions[version] = file
	return file, nil
}

func (i *Index) versionPath(version int) string {
	return filepath.Join(i.dir, fmt.Sprintf("index-%d.json", version))
}

func validateIndexFile(file *indexFile) error {
	if file.FormatVersion != FormatVersion || file.IndexVersion < 1 || file.Provider == "" || file.Model == "" || file.Dimension < 1 || file.Records == nil {
		return errors.New("invalid vector index file metadata")
	}
	seen := make(map[string]struct{}, len(file.Records))
	for _, record := range file.Records {
		metadata := recordMetadata(file, record)
		if err := validateMetadata(metadata); err != nil {
			return err
		}
		if err := validateVector(record.Vector, file.Dimension); err != nil {
			return err
		}
		key := record.MemoryID + "\x00" + record.RevisionID
		if _, ok := seen[key]; ok {
			return errors.New("duplicate memory revision in vector index file")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateMetadata(metadata Metadata) error {
	if metadata.MemoryID == "" || metadata.RevisionID == "" || metadata.SpaceID == "" || metadata.ContentHash == "" || metadata.Provider == "" || metadata.Model == "" || metadata.Dimension < 1 || metadata.IndexVersion < 1 {
		return fmt.Errorf("%w: incomplete vector metadata", ErrInvalidArgument)
	}
	return nil
}

func validateVector(vector []float64, dimension int) error {
	if len(vector) != dimension {
		return fmt.Errorf("%w: vector dimension is %d, want %d", ErrInvalidArgument, len(vector), dimension)
	}
	nonzero := false
	for _, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%w: vector contains a non-finite value", ErrInvalidArgument)
		}
		nonzero = nonzero || value != 0
	}
	if !nonzero {
		return fmt.Errorf("%w: vector norm must be non-zero", ErrInvalidArgument)
	}
	return nil
}

func readIndexFile(path string, version int) (*indexFile, error) {
	file, err := readJSON[indexFile](path)
	if err != nil {
		return nil, err
	}
	if file.IndexVersion != version {
		return nil, errors.New("vector index filename and version do not match")
	}
	if err := validateIndexFile(&file); err != nil {
		return nil, err
	}
	return &file, nil
}

func readJSON[T any](path string) (T, error) {
	var value T
	info, err := os.Lstat(path)
	if err != nil {
		return value, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return value, errors.New("index metadata must be a regular file, not a symlink")
	}
	file, err := os.Open(path)
	if err != nil {
		return value, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return value, errors.New("index file contains trailing data")
		}
		return value, err
	}
	return value, nil
}

func writeJSONAtomic(dir, name string, value any) error {
	temp, err := os.CreateTemp(dir, ".vector-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	encoder := json.NewEncoder(temp)
	if err := encoder.Encode(value); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, filepath.Join(dir, name)); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(dir, name), 0o600); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func toDiskRecord(metadata Metadata, vector []float64) diskRecord {
	return diskRecord{MemoryID: metadata.MemoryID, RevisionID: metadata.RevisionID, SpaceID: metadata.SpaceID, ContentHash: metadata.ContentHash, Vector: append([]float64(nil), vector...)}
}

func recordMetadata(file *indexFile, record diskRecord) Metadata {
	return Metadata{MemoryID: record.MemoryID, RevisionID: record.RevisionID, SpaceID: record.SpaceID, ContentHash: record.ContentHash, Provider: file.Provider, Model: file.Model, Dimension: file.Dimension, IndexVersion: file.IndexVersion}
}

func cloneIndexFile(file *indexFile) *indexFile {
	clone := *file
	clone.Records = make([]diskRecord, len(file.Records))
	for n, record := range file.Records {
		clone.Records[n] = record
		clone.Records[n].Vector = append([]float64(nil), record.Vector...)
	}
	return &clone
}

func sortRecords(records []diskRecord) {
	sort.Slice(records, func(a, b int) bool {
		if records[a].MemoryID != records[b].MemoryID {
			return records[a].MemoryID < records[b].MemoryID
		}
		return records[a].RevisionID < records[b].RevisionID
	})
}

func cosine(a, b []float64) float64 {
	aScale, bScale := 0.0, 0.0
	for n := range a {
		aScale = max(aScale, math.Abs(a[n]))
		bScale = max(bScale, math.Abs(b[n]))
	}
	dot, aNorm, bNorm := 0.0, 0.0, 0.0
	for n := range a {
		aValue, bValue := a[n]/aScale, b[n]/bScale
		dot += aValue * bValue
		aNorm += aValue * aValue
		bNorm += bValue * bValue
	}
	return dot / math.Sqrt(aNorm*bNorm)
}
