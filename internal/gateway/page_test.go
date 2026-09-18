package gateway

import "testing"

func TestPageRequestEffectiveLimit(t *testing.T) {
	cases := []struct {
		limit int
		want  int
	}{
		{limit: 0, want: DefaultPageLimit},
		{limit: -1, want: DefaultPageLimit},
		{limit: 10, want: 10},
		{limit: MaxPageLimit + 100, want: MaxPageLimit},
	}
	for _, tc := range cases {
		if got := (PageRequest{Limit: tc.limit}).EffectiveLimit(); got != tc.want {
			t.Fatalf("EffectiveLimit(%d) = %d, want %d", tc.limit, got, tc.want)
		}
	}
}

func TestPageRequestValidate(t *testing.T) {
	if err := (PageRequest{Limit: 0}).Validate(); err != nil {
		t.Fatalf("zero limit must be valid: %v", err)
	}
	if err := (PageRequest{Limit: MaxPageLimit}).Validate(); err != nil {
		t.Fatalf("max limit must be valid: %v", err)
	}
	if err := (PageRequest{Limit: -1}).Validate(); err == nil {
		t.Fatal("negative limit must be invalid")
	}
	if err := (PageRequest{Limit: MaxPageLimit + 1}).Validate(); err == nil {
		t.Fatal("over-max limit must be invalid")
	}
}
