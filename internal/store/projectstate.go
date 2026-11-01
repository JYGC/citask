package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JYGC/citask/internal/domain"
)

const (
	lastTestedCommitFileName   = "last-tested-commit"
	unreportedResultsFolder    = "unreported-results"
	unreportedResultFileSuffix = ".json"
)

type ProjectStateStore struct {
	StateDirectory string
}

func (stateStore ProjectStateStore) lastTestedCommitFile(projectName string) string {
	return filepath.Join(stateStore.StateDirectory, projectName, lastTestedCommitFileName)
}

func (stateStore ProjectStateStore) unreportedResultsDirectory(projectName string) string {
	return filepath.Join(stateStore.StateDirectory, projectName, unreportedResultsFolder)
}

func (stateStore ProjectStateStore) unreportedResultFile(result domain.RunResult) string {
	return filepath.Join(stateStore.unreportedResultsDirectory(result.ProjectName), result.RunIdentifier+unreportedResultFileSuffix)
}

func (stateStore ProjectStateStore) ReadLastTestedCommit(project domain.Project) (string, bool, error) {
	content, err := os.ReadFile(stateStore.lastTestedCommitFile(project.Name))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read last tested commit of %s: %w", project.Name, err)
	}
	commitSha := strings.TrimSpace(string(content))
	return commitSha, commitSha != "", nil
}

func (stateStore ProjectStateStore) SaveLastTestedCommit(project domain.Project, commitSha string) error {
	return writeFileAtomically(stateStore.lastTestedCommitFile(project.Name), []byte(commitSha+"\n"), 0o644)
}

func (stateStore ProjectStateStore) SaveUnreportedResult(result domain.RunResult) error {
	content, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode result of %s: %w", result.RunIdentifier, err)
	}
	return writeFileAtomically(stateStore.unreportedResultFile(result), content, 0o644)
}

func (stateStore ProjectStateStore) ListUnreportedResultsOldestFirst(project domain.Project) ([]domain.RunResult, error) {
	directory := stateStore.unreportedResultsDirectory(project.Name)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list unreported results of %s: %w", project.Name, err)
	}

	results := make([]domain.RunResult, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), unreportedResultFileSuffix) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read unreported result %s: %w", entry.Name(), err)
		}
		var result domain.RunResult
		if err := json.Unmarshal(content, &result); err != nil {
			return nil, fmt.Errorf("decode unreported result %s: %w", entry.Name(), err)
		}
		results = append(results, result)
	}
	sort.SliceStable(results, func(first, second int) bool {
		return results[first].FinishedAt.Before(results[second].FinishedAt)
	})
	return results, nil
}

func (stateStore ProjectStateStore) DeleteUnreportedResult(result domain.RunResult) error {
	if err := os.Remove(stateStore.unreportedResultFile(result)); err != nil {
		return fmt.Errorf("delete unreported result %s: %w", result.RunIdentifier, err)
	}
	return nil
}
