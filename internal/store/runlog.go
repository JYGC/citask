package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JYGC/citask/internal/domain"
)

const (
	runLogFileName        = "run.log"
	runArtifactFolderName = "artifacts"
	pollProblemsFileName  = "poll-problems.log"
)

type RunLogStore struct {
	LogsDirectory string
}

func (runLogs RunLogStore) runFolder(project domain.Project, runIdentifier string) string {
	return filepath.Join(runLogs.LogsDirectory, project.Name, runIdentifier)
}

func (runLogs RunLogStore) CreateRunLog(project domain.Project, runIdentifier string) (domain.RunLogLocation, error) {
	runFolder := runLogs.runFolder(project, runIdentifier)
	location := domain.RunLogLocation{
		RunLogFile:           filepath.Join(runFolder, runLogFileName),
		RunArtifactDirectory: filepath.Join(runFolder, runArtifactFolderName),
	}
	if err := os.MkdirAll(location.RunArtifactDirectory, 0o755); err != nil {
		return domain.RunLogLocation{}, fmt.Errorf("create run folder: %w", err)
	}
	if err := appendToFile(location.RunLogFile, ""); err != nil {
		return domain.RunLogLocation{}, err
	}
	return location, nil
}

func (runLogs RunLogStore) AppendToRunLog(project domain.Project, runIdentifier, text string) error {
	runFolder := runLogs.runFolder(project, runIdentifier)
	if err := os.MkdirAll(runFolder, 0o755); err != nil {
		return fmt.Errorf("create run folder: %w", err)
	}
	return appendToFile(filepath.Join(runFolder, runLogFileName), text)
}

// RecordPollProblem keeps problems that belong to no run, such as an unreachable remote. Cron discards
// the output of a poll that exits zero, so these would otherwise be visible nowhere.
func (runLogs RunLogStore) RecordPollProblem(project domain.Project, problem string) error {
	projectLogs := filepath.Join(runLogs.LogsDirectory, project.Name)
	if err := os.MkdirAll(projectLogs, 0o755); err != nil {
		return fmt.Errorf("create logs folder: %w", err)
	}
	line := time.Now().UTC().Format(time.RFC3339) + " " + strings.Join(strings.Fields(problem), " ") + "\n"
	return appendToFile(filepath.Join(projectLogs, pollProblemsFileName), line)
}

func (runLogs RunLogStore) DeleteRunLogsOlderThan(project domain.Project, cutoff time.Time) error {
	projectLogs := filepath.Join(runLogs.LogsDirectory, project.Name)
	entries, err := os.ReadDir(projectLogs)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list logs of %s: %w", project.Name, err)
	}
	for _, entry := range entries {
		startedAt, isRunFolder := domain.StartTimeOfRunIdentifier(entry.Name())
		if !isRunFolder || !entry.IsDir() || !startedAt.Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(projectLogs, entry.Name())); err != nil {
			return fmt.Errorf("delete expired run %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func appendToFile(path, text string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	if _, err := file.WriteString(text); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
