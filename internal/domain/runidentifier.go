package domain

import (
	"regexp"
	"time"
)

const (
	runIdentifierTimeLayout = "20060102T150405Z"
	shortCommitShaLength    = 7
)

var runIdentifierPattern = regexp.MustCompile(`^(\d{8}T\d{6}Z)-[0-9a-f]+$`)

func ShortCommitSha(commitSha string) string {
	return commitSha[:min(shortCommitShaLength, len(commitSha))]
}

func NewRunIdentifier(startedAt time.Time, commitSha string) string {
	return startedAt.UTC().Format(runIdentifierTimeLayout) + "-" + ShortCommitSha(commitSha)
}

func StartTimeOfRunIdentifier(name string) (time.Time, bool) {
	match := runIdentifierPattern.FindStringSubmatch(name)
	if match == nil {
		return time.Time{}, false
	}
	startedAt, err := time.Parse(runIdentifierTimeLayout, match[1])
	if err != nil {
		return time.Time{}, false
	}
	return startedAt, true
}
