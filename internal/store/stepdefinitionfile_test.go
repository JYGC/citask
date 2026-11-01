package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStepDefinitionFile(t *testing.T, content string) string {
	t.Helper()
	checkoutDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(checkoutDirectory, "ci-steps.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return checkoutDirectory
}

func assertStepDefinitionsAreRejectedNaming(t *testing.T, content, expectedInTheError string) {
	t.Helper()
	_, err := StepDefinitionFileReader{}.ReadStepDefinitions(writeStepDefinitionFile(t, content))
	if err == nil {
		t.Fatalf("expected %s to be rejected", content)
	}
	if !strings.Contains(err.Error(), expectedInTheError) {
		t.Fatalf("expected %q in: %v", expectedInTheError, err)
	}
}

func TestReadStepDefinitionsReadsEveryField(t *testing.T) {
	checkoutDirectory := writeStepDefinitionFile(t, `{"Steps":[
		{"Name":"build","WorkingDirectory":"frontend","Command":["make","build"]},
		{"Name":"test","WorkingDirectory":".","Command":["make","test"],
		 "Environment":{"EXAMPLE":"value"},"DependsOn":["build"],"RepeatOnceOnFailure":true}
	]}`)

	steps, err := StepDefinitionFileReader{}.ReadStepDefinitions(checkoutDirectory)

	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 {
		t.Fatalf("expected two steps, got %+v", steps)
	}
	if steps[0].Name != "build" || steps[0].WorkingDirectory != "frontend" || strings.Join(steps[0].Command, " ") != "make build" {
		t.Errorf("first step: %+v", steps[0])
	}
	if steps[1].Environment["EXAMPLE"] != "value" || steps[1].DependsOn[0] != "build" || !steps[1].RepeatOnceOnFailure {
		t.Errorf("second step: %+v", steps[1])
	}
}

func TestReadStepDefinitionsReportsAMissingFile(t *testing.T) {
	_, err := StepDefinitionFileReader{}.ReadStepDefinitions(t.TempDir())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected a not-exist error, got %v", err)
	}
	if !strings.Contains(err.Error(), "ci-steps.json") {
		t.Fatalf("expected the file to be named, got %v", err)
	}
}

func TestStepDefinitionsAreRejectedWhenNotJson(t *testing.T) {
	assertStepDefinitionsAreRejectedNaming(t, "{ not json", "ci-steps.json")
}

func TestStepDefinitionsAreRejectedWithTrailingContent(t *testing.T) {
	assertStepDefinitionsAreRejectedNaming(t,
		`{"Steps":[{"Name":"a","WorkingDirectory":".","Command":["true"]}]} {"Steps":[]}`, "ci-steps.json")
}

func TestStepDefinitionsAreRejectedWithAnUnknownField(t *testing.T) {
	assertStepDefinitionsAreRejectedNaming(t,
		`{"Steps":[{"Name":"a","WorkingDirectory":".","Command":["true"],"DependOn":["b"]}]}`, "DependOn")
}

func TestStepDefinitionsAreRejectedWithAnUnknownTopLevelField(t *testing.T) {
	assertStepDefinitionsAreRejectedNaming(t,
		`{"Stepz":[],"Steps":[{"Name":"a","WorkingDirectory":".","Command":["true"]}]}`, "Stepz")
}

func TestStepDefinitionsAreRejectedWithNoSteps(t *testing.T) {
	assertStepDefinitionsAreRejectedNaming(t, `{"Steps":[]}`, "at least one step")
}

func TestStepDefinitionsAreRejectedWithADuplicateStepName(t *testing.T) {
	assertStepDefinitionsAreRejectedNaming(t, `{"Steps":[
		{"Name":"same","WorkingDirectory":".","Command":["true"]},
		{"Name":"same","WorkingDirectory":".","Command":["true"]}
	]}`, "duplicate")
}

func TestStepDefinitionsAreRejectedWithTheReservedStepName(t *testing.T) {
	assertStepDefinitionsAreRejectedNaming(t,
		`{"Steps":[{"Name":"read-step-definitions","WorkingDirectory":".","Command":["true"]}]}`, "reserved")
}

func TestStepDefinitionsAreRejectedWithAMissingRequiredField(t *testing.T) {
	assertStepDefinitionsAreRejectedNaming(t, `{"Steps":[{"WorkingDirectory":".","Command":["true"]}]}`, "Name")
	assertStepDefinitionsAreRejectedNaming(t, `{"Steps":[{"Name":"a","Command":["true"]}]}`, "WorkingDirectory")
	assertStepDefinitionsAreRejectedNaming(t, `{"Steps":[{"Name":"a","WorkingDirectory":"."}]}`, "Command")
	assertStepDefinitionsAreRejectedNaming(t, `{"Steps":[{"Name":"a","WorkingDirectory":".","Command":[""]}]}`, "Command")
}

func TestStepDefinitionsAreRejectedWhenADependencyIsUnknown(t *testing.T) {
	assertStepDefinitionsAreRejectedNaming(t,
		`{"Steps":[{"Name":"a","WorkingDirectory":".","Command":["true"],"DependsOn":["nothing"]}]}`, "nothing")
}

func TestStepDefinitionsAreRejectedWhenADependencyIsALaterStep(t *testing.T) {
	assertStepDefinitionsAreRejectedNaming(t, `{"Steps":[
		{"Name":"first","WorkingDirectory":".","Command":["true"],"DependsOn":["second"]},
		{"Name":"second","WorkingDirectory":".","Command":["true"]}
	]}`, "second")
}

func TestStepDefinitionsAreRejectedWhenAStepDependsOnItself(t *testing.T) {
	assertStepDefinitionsAreRejectedNaming(t,
		`{"Steps":[{"Name":"a","WorkingDirectory":".","Command":["true"],"DependsOn":["a"]}]}`, "DependsOn")
}

func TestStepDefinitionsAreRejectedWhenAWorkingDirectoryLeavesTheRepo(t *testing.T) {
	for _, outsideDirectory := range []string{"..", "../outside", "folder/../../outside", "/etc"} {
		assertStepDefinitionsAreRejectedNaming(t,
			`{"Steps":[{"Name":"a","WorkingDirectory":"`+outsideDirectory+`","Command":["true"]}]}`, "WorkingDirectory")
	}
}

func TestStepDefinitionsNameEveryProblemAtOnce(t *testing.T) {
	content := `{"Steps":[
		{"Name":"first","WorkingDirectory":"..","Command":["true"]},
		{"Name":"second","WorkingDirectory":".","Command":[]}
	]}`
	_, err := StepDefinitionFileReader{}.ReadStepDefinitions(writeStepDefinitionFile(t, content))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, expectedInTheError := range []string{"first", "WorkingDirectory", "second", "Command"} {
		if !strings.Contains(err.Error(), expectedInTheError) {
			t.Errorf("expected %q in: %v", expectedInTheError, err)
		}
	}
}
