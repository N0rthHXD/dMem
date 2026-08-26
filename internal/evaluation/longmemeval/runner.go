package longmemeval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

const ResultFormatVersion = "longmemeval-dmem-v1"

type Config struct {
	RunID          string
	OutputPath     string
	HypothesesPath string
	RetrievalMode  string
	Limit          int
	MaxChars       int
	Cutoffs        []int
	AnswerCutoff   int
	Resume         bool
}

type EventLabel struct {
	SessionID string `json:"session_id"`
	Date      string `json:"date"`
	TurnStart int    `json:"turn_start"`
	TurnEnd   int    `json:"turn_end"`
	HasAnswer bool   `json:"has_answer"`
}

type IngestionResult struct {
	SpaceID     string                `json:"space_id"`
	TotalPairs  int                   `json:"total_pairs"`
	EventLabels map[string]EventLabel `json:"event_labels"`
	DurationMS  float64               `json:"duration_ms"`
}

type RetrievalResult struct {
	DurationMS       float64                  `json:"duration_ms"`
	Response         domain.SearchResponse    `json:"response"`
	SessionIDsByRank []string                 `json:"session_ids_by_rank"`
	SessionMetrics   map[string]CutoffMetrics `json:"session_metrics,omitempty"`
	EvidenceCoverage float64                  `json:"evidence_coverage,omitempty"`
	UnmappedResults  int                      `json:"unmapped_results"`
}

type CaseResult struct {
	QuestionID        string            `json:"question_id"`
	QuestionType      string            `json:"question_type"`
	IsAbstention      bool              `json:"is_abstention"`
	Question          string            `json:"question"`
	GroundTruthAnswer string            `json:"ground_truth_answer"`
	QuestionDate      string            `json:"question_date"`
	AnswerSessionIDs  []string          `json:"answer_session_ids"`
	Ingestion         IngestionResult   `json:"ingestion"`
	Retrieval         RetrievalResult   `json:"retrieval"`
	Generation        *GenerationResult `json:"generation,omitempty"`
	Evaluation        *EvaluationResult `json:"evaluation,omitempty"`
	Error             string            `json:"error,omitempty"`
}

type SummaryMetrics struct {
	EligibleQuestions int                      `json:"eligible_questions"`
	ExcludedQuestions int                      `json:"excluded_questions"`
	SessionMetrics    map[string]CutoffMetrics `json:"session_metrics"`
	EvidenceCoverage  float64                  `json:"evidence_coverage"`
	QA                QAMetrics                `json:"qa"`
}

type GenerationResult struct {
	Cutoff           int     `json:"cutoff"`
	Hypothesis       string  `json:"hypothesis"`
	Model            string  `json:"model"`
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
	RequestID        string  `json:"request_id,omitempty"`
	DurationMS       float64 `json:"duration_ms"`
}

type EvaluationResult struct {
	JudgeModel string  `json:"judge_model"`
	Label      bool    `json:"label"`
	Raw        string  `json:"raw"`
	RequestID  string  `json:"request_id,omitempty"`
	DurationMS float64 `json:"duration_ms"`
}

type QAMetrics struct {
	Evaluated            int                `json:"evaluated"`
	OverallAccuracy      float64            `json:"overall_accuracy"`
	TaskAveragedAccuracy float64            `json:"task_averaged_accuracy"`
	AbstentionAccuracy   float64            `json:"abstention_accuracy"`
	ByType               map[string]float64 `json:"by_type"`
}

type RunResult struct {
	FormatVersion string         `json:"format_version"`
	RunID         string         `json:"run_id"`
	DatasetSHA256 string         `json:"dataset_sha256"`
	IngestionMode string         `json:"ingestion_mode"`
	RetrievalMode string         `json:"retrieval_mode"`
	Limit         int            `json:"limit"`
	MaxChars      int            `json:"max_chars"`
	Cutoffs       []int          `json:"cutoffs"`
	AnswerCutoff  int            `json:"answer_cutoff,omitempty"`
	AnswerModel   string         `json:"answer_model,omitempty"`
	JudgeModel    string         `json:"judge_model,omitempty"`
	UpdatedAt     string         `json:"updated_at"`
	Summary       SummaryMetrics `json:"summary"`
	Cases         []CaseResult   `json:"cases"`
}

