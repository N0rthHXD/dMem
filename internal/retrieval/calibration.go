package retrieval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// Calibration replaces the fixed global semantic score floor with a versioned
// absolute floor plus a query-relative margin from the best result.
type Calibration struct {
	Version        string             `json:"version"`
	AbsoluteFloor  float64            `json:"absolute_floor"`
	TopMargin      float64            `json:"top_margin"`
	ByKind         map[string]float64 `json:"by_kind,omitempty"`
	ByLanguage     map[string]float64 `json:"by_language,omitempty"`
	ByQuestionType map[string]float64 `json:"by_question_type,omitempty"`
}

// DefaultCalibration matches the configured semantic calibration defaults.
func DefaultCalibration() Calibration {
	return Calibration{
		Version:        SemanticCalibrationVer,
		AbsoluteFloor:  0.35,
		TopMargin:      0.08,
		ByKind:         map[string]float64{},
		ByLanguage:     map[string]float64{},
		ByQuestionType: map[string]float64{},
	}
}

func (c Calibration) FloorForContext(kind, language string, question QuestionType) float64 {
	floor := c.FloorFor(kind)
	if value, ok := c.ByLanguage[language]; ok && value > floor {
		floor = value
	}
	if value, ok := c.ByQuestionType[string(question)]; ok && value > floor {
		floor = value
	}
	return floor
}

// FloorFor returns the effective absolute floor for a memory kind.
func (c Calibration) FloorFor(kind string) float64 {
	if value, ok := c.ByKind[kind]; ok && value > 0 {
		return value
	}
	if c.AbsoluteFloor > 0 {
		return c.AbsoluteFloor
	}
	return 0
}

// Identity returns a deterministic hash of the calibration contract.
func (c Calibration) Identity() string {
	keys := make([]string, 0, len(c.ByKind))
	for key := range c.ByKind {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	canonical := struct {
		Version       string
		AbsoluteFloor float64
		TopMargin     float64
		Kinds         []string
		Values        []float64
		ByLanguage    map[string]float64
		ByQuestion    map[string]float64
	}{c.Version, c.AbsoluteFloor, c.TopMargin, keys, nil, c.ByLanguage, c.ByQuestionType}
	for _, key := range keys {
		canonical.Values = append(canonical.Values, c.ByKind[key])
	}
	encoded, _ := json.Marshal(canonical)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

// Calibrate decides whether a raw similarity is admissible. It preserves the
// raw score and returns the reason when the candidate is rejected.
func (c Calibration) Calibrate(kind string, raw, top, percentile float64) (calibrated float64, admitted bool, reason string) {
	return c.CalibrateContext(kind, "", "", raw, top, percentile)
}

func (c Calibration) CalibrateContext(kind, language string, question QuestionType, raw, top, percentile float64) (calibrated float64, admitted bool, reason string) {
	calibrated = raw
	if raw < 0 {
		calibrated = 0
	}
	if calibrated > 1 {
		calibrated = 1
	}
	floor := c.FloorForContext(kind, language, question)
	if raw < floor {
		return calibrated, false, "below_absolute_floor"
	}
	if c.TopMargin > 0 && top > 0 && raw < top-c.TopMargin {
		return calibrated, false, "below_top_margin"
	}
	_ = percentile
	return calibrated, true, ""
}
