package gateway

import "testing"

type hashCommand struct {
	SpaceID string
	Content string
	Tags    map[string]string
	Values  []int
}

func TestCanonicalHashIsDeterministicAndOrderIndependent(t *testing.T) {
	first := hashCommand{SpaceID: "space", Content: "text", Tags: map[string]string{"b": "2", "a": "1"}, Values: []int{1, 2}}
	second := hashCommand{SpaceID: "space", Content: "text", Tags: map[string]string{"a": "1", "b": "2"}, Values: []int{1, 2}}
	firstHash, err := CanonicalHash(first)
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	secondHash, err := CanonicalHash(second)
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	if firstHash != secondHash {
		t.Fatalf("map insertion order changed the hash: %s != %s", firstHash, secondHash)
	}
	if len(firstHash) != 64 {
		t.Fatalf("hash must be a hex sha256 digest, got %q", firstHash)
	}
}

func TestCanonicalHashDistinguishesCommands(t *testing.T) {
	base, _ := CanonicalHash(hashCommand{SpaceID: "space", Content: "text"})
	changed, _ := CanonicalHash(hashCommand{SpaceID: "space", Content: "other"})
	if base == changed {
		t.Fatal("different commands must not share a canonical hash")
	}
}

func TestCanonicalHashRejectsUnencodableValue(t *testing.T) {
	if _, err := CanonicalHash(make(chan int)); err == nil {
		t.Fatal("expected an error for an unencodable command value")
	}
}
