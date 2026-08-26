package longmemeval

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/N0rthHXD/dMem/internal/domain"
)

type fakeClient struct {
	remembered []rememberRequest
	recall     recallRequest
}

func (f *fakeClient) CreateSpace(context.Context, string, string, string) (domain.Space, error) {
	return domain.Space{SpaceID: "space-1"}, nil
}

func (f *fakeClient) Remember(_ context.Context, _ string, request rememberRequest) (rememberResult, error) {
	f.remembered = append(f.remembered, request)
	return rememberResult{EventID: "event-" + request.Metadata["session_id"].(string)}, nil
}

func (f *fakeClient) Recall(_ context.Context, request recallRequest) (domain.SearchResponse, error) {
	f.recall = request
	return domain.SearchResponse{Results: []domain.SearchResult{
		{Memory: domain.Memory{CurrentRevision: domain.MemoryRevision{Evidence: []domain.MemoryEvidence{{EventID: "event-answer-1"}}}}},
		{Memory: domain.Memory{CurrentRevision: domain.MemoryRevision{Evidence: []domain.MemoryEvidence{{EventID: "event-later"}}}}},
	}}, nil
}

func TestRunnerIngestsChronologicallyWithoutLabelsAndScoresEvidence(t *testing.T) {
	dataset, err := Decode(strings.NewReader(validDataset))
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{}
	output := filepath.Join(t.TempDir(), "result.json")
	runner, err := NewRunner(client, nil, nil, Config{RunID: "test", OutputPath: output, RetrievalMode: "keyword", Limit: 2, MaxChars: 1000, Cutoffs: []int{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), dataset)
	if err != nil {
		t.Fatal(err)
	}
	if got := client.remembered[0].Metadata["session_id"]; got != "answer-1" {
		t.Fatalf("first session = %v", got)
	}
	for _, request := range client.remembered {
		if strings.Contains(request.Content, "has_answer") {
			t.Fatalf("benchmark label leaked into memory: %q", request.Content)
		}
	}
	if client.recall.ReferenceTime != "2024-01-03T12:00:00Z" || client.recall.IncludeAncestors || !client.recall.IncludeEvidence {
		t.Fatalf("recall request = %+v", client.recall)
	}
	metrics := result.Cases[0].Retrieval.SessionMetrics
	if metrics["1"].RecallAll != 1 || result.Cases[0].Retrieval.EvidenceCoverage != 1 {
		t.Fatalf("retrieval = %+v", result.Cases[0].Retrieval)
	}
	if result.Summary.EligibleQuestions != 1 || result.Summary.SessionMetrics["1"].RecallAny != 1 {
		t.Fatalf("summary = %+v", result.Summary)
	}
}

func TestRunnerRejectsIncompatibleResumeCutoffs(t *testing.T) {
	dataset, _ := Decode(strings.NewReader(validDataset))
	output := filepath.Join(t.TempDir(), "result.json")
	client := &fakeClient{}
	first, _ := NewRunner(client, nil, nil, Config{RunID: "test", OutputPath: output, RetrievalMode: "keyword", Limit: 2, MaxChars: 1000, Cutoffs: []int{1}})
	if _, err := first.Run(context.Background(), dataset); err != nil {
		t.Fatal(err)
	}
	resume, _ := NewRunner(client, nil, nil, Config{RunID: "test", OutputPath: output, RetrievalMode: "keyword", Limit: 2, MaxChars: 1000, Cutoffs: []int{2}, Resume: true})
	if _, err := resume.Run(context.Background(), dataset); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("resume error = %v", err)
	}
}