type Runner struct {
	client   MemoryClient
	answerer ChatModel
	judge    ChatModel
	config   Config
}

func NewRunner(client MemoryClient, answerer, judge ChatModel, config Config) (*Runner, error) {
	if client == nil || strings.TrimSpace(config.RunID) == "" || config.OutputPath == "" {
		return nil, errors.New("client, run_id, and output path are required")
	}
	if config.RetrievalMode != "keyword" && config.RetrievalMode != "semantic" && config.RetrievalMode != "hybrid" {
		return nil, errors.New("retrieval mode must be keyword, semantic, or hybrid")
	}
	if config.Limit < 1 || config.Limit > 100 || config.MaxChars < 1 || config.MaxChars > 100000 {
		return nil, errors.New("limit or max_chars is outside the dMem API range")
	}
	if len(config.Cutoffs) == 0 {
		return nil, errors.New("at least one cutoff is required")
	}
	for _, cutoff := range config.Cutoffs {
		if cutoff < 1 || cutoff > config.Limit {
			return nil, fmt.Errorf("cutoff %d is outside [1, limit]", cutoff)
		}
	}
	if answerer == nil && judge != nil {
		return nil, errors.New("judge requires an answerer")
	}
	if answerer != nil && (config.AnswerCutoff < 1 || config.AnswerCutoff > config.Limit) {
		return nil, errors.New("answer cutoff is outside [1, limit]")
	}
	return &Runner{client: client, answerer: answerer, judge: judge, config: config}, nil
}

