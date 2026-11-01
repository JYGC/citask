package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JYGC/citask/internal/domain"
)

var exampleProject = domain.Project{Name: "example-project"}

func TestLastTestedCommitIsNotFoundBeforeItIsSaved(t *testing.T) {
	stateStore := ProjectStateStore{StateDirectory: t.TempDir()}

	_, found, err := stateStore.ReadLastTestedCommit(exampleProject)

	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("nothing has been saved yet")
	}
}

func TestLastTestedCommitIsReadBackAfterSaving(t *testing.T) {
	stateStore := ProjectStateStore{StateDirectory: t.TempDir()}

	if err := stateStore.SaveLastTestedCommit(exampleProject, "first-commit"); err != nil {
		t.Fatal(err)
	}
	if err := stateStore.SaveLastTestedCommit(exampleProject, "second-commit"); err != nil {
		t.Fatal(err)
	}

	commitSha, found, err := stateStore.ReadLastTestedCommit(exampleProject)
	if err != nil {
		t.Fatal(err)
	}
	if !found || commitSha != "second-commit" {
		t.Errorf("got %q, found %v", commitSha, found)
	}
}

func TestSavingReplacesTheFileRatherThanWritingIntoIt(t *testing.T) {
	stateDirectory := t.TempDir()
	stateStore := ProjectStateStore{StateDirectory: stateDirectory}
	if err := stateStore.SaveLastTestedCommit(exampleProject, "first-commit"); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(stateDirectory, exampleProject.Name, "last-tested-commit")
	hardLinkToTheOriginalFile := stateFile + ".original"
	if err := os.Link(stateFile, hardLinkToTheOriginalFile); err != nil {
		t.Fatal(err)
	}

	if err := stateStore.SaveLastTestedCommit(exampleProject, "second-commit"); err != nil {
		t.Fatal(err)
	}

	originalContent, err := os.ReadFile(hardLinkToTheOriginalFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(originalContent) != "first-commit\n" {
		t.Errorf("the original file was written into, it now holds %q", originalContent)
	}
}

func TestFailedSaveLeavesTheLastTestedCommitAndNoPartialFile(t *testing.T) {
	stateDirectory := t.TempDir()
	stateStore := ProjectStateStore{StateDirectory: stateDirectory}
	if err := stateStore.SaveLastTestedCommit(exampleProject, "first-commit"); err != nil {
		t.Fatal(err)
	}
	projectStateDirectory := filepath.Join(stateDirectory, exampleProject.Name)
	stateFile := filepath.Join(projectStateDirectory, "last-tested-commit")
	if err := os.Remove(stateFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateFile, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := stateStore.SaveLastTestedCommit(exampleProject, "second-commit"); err == nil {
		t.Fatal("renaming over a folder should fail")
	}

	entries, err := os.ReadDir(projectStateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("a partial file was left behind: %v", entries)
	}
}

func exampleResult(runIdentifier string, finishedAt time.Time) domain.RunResult {
	return domain.RunResult{
		ProjectName:   exampleProject.Name,
		CommitSha:     "example-commit",
		RunIdentifier: runIdentifier,
		Passed:        false,
		StepOutcomes:  []domain.StepOutcome{{StepName: "example-step", Status: domain.StepFailed, Duration: time.Second}},
		FinishedAt:    finishedAt,
	}
}

func TestUnreportedResultsAreReadBackWhole(t *testing.T) {
	stateStore := ProjectStateStore{StateDirectory: t.TempDir()}
	savedResult := exampleResult("20261031T052700Z-094b1e7", time.Date(2026, 10, 31, 5, 27, 0, 0, time.UTC))

	if err := stateStore.SaveUnreportedResult(savedResult); err != nil {
		t.Fatal(err)
	}

	results, err := stateStore.ListUnreportedResultsOldestFirst(exampleProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %+v", results)
	}
	if results[0].RunIdentifier != savedResult.RunIdentifier || results[0].StepOutcomes[0].Status != domain.StepFailed {
		t.Errorf("got %+v", results[0])
	}
}

func TestUnreportedResultsAreListedOldestFirstEvenWithinOneSecond(t *testing.T) {
	stateStore := ProjectStateStore{StateDirectory: t.TempDir()}
	sameSecond := time.Date(2026, 10, 31, 5, 27, 0, 0, time.UTC)
	identifierThatSortsLastButFinishedFirst := "20261031T052700Z-fffffff"
	identifierThatSortsFirstButFinishedLast := "20261031T052700Z-0000000"
	for _, result := range []domain.RunResult{
		exampleResult(identifierThatSortsFirstButFinishedLast, sameSecond.Add(500*time.Millisecond)),
		exampleResult(identifierThatSortsLastButFinishedFirst, sameSecond.Add(100*time.Millisecond)),
	} {
		if err := stateStore.SaveUnreportedResult(result); err != nil {
			t.Fatal(err)
		}
	}

	results, err := stateStore.ListUnreportedResultsOldestFirst(exampleProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].RunIdentifier != identifierThatSortsLastButFinishedFirst {
		t.Fatalf("wrong order: %+v", results)
	}
}

func TestUnreportedResultListingIgnoresAPartialFileLeftByACrash(t *testing.T) {
	stateDirectory := t.TempDir()
	stateStore := ProjectStateStore{StateDirectory: stateDirectory}
	if err := stateStore.SaveUnreportedResult(exampleResult("20261031T052700Z-094b1e7", time.Now())); err != nil {
		t.Fatal(err)
	}
	partialFile := filepath.Join(stateDirectory, exampleProject.Name, "unreported-results", ".partial-123456")
	if err := os.WriteFile(partialFile, []byte(`{"truncated`), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := stateStore.ListUnreportedResultsOldestFirst(exampleProject)

	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("expected only the whole result, got %+v", results)
	}
}

func TestDeletingAnUnreportedResultRemovesOnlyThatResult(t *testing.T) {
	stateStore := ProjectStateStore{StateDirectory: t.TempDir()}
	keptResult := exampleResult("20261031T052700Z-aaaaaaa", time.Now())
	deletedResult := exampleResult("20261031T053200Z-bbbbbbb", time.Now())
	for _, result := range []domain.RunResult{keptResult, deletedResult} {
		if err := stateStore.SaveUnreportedResult(result); err != nil {
			t.Fatal(err)
		}
	}

	if err := stateStore.DeleteUnreportedResult(deletedResult); err != nil {
		t.Fatal(err)
	}

	results, err := stateStore.ListUnreportedResultsOldestFirst(exampleProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].RunIdentifier != keptResult.RunIdentifier {
		t.Errorf("got %+v", results)
	}
}

func TestEachProjectKeepsSeparateState(t *testing.T) {
	stateStore := ProjectStateStore{StateDirectory: t.TempDir()}
	otherProject := domain.Project{Name: "other-project"}
	if err := stateStore.SaveLastTestedCommit(exampleProject, "example-commit"); err != nil {
		t.Fatal(err)
	}
	if err := stateStore.SaveUnreportedResult(exampleResult("20261031T052700Z-094b1e7", time.Now())); err != nil {
		t.Fatal(err)
	}

	_, found, err := stateStore.ReadLastTestedCommit(otherProject)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("another project's last tested commit was visible")
	}
	results, err := stateStore.ListUnreportedResultsOldestFirst(otherProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("another project's results were visible: %+v", results)
	}
}
