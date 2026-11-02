package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests never run the poll command: citask enrols itself, so they run as _ci inside a live run,
// where the real leftover process stopper would stop that run.

func writeConfigurationFile(t *testing.T, content string) string {
	t.Helper()
	configurationFile := filepath.Join(t.TempDir(), "citask.json")
	if err := os.WriteFile(configurationFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return configurationFile
}

func TestCheckConfigAcceptsAValidFile(t *testing.T) {
	if err := runCommand([]string{"check-config", filepath.Join("..", "..", "citask.example.json")}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckConfigRejectsAnInvalidFileNamingTheField(t *testing.T) {
	err := runCommand([]string{"check-config", writeConfigurationFile(t, `{"CiHomeDirectory":"relative"}`)})

	if err == nil || !strings.Contains(err.Error(), "CiHomeDirectory") {
		t.Fatalf("expected the field to be named, got %v", err)
	}
}

func TestCheckConfigReportsAMissingFile(t *testing.T) {
	if err := runCommand([]string{"check-config", filepath.Join(t.TempDir(), "citask.json")}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestAnythingElseIsRefusedWithTheUsage(t *testing.T) {
	for _, arguments := range [][]string{nil, {"unknown"}, {"poll", "extra"}, {"check-config"}, {"check-config", "a", "b"}} {
		err := runCommand(arguments)
		if err == nil || !strings.Contains(err.Error(), "usage") {
			t.Errorf("%v: expected the usage, got %v", arguments, err)
		}
	}
}
