package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JYGC/citask/internal/domain"
)

func TestCreatingARunLogMakesTheLogAndArtifactFolderReadableByOtherUsers(t *testing.T) {
	logsDirectory := t.TempDir()
	runLogs := RunLogStore{LogsDirectory: logsDirectory}

	location, err := runLogs.CreateRunLog(domain.Project{Name: "example-project"}, "20261031T052700Z-094b1e7")

	if err != nil {
		t.Fatal(err)
	}
	expectedRunFolder := filepath.Join(logsDirectory, "example-project", "20261031T052700Z-094b1e7")
	if location.RunLogFile != filepath.Join(expectedRunFolder, "run.log") {
		t.Errorf("run log file: %s", location.RunLogFile)
	}
	if location.RunArtifactDirectory != filepath.Join(expectedRunFolder, "artifacts") {
		t.Errorf("artifact directory: %s", location.RunArtifactDirectory)
	}
	for path, expectedPermissions := range map[string]os.FileMode{
		location.RunLogFile:           0o644,
		location.RunArtifactDirectory: 0o755,
		expectedRunFolder:             0o755,
	} {
		information, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if information.Mode().Perm() != expectedPermissions {
			t.Errorf("%s has permissions %o", path, information.Mode().Perm())
		}
	}
}

func TestAppendingToARunLogKeepsWhatIsThere(t *testing.T) {
	runLogs := RunLogStore{LogsDirectory: t.TempDir()}
	project := domain.Project{Name: "example-project"}
	location, err := runLogs.CreateRunLog(project, "20261031T052700Z-094b1e7")
	if err != nil {
		t.Fatal(err)
	}

	for _, text := range []string{"first\n", "second\n"} {
		if err := runLogs.AppendToRunLog(project, "20261031T052700Z-094b1e7", text); err != nil {
			t.Fatal(err)
		}
	}

	content, err := os.ReadFile(location.RunLogFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "first\nsecond\n" {
		t.Errorf("got %q", content)
	}
}

func TestPollProblemsAreKeptPerProjectWithATime(t *testing.T) {
	logsDirectory := t.TempDir()
	runLogs := RunLogStore{LogsDirectory: logsDirectory}

	for _, problem := range []string{"first problem", "second problem"} {
		if err := runLogs.RecordPollProblem(domain.Project{Name: "example-project"}, problem); err != nil {
			t.Fatal(err)
		}
	}

	content, err := os.ReadFile(filepath.Join(logsDirectory, "example-project", "poll-problems.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two lines, got %q", content)
	}
	if !strings.Contains(lines[1], "second problem") || !strings.Contains(lines[1], time.Now().UTC().Format("2006")) {
		t.Errorf("unexpected line: %q", lines[1])
	}
}

func TestDeletingOldRunLogsLeavesNewerRunsAndAnythingCitaskDidNotCreate(t *testing.T) {
	logsDirectory := t.TempDir()
	runLogs := RunLogStore{LogsDirectory: logsDirectory}
	project := domain.Project{Name: "example-project"}
	expiredRunIdentifier := "20200101T000000Z-aaaaaaa"
	recentRunIdentifier := "20261031T052700Z-bbbbbbb"
	for _, runIdentifier := range []string{expiredRunIdentifier, recentRunIdentifier} {
		if _, err := runLogs.CreateRunLog(project, runIdentifier); err != nil {
			t.Fatal(err)
		}
	}
	projectLogs := filepath.Join(logsDirectory, project.Name)
	folderCitaskDidNotCreate := filepath.Join(projectLogs, "kept-by-someone-else")
	if err := os.Mkdir(folderCitaskDidNotCreate, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runLogs.RecordPollProblem(project, "a problem"); err != nil {
		t.Fatal(err)
	}

	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := runLogs.DeleteRunLogsOlderThan(project, cutoff); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(projectLogs, expiredRunIdentifier)); !os.IsNotExist(err) {
		t.Errorf("the expired run survived: %v", err)
	}
	for _, survivor := range []string{recentRunIdentifier, "kept-by-someone-else", "poll-problems.log"} {
		if _, err := os.Stat(filepath.Join(projectLogs, survivor)); err != nil {
			t.Errorf("%s was removed: %v", survivor, err)
		}
	}
}

func TestDeletingOldRunLogsOfAProjectWithNoLogsIsNotAnError(t *testing.T) {
	runLogs := RunLogStore{LogsDirectory: t.TempDir()}

	if err := runLogs.DeleteRunLogsOlderThan(domain.Project{Name: "example-project"}, time.Now()); err != nil {
		t.Fatal(err)
	}
}
