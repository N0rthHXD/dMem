package longmemeval

import (
	"strings"
	"testing"
)

const validDataset = `[{"question_id":"q1","question_type":"single-session-user","question":"Where does the user live?","answer":"Paris","question_date":"2024/01/03 (Wed) 12:00","haystack_session_ids":["later","answer-1"],"haystack_dates":["2024/01/02 (Tue) 12:00","2024/01/01 (Mon) 12:00"],"haystack_sessions":[[{"role":"user","content":"Unrelated"}],[{"role":"user","content":"I live in Paris","has_answer":true},{"role":"assistant","content":"Noted"}]],"answer_session_ids":["answer-1"]}]`

func TestDecodeValidatesAndSortsSessions(t *testing.T) {
	dataset, err := Decode(strings.NewReader(validDataset))
	if err != nil {
		t.Fatal(err)
	}
	sessions := dataset.Cases[0].Sessions()
	if dataset.SHA256 == "" || sessions[0].ID != "answer-1" || sessions[1].ID != "later" {
		t.Fatalf("dataset = %+v, sessions = %+v", dataset, sessions)
	}
}

func TestDecodeRejectsUnknownFieldsAndParallelArrayMismatch(t *testing.T) {
	unknown := strings.Replace(validDataset, `"question_id":"q1"`, `"unexpected":true,"question_id":"q1"`, 1)
	if _, err := Decode(strings.NewReader(unknown)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
	mismatch := strings.Replace(validDataset, `"haystack_dates":["2024/01/02 (Tue) 12:00","2024/01/01 (Mon) 12:00"]`, `"haystack_dates":["2024/01/02 (Tue) 12:00"]`, 1)
	if _, err := Decode(strings.NewReader(mismatch)); err == nil || !strings.Contains(err.Error(), "equal lengths") {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestDecodeRejectsUnresolvedAnswerSession(t *testing.T) {
	input := strings.Replace(validDataset, `"answer_session_ids":["answer-1"]`, `"answer_session_ids":["missing"]`, 1)
	if _, err := Decode(strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), "does not resolve") {
		t.Fatalf("error = %v", err)
	}
}
