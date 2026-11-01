package store

import (
	"fmt"
	"os"
	"path/filepath"
)

const partialFilePattern = ".partial-*"

func writeFileAtomically(path string, content []byte, permissions os.FileMode) (returnedError error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", directory, err)
	}
	partialFile, err := os.CreateTemp(directory, partialFilePattern)
	if err != nil {
		return fmt.Errorf("create partial file in %s: %w", directory, err)
	}
	defer func() {
		if returnedError != nil {
			os.Remove(partialFile.Name())
		}
	}()
	defer partialFile.Close()

	if _, err := partialFile.Write(content); err != nil {
		return fmt.Errorf("write %s: %w", partialFile.Name(), err)
	}
	if err := partialFile.Chmod(permissions); err != nil {
		return fmt.Errorf("set permissions of %s: %w", partialFile.Name(), err)
	}
	if err := partialFile.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", partialFile.Name(), err)
	}
	if err := partialFile.Close(); err != nil {
		return fmt.Errorf("close %s: %w", partialFile.Name(), err)
	}
	if err := os.Rename(partialFile.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
