package domain

import (
	"testing"
	"time"
)

func TestRunIdentifierIsTheUtcStartTimeAndShortCommit(t *testing.T) {
	startedAt := time.Date(2026, 10, 31, 16, 27, 0, 0, time.FixedZone("example", 11*60*60))

	runIdentifier := NewRunIdentifier(startedAt, "094b1e7c0000000000000000000000000000000000")

	if runIdentifier != "20261031T052700Z-094b1e7" {
		t.Errorf("got %s", runIdentifier)
	}
}

func TestStartTimeIsReadBackFromARunIdentifier(t *testing.T) {
	startedAt, ok := StartTimeOfRunIdentifier("20261031T052700Z-094b1e7")

	if !ok {
		t.Fatal("expected the identifier to be recognised")
	}
	if !startedAt.Equal(time.Date(2026, 10, 31, 5, 27, 0, 0, time.UTC)) {
		t.Errorf("got %s", startedAt)
	}
}

func TestNamesThatAreNotRunIdentifiersAreNotRecognised(t *testing.T) {
	for _, name := range []string{"", "kept-by-someone-else", "20261031T052700Z", "20261031T052700Z-", "20261331T052700Z-094b1e7", "x20261031T052700Z-094b1e7", "poll-problems.log"} {
		if _, ok := StartTimeOfRunIdentifier(name); ok {
			t.Errorf("%q should not be recognised", name)
		}
	}
}