func (r *Runner) Run(ctx context.Context, dataset Dataset) (RunResult, error) {
	result := RunResult{FormatVersion: ResultFormatVersion, RunID: r.config.RunID, DatasetSHA256: dataset.SHA256, IngestionMode: "direct-turn", RetrievalMode: r.config.RetrievalMode, Limit: r.config.Limit, MaxChars: r.config.MaxChars, Cutoffs: r.config.Cutoffs}
	if r.answerer != nil {
		result.AnswerCutoff = r.config.AnswerCutoff
		result.AnswerModel = r.answerer.Model()
	}
	if r.judge != nil {
		result.JudgeModel = r.judge.Model()
	}
	completed := map[string]CaseResult{}
	failed := 0
	if r.config.Resume {
		previous, err := loadResult(r.config.OutputPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return RunResult{}, err
		}
		if err == nil {
			if previous.FormatVersion != result.FormatVersion || previous.RunID != result.RunID || previous.DatasetSHA256 != result.DatasetSHA256 || previous.RetrievalMode != result.RetrievalMode || previous.Limit != result.Limit || previous.MaxChars != result.MaxChars || !equalInts(previous.Cutoffs, result.Cutoffs) || previous.AnswerCutoff != result.AnswerCutoff || previous.AnswerModel != result.AnswerModel || previous.JudgeModel != result.JudgeModel {
				return RunResult{}, errors.New("resume result is incompatible with this run")
			}
			for _, item := range previous.Cases {
				if item.Error == "" {
					completed[item.QuestionID] = item
				}
			}
		}
	}
	for _, item := range dataset.Cases {
		if existing, ok := completed[item.QuestionID]; ok {
			result.Cases = append(result.Cases, existing)
			continue
		}
		caseResult, err := r.runCase(ctx, dataset.SHA256, item)
		if err != nil {
			caseResult.Error = err.Error()
			failed++
		}
		result.Cases = append(result.Cases, caseResult)
		result.Summary = summarize(result.Cases, r.config.Cutoffs)
		result.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if writeErr := writeResult(r.config.OutputPath, result); writeErr != nil {
			return RunResult{}, writeErr
		}
		if writeErr := writeHypotheses(r.config.HypothesesPath, result.Cases); writeErr != nil {
			return RunResult{}, writeErr
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
	}
	result.Summary = summarize(result.Cases, r.config.Cutoffs)
	result.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeResult(r.config.OutputPath, result); err != nil {
		return result, err
	}
	if err := writeHypotheses(r.config.HypothesesPath, result.Cases); err != nil {
		return result, err
	}
	if failed > 0 {
		return result, fmt.Errorf("%d LongMemEval questions failed; inspect %s", failed, r.config.OutputPath)
	}
	return result, nil
}

func (r *Runner) runCase(ctx context.Context, datasetSHA string, item Case) (CaseResult, error) {
	result := CaseResult{QuestionID: item.QuestionID, QuestionType: item.QuestionType, IsAbstention: item.IsAbstention(), Question: item.Question, GroundTruthAnswer: item.Answer, QuestionDate: item.QuestionDate, AnswerSessionIDs: item.AnswerSessionIDs}
	started := time.Now()
	space, err := r.client.CreateSpace(ctx, stableKey("space", datasetSHA, item.QuestionID, r.config.RunID), boundedExternalID(datasetSHA, item.QuestionID, r.config.RunID), "LongMemEval "+item.QuestionID)
	if err != nil {
		return result, fmt.Errorf("create space: %w", err)
	}
	result.Ingestion.SpaceID = space.SpaceID
	result.Ingestion.EventLabels = map[string]EventLabel{}
	for _, session := range item.Sessions() {
		for start := 0; start < len(session.Turns); start += 2 {
			end := start + 2
			if end > len(session.Turns) {
				end = len(session.Turns)
			}
			turns := session.Turns[start:end]
			content := renderTurns(turns)
			sourceID := stableKey("source", datasetSHA, item.QuestionID, session.ID, fmt.Sprint(start))
			memory, err := r.client.Remember(ctx, stableKey("remember", datasetSHA, item.QuestionID, session.ID, fmt.Sprint(start)), rememberRequest{
				SpaceID: space.SpaceID, Content: content, MemoryType: "event", SourceName: "longmemeval", SourceEventID: sourceID,
				OccurredAt: session.Date.Format(time.RFC3339), Reason: "longmemeval_direct_turn",
				Metadata: map[string]any{"benchmark": "longmemeval", "question_id": item.QuestionID, "session_id": session.ID, "turn_start": start, "turn_end": end - 1},
			})
			if err != nil {
				return result, fmt.Errorf("remember session %s turns %d-%d: %w", session.ID, start, end-1, err)
			}
			hasAnswer := false
			for _, turn := range turns {
				hasAnswer = hasAnswer || turn.HasAnswer
			}
			result.Ingestion.EventLabels[memory.EventID] = EventLabel{SessionID: session.ID, Date: session.Date.Format(time.RFC3339), TurnStart: start, TurnEnd: end - 1, HasAnswer: hasAnswer}
			result.Ingestion.TotalPairs++
		}
	}
	result.Ingestion.DurationMS = float64(time.Since(started).Microseconds()) / 1000
	questionDate, _ := ParseDate(item.QuestionDate)
	retrievalStarted := time.Now()
	response, err := r.client.Recall(ctx, recallRequest{Query: item.Question, SpaceIDs: []string{space.SpaceID}, IncludeAncestors: false, ReferenceTime: questionDate.Format(time.RFC3339), Mode: r.config.RetrievalMode, Limit: r.config.Limit, MaxChars: r.config.MaxChars, IncludeEvidence: true})
	if err != nil {
		return result, fmt.Errorf("recall: %w", err)
	}
	result.Retrieval.DurationMS = float64(time.Since(retrievalStarted).Microseconds()) / 1000
	result.Retrieval.Response = response
	result.Retrieval.SessionIDsByRank, result.Retrieval.UnmappedResults = sessionRanks(response.Results, result.Ingestion.EventLabels)
	if !item.IsAbstention() && len(item.AnswerSessionIDs) > 0 {
		result.Retrieval.SessionMetrics = map[string]CutoffMetrics{}
		for _, cutoff := range r.config.Cutoffs {
			result.Retrieval.SessionMetrics[fmt.Sprint(cutoff)] = SessionRetrievalMetrics(result.Retrieval.SessionIDsByRank, item.AnswerSessionIDs, cutoff)
		}
		result.Retrieval.EvidenceCoverage = evidenceCoverage(response.Results, result.Ingestion.EventLabels, item)
	}
	if r.answerer != nil {
		started := time.Now()
		completion, err := r.answerer.Complete(ctx, answerPrompt(item, answerMemories(response.Results, result.Ingestion.EventLabels, r.config.AnswerCutoff)), 500)
		if err != nil {
			return result, fmt.Errorf("generate answer: %w", err)
		}
		result.Generation = &GenerationResult{Cutoff: r.config.AnswerCutoff, Hypothesis: completion.Content, Model: r.answerer.Model(), PromptTokens: completion.PromptTokens, CompletionTokens: completion.CompletionTokens, RequestID: completion.RequestID, DurationMS: float64(time.Since(started).Microseconds()) / 1000}
		if r.judge != nil {
			started = time.Now()
			judgment, err := r.judge.Complete(ctx, judgePrompt(item, completion.Content), 10)
			if err != nil {
				return result, fmt.Errorf("judge answer: %w", err)
			}
			result.Evaluation = &EvaluationResult{JudgeModel: r.judge.Model(), Label: strings.Contains(strings.ToLower(judgment.Content), "yes"), Raw: judgment.Content, RequestID: judgment.RequestID, DurationMS: float64(time.Since(started).Microseconds()) / 1000}
		}
	}
	return result, nil
}

type datedMemory struct {
	Date    time.Time
	Content string
}

func answerMemories(results []domain.SearchResult, labels map[string]EventLabel, cutoff int) []datedMemory {
	if cutoff > len(results) {
		cutoff = len(results)
	}
	memories := make([]datedMemory, 0, cutoff)
	for _, result := range results[:cutoff] {
		var date time.Time
		for _, evidence := range result.Memory.CurrentRevision.Evidence {
			if label, ok := labels[evidence.EventID]; ok {
				date, _ = time.Parse(time.RFC3339, label.Date)
				break
			}
		}
		memories = append(memories, datedMemory{Date: date, Content: result.Memory.CurrentRevision.Content})
	}
	sort.SliceStable(memories, func(i, j int) bool { return memories[i].Date.Before(memories[j].Date) })
	return memories
}

func renderTurns(turns []Turn) string {
	var builder strings.Builder
	for i, turn := range turns {
		if i > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(turn.Role)
		builder.WriteString(": ")
		builder.WriteString(turn.Content)
	}
	return builder.String()
}

func sessionRanks(results []domain.SearchResult, labels map[string]EventLabel) ([]string, int) {
	ranks := make([]string, len(results))
	unmapped := 0
	for i, result := range results {
		for _, evidence := range result.Memory.CurrentRevision.Evidence {
			label, ok := labels[evidence.EventID]
			if !ok {
				continue
			}
			ranks[i] = label.SessionID
			break
		}
		if ranks[i] == "" {
			unmapped++
		}
	}
	return ranks, unmapped
}

func equalInts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func evidenceCoverage(results []domain.SearchResult, labels map[string]EventLabel, item Case) float64 {
	answerSessions := make(map[string]bool, len(item.AnswerSessionIDs))
	for _, id := range item.AnswerSessionIDs {
		answerSessions[id] = true
	}
	total := 0
	for _, label := range labels {
		if answerSessions[label.SessionID] && label.HasAnswer {
			total++
		}
	}
	if total == 0 {
		return 0
	}
	found := map[string]bool{}
	for _, result := range results {
		for _, evidence := range result.Memory.CurrentRevision.Evidence {
			label, ok := labels[evidence.EventID]
			if ok && answerSessions[label.SessionID] && label.HasAnswer {
				found[evidence.EventID] = true
			}
		}
	}
	return float64(len(found)) / float64(total)
}

func summarize(cases []CaseResult, cutoffs []int) SummaryMetrics {
	result := SummaryMetrics{SessionMetrics: map[string]CutoffMetrics{}}
	sums := map[string]CutoffMetrics{}
	for _, item := range cases {
		if item.Error != "" || item.IsAbstention || len(item.AnswerSessionIDs) == 0 {
			result.ExcludedQuestions++
			continue
		}
		result.EligibleQuestions++
		result.EvidenceCoverage += item.Retrieval.EvidenceCoverage
		for _, cutoff := range cutoffs {
			key := fmt.Sprint(cutoff)
			metric := item.Retrieval.SessionMetrics[key]
			sum := sums[key]
			sum.RecallAny += metric.RecallAny
			sum.RecallAll += metric.RecallAll
			sum.NDCG += metric.NDCG
			sums[key] = sum
		}
	}
	if result.EligibleQuestions == 0 {
		result.QA = summarizeQA(cases)
		return result
	}
	for key, sum := range sums {
		count := float64(result.EligibleQuestions)
		result.SessionMetrics[key] = CutoffMetrics{RecallAny: sum.RecallAny / count, RecallAll: sum.RecallAll / count, NDCG: sum.NDCG / count}
	}
	result.EvidenceCoverage /= float64(result.EligibleQuestions)
	result.QA = summarizeQA(cases)
	return result
}

func summarizeQA(cases []CaseResult) QAMetrics {
	result := QAMetrics{ByType: map[string]float64{}}
	counts, correct := map[string]int{}, map[string]int{}
	abstentionCount, abstentionCorrect := 0, 0
	for _, item := range cases {
		if item.Error != "" || item.Evaluation == nil {
			continue
		}
		result.Evaluated++
		counts[item.QuestionType]++
		if item.Evaluation.Label {
			correct[item.QuestionType]++
			result.OverallAccuracy++
		}
		if item.IsAbstention {
			abstentionCount++
			if item.Evaluation.Label {
				abstentionCorrect++
			}
		}
	}
	if result.Evaluated > 0 {
		result.OverallAccuracy /= float64(result.Evaluated)
	}
	for questionType := range questionTypes {
		if counts[questionType] == 0 {
			continue
		}
		accuracy := float64(correct[questionType]) / float64(counts[questionType])
		result.ByType[questionType] = accuracy
		result.TaskAveragedAccuracy += accuracy
	}
	if len(result.ByType) > 0 {
		result.TaskAveragedAccuracy /= float64(len(result.ByType))
	}
	if abstentionCount > 0 {
		result.AbstentionAccuracy = float64(abstentionCorrect) / float64(abstentionCount)
	}
	return result
}

func stableKey(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "lme-" + hex.EncodeToString(hash[:16])
}

func boundedExternalID(datasetSHA, questionID, runID string) string {
	return "longmemeval:" + stableKey(datasetSHA, questionID, runID)
}

func loadResult(path string) (RunResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RunResult{}, err
	}
	var result RunResult
	if err := json.Unmarshal(data, &result); err != nil {
		return RunResult{}, fmt.Errorf("decode resume result: %w", err)
	}
	return result, nil
}

func writeResult(path string, result RunResult) error {
	result.Cases = append([]CaseResult(nil), result.Cases...)
	sort.Slice(result.Cases, func(i, j int) bool { return result.Cases[i].QuestionID < result.Cases[j].QuestionID })
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWrite(path, data)
}

func atomicWrite(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".longmemeval-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

func writeHypotheses(path string, cases []CaseResult) error {
	if path == "" {
		return nil
	}
	cases = append([]CaseResult(nil), cases...)
	sort.Slice(cases, func(i, j int) bool { return cases[i].QuestionID < cases[j].QuestionID })
	var output strings.Builder
	for _, item := range cases {
		if item.Error != "" || item.Generation == nil {
			continue
		}
		data, err := json.Marshal(struct {
			QuestionID string `json:"question_id"`
			Hypothesis string `json:"hypothesis"`
		}{item.QuestionID, item.Generation.Hypothesis})
		if err != nil {
			return err
		}
		output.Write(data)
		output.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return atomicWrite(path, []byte(output.String()))
}
