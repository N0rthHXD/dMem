package retrieval

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/N0rthHXD/dMem/internal/gateway"
)

const cursorVersion = 1

// CursorState is the validated, gateway-owned continuation state passed to the
// recall adapter. The encoded cursor remains opaque outside Retrieval.
type CursorState struct {
	Version       int           `json:"v"`
	QueryHash     string        `json:"q"`
	Binding       CursorBinding `json:"g"`
	Limit         int           `json:"l"`
	MaxChars      int           `json:"b"`
	ScoreBits     string        `json:"s"`
	MemoryID      string        `json:"m"`
	RevisionID    string        `json:"r"`
	ReferenceTime string        `json:"rt"`
}

func decodeCursor(value string) (*CursorState, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, invalidCursor()
	}
	var state CursorState
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validCursorState(state) {
		return nil, invalidCursor()
	}
	return &state, nil
}

func validCursorState(state CursorState) bool {
	b := state.Binding
	if state.Version != cursorVersion || state.QueryHash == "" || state.MemoryID == "" || state.RevisionID == "" || state.ReferenceTime == "" ||
		b.Schema == "" || b.QueryPlan == "" || b.Membership == "" || b.Ranking == "" || b.Graph == "" || b.Context == "" || b.Feedback == "" {
		return false
	}
	bits, err := strconv.ParseUint(state.ScoreBits, 16, 64)
	if err != nil || math.IsNaN(math.Float64frombits(bits)) || math.IsInf(math.Float64frombits(bits), 0) {
		return false
	}
	_, err = time.Parse(time.RFC3339Nano, state.ReferenceTime)
	return err == nil
}

func (s *ServiceImpl) EncodeCursor(params RecallParams, binding CursorBinding, position CursorPosition) (string, error) {
	state := CursorState{
		Version: cursorVersion, QueryHash: cursorQueryHash(params), Binding: binding,
		Limit: params.Limit, MaxChars: params.MaxChars,
		ScoreBits: strconv.FormatUint(math.Float64bits(position.Score), 16),
		MemoryID:  position.MemoryID, RevisionID: position.RevisionID, ReferenceTime: params.ReferenceTime,
	}
	if !validCursorState(state) {
		return "", invalidCursor()
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", gateway.WrapError(gateway.ErrorInternal, "cursor encoding failed", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func (s *ServiceImpl) PrepareCursor(params RecallParams) (RecallParams, error) {
	if params.Cursor == "" {
		return params, nil
	}
	state, err := decodeCursor(params.Cursor)
	if err != nil {
		return RecallParams{}, err
	}
	params.CursorState = state
	if !params.ReferenceTimeSet {
		params.ReferenceTime = state.ReferenceTime
	}
	return params, nil
}

func validateCursor(state *CursorState, params RecallParams, binding CursorBinding) error {
	if state == nil {
		return nil
	}
	if state.QueryHash != cursorQueryHash(params) || state.ReferenceTime != params.ReferenceTime || state.Binding != binding ||
		state.Limit != params.Limit || state.MaxChars != params.MaxChars {
		return invalidCursor()
	}
	return nil
}

func cursorStart(candidates []Candidate, state *CursorState) (int, error) {
	if state == nil {
		return 0, nil
	}
	for i := range candidates {
		if candidates[i].MemoryID == state.MemoryID && candidates[i].RevisionID == state.RevisionID &&
			strconv.FormatUint(math.Float64bits(candidates[i].Score), 16) == state.ScoreBits {
			return i + 1, nil
		}
	}
	return 0, invalidCursor()
}

func cursorQueryHash(params RecallParams) string {
	spaces := sortedCursorValues(params.SpaceIDs)
	types := sortedCursorValues(params.MemoryTypes)
	states := sortedCursorValues(params.States)
	canonical := struct {
		Query, Plan, Reference, Intent, GraphPolicy                      string
		Spaces, Types, States, Channels, Executed                        []string
		TypesSet, Ancestors, Evidence, ExcludeStale, SkipDiversification bool
		Semantic, Limit, MaxChars, MaxTokens, MaxSources, MaxSensitive   int
	}{
		params.Query, params.Plan.Identity(), params.ReferenceTime, params.Intent, string(params.GraphPolicy),
		spaces, types, states, copyStrings(params.Channels), copyStrings(params.EffectiveChannels),
		params.MemoryTypesSet, params.IncludeAncestors, params.IncludeEvidence, params.ExcludeStaleAggregates, params.SkipDiversification,
		params.SemanticVersion, params.Limit, params.MaxChars, params.MaxContextTokens, params.MaxSources, params.MaxSensitive,
	}
	encoded, _ := json.Marshal(canonical)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func sortedCursorValues(values []string) []string {
	result := copyStrings(values)
	sort.Strings(result)
	return result
}

func invalidCursor() error {
	return gateway.NewError(gateway.ErrorInvalidInput, "cursor is invalid")
}
