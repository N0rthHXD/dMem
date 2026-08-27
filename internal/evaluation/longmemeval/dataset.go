package longmemeval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

const DateLayout = "2006/01/02 (Mon) 15:04"

var questionTypes = map[string]bool{
	"single-session-user":       true,
	"single-session-assistant":  true,
	"single-session-preference": true,
	"multi-session":             true,
	"temporal-reasoning":        true,
	"knowledge-update":          true,
}

type Turn struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	HasAnswer bool   `json:"has_answer,omitempty"`
}

type Case struct {
	QuestionID         string   `json:"question_id"`
	QuestionType       string   `json:"question_type"`
	Question           string   `json:"question"`
	Answer             any      `json:"answer"`
	QuestionDate       string   `json:"question_date"`
	HaystackSessionIDs []string `json:"haystack_session_ids"`
	HaystackDates      []string `json:"haystack_dates"`
	HaystackSessions   [][]Turn `json:"haystack_sessions"`
	AnswerSessionIDs   []string `json:"answer_session_ids"`
}

func (c Case) AnswerString() string {
	if c.Answer == nil {
		return ""
	}
	if s, ok := c.Answer.(string); ok {
		return s
	}
	data, _ := json.Marshal(c.Answer)
	return string(data)
}

type Dataset struct {
	Cases  []Case
	SHA256 string
}

type Session struct {
	ID    string
	Date  time.Time
	Turns []Turn
}

func Load(path string) (Dataset, error) {
	file, err := os.Open(path)
	if err != nil {
		return Dataset{}, err
	}
	defer file.Close()
	return Decode(file)
}

func Decode(reader io.Reader) (Dataset, error) {
	hash := sha256.New()
	decoder := json.NewDecoder(io.TeeReader(reader, hash))
	decoder.DisallowUnknownFields()
	var cases []Case
	if err := decoder.Decode(&cases); err != nil {
		return Dataset{}, fmt.Errorf("decode dataset: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Dataset{}, errors.New("decode dataset: trailing JSON value")
	}
	if len(cases) == 0 {
		return Dataset{}, errors.New("dataset is empty")
	}
	seen := make(map[string]bool, len(cases))
	for i := range cases {
		if err := validateCase(&cases[i], seen); err != nil {
			return Dataset{}, fmt.Errorf("case %d: %w", i, err)
		}
	}
	return Dataset{Cases: cases, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func validateCase(item *Case, seen map[string]bool) error {
	if strings.TrimSpace(item.QuestionID) == "" || strings.TrimSpace(item.Question) == "" || strings.TrimSpace(item.AnswerString()) == "" {
		return errors.New("question_id, question, and answer are required")
	}
	if seen[item.QuestionID] {
		return fmt.Errorf("duplicate question_id %q", item.QuestionID)
	}
	seen[item.QuestionID] = true
	if !questionTypes[item.QuestionType] {
		return fmt.Errorf("unknown question_type %q", item.QuestionType)
	}
	if _, err := ParseDate(item.QuestionDate); err != nil {
		return fmt.Errorf("question_date: %w", err)
	}
	if len(item.HaystackSessionIDs) != len(item.HaystackDates) || len(item.HaystackDates) != len(item.HaystackSessions) {
		return errors.New("haystack_session_ids, haystack_dates, and haystack_sessions must have equal lengths")
	}
	if len(item.HaystackSessions) == 0 {
		return errors.New("haystack_sessions is empty")
	}
	sessions := make(map[string]bool, len(item.HaystackSessionIDs))
	for i, id := range item.HaystackSessionIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("session %d has an empty ID", i)
		}
		key := id + "\x00" + item.HaystackDates[i]
		if sessions[key] {
			return fmt.Errorf("session %d has a duplicate ID+date", i)
		}
		sessions[key] = true
		if _, err := ParseDate(item.HaystackDates[i]); err != nil {
			return fmt.Errorf("session %q date: %w", id, err)
		}
		if len(item.HaystackSessions[i]) == 0 {
			return fmt.Errorf("session %q is empty", id)
		}
		for j, turn := range item.HaystackSessions[i] {
			if turn.Role != "user" && turn.Role != "assistant" {
				return fmt.Errorf("session %q turn %d has invalid role %q", id, j, turn.Role)
			}
		}
	}
	sessionIDs := make(map[string]bool, len(item.HaystackSessionIDs))
	for _, id := range item.HaystackSessionIDs {
		sessionIDs[id] = true
	}
	for _, id := range item.AnswerSessionIDs {
		if !sessionIDs[id] {
			return fmt.Errorf("answer_session_id %q does not resolve", id)
		}
	}
	return nil
}

func ParseDate(value string) (time.Time, error) {
	parsed, err := time.ParseInLocation(DateLayout, value, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("must match %q: %w", DateLayout, err)
	}
	return parsed.UTC(), nil
}

func (item Case) Sessions() []Session {
	sessions := make([]Session, len(item.HaystackSessions))
	for i := range sessions {
		date, _ := ParseDate(item.HaystackDates[i])
		sessions[i] = Session{ID: item.HaystackSessionIDs[i], Date: date, Turns: item.HaystackSessions[i]}
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].Date.Before(sessions[j].Date) })
	return sessions
}

func (item Case) IsAbstention() bool {
	return strings.HasSuffix(item.QuestionID, "_abs")
}
