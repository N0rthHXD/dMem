package longmemeval

import (
	"strings"
	"testing"
	"time"
)

func TestAnswerPromptUsesChronologicalMemoryOrder(t *testing.T) {
	item := Case{Question: "Where?", QuestionDate: "2024/01/03 (Wed) 12:00"}
	prompt := answerPrompt(item, []datedMemory{
		{Date: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC), Content: "first"},
		{Date: time.Date(2024, 1, 2, 12, 0, 0, 0, time.UTC), Content: "second"},
	})
	if strings.Index(prompt, "first") > strings.Index(prompt, "second") || !strings.Contains(prompt, "Current Date: "+item.QuestionDate) {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestJudgePromptUsesOfficialTaskRules(t *testing.T) {
	temporal := judgePrompt(Case{QuestionType: "temporal-reasoning", Question: "How long?", Answer: "18 days"}, "19 days")
	if !strings.Contains(temporal, "do not penalize off-by-one errors") {
		t.Fatalf("temporal prompt = %q", temporal)
	}
	abstention := judgePrompt(Case{QuestionID: "q_abs", QuestionType: "single-session-user", Question: "Unknown?", Answer: "Not provided"}, "I do not know")
	if !strings.Contains(abstention, "unanswerable question") {
		t.Fatalf("abstention prompt = %q", abstention)
	}
}

func TestSummarizeQA(t *testing.T) {
	correct, wrong := &EvaluationResult{Label: true}, &EvaluationResult{Label: false}
	metrics := summarizeQA([]CaseResult{
		{QuestionType: "single-session-user", Evaluation: correct},
		{QuestionType: "single-session-user", Evaluation: wrong},
		{QuestionType: "knowledge-update", IsAbstention: true, Evaluation: correct},
	})
	if metrics.Evaluated != 3 || metrics.OverallAccuracy != 2.0/3.0 || metrics.TaskAveragedAccuracy != 0.75 || metrics.AbstentionAccuracy != 1 {
		t.Fatalf("metrics = %+v", metrics)
	}
}
