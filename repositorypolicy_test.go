package citask_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var namesOfEnrolledProjects = []string{"skill" + "survey", "key" + "book"}

var realStopperConstructorCall = "NewLeftoverProcess" + "Stopper("

var pathsOutsideTheSource = map[string]bool{
	".git":            true,
	".claude":         true,
	"build":           true,
	"citask.json":     true,
	"CLAUDE.local.md": true,
}

func walkSourceFiles(rootDirectory string, visitFile func(relativePath string, content string) error) error {
	return filepath.WalkDir(rootDirectory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if pathsOutsideTheSource[entry.Name()] {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relativePath, err := filepath.Rel(rootDirectory, path)
		if err != nil {
			return err
		}
		return visitFile(relativePath, string(content))
	})
}

func findFilesNamingAnEnrolledProject(t *testing.T, rootDirectory string) []string {
	t.Helper()
	filesFound := []string{}
	err := walkSourceFiles(rootDirectory, func(relativePath, content string) error {
		searchableText := strings.ToLower(relativePath + "\n" + content)
		for _, projectName := range namesOfEnrolledProjects {
			if strings.Contains(searchableText, projectName) {
				filesFound = append(filesFound, relativePath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return filesFound
}

func findFilesCallingTheRealStopper(t *testing.T, rootDirectory string) []string {
	t.Helper()
	filesFound := []string{}
	err := walkSourceFiles(rootDirectory, func(relativePath, content string) error {
		isGoFile := strings.HasSuffix(relativePath, ".go")
		if isGoFile && strings.Contains(strings.ReplaceAll(content, "func "+realStopperConstructorCall, ""), realStopperConstructorCall) {
			filesFound = append(filesFound, relativePath)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return filesFound
}

func TestSourceNamesNoEnrolledProject(t *testing.T) {
	if filesFound := findFilesNamingAnEnrolledProject(t, "."); len(filesFound) != 0 {
		t.Errorf("the pipeline must stay reusable, but these files name an enrolled project: %v", filesFound)
	}
}

func TestPlantedProjectNameIsFoundInContentAndInFileNamesWhateverItsCase(t *testing.T) {
	rootDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDirectory, "content.go"), []byte("// Skill"+"Survey only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDirectory, "Key"+"Book-notes.md"), []byte("nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	filesFound := findFilesNamingAnEnrolledProject(t, rootDirectory)

	if len(filesFound) != 2 {
		t.Errorf("expected both planted names to be found, got %v", filesFound)
	}
}

func TestOnlyMainCallsTheRealLeftoverProcessStopper(t *testing.T) {
	filesFound := findFilesCallingTheRealStopper(t, ".")

	if len(filesFound) != 1 || filesFound[0] != filepath.Join("cmd", "citask", "main.go") {
		t.Errorf("citask tests itself inside a live run, where a real sweep would stop that run, but these files call it: %v", filesFound)
	}
}

func TestPlantedCallToTheRealStopperIsFound(t *testing.T) {
	rootDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDirectory, "planted_test.go"), []byte("store."+realStopperConstructorCall+"0).StopLeftoverProcesses()\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if filesFound := findFilesCallingTheRealStopper(t, rootDirectory); len(filesFound) != 1 {
		t.Errorf("expected the planted call to be found, got %v", filesFound)
	}
}
