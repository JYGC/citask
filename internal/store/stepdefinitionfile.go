package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/JYGC/citask/internal/domain"
)

const stepDefinitionFileName = "ci-steps.json"

type StepDefinitionFileReader struct{}

func (StepDefinitionFileReader) ReadStepDefinitions(checkoutDirectory string) ([]domain.StepDefinition, error) {
	stepDefinitionFile, err := os.Open(filepath.Join(checkoutDirectory, stepDefinitionFileName))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", stepDefinitionFileName, err)
	}
	defer stepDefinitionFile.Close()

	var fileContent struct{ Steps []domain.StepDefinition }
	decoder := json.NewDecoder(stepDefinitionFile)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fileContent); err != nil {
		return nil, fmt.Errorf("decode %s: %w", stepDefinitionFileName, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode %s: content follows the first JSON value", stepDefinitionFileName)
	}
	if err := domain.ValidateStepDefinitions(fileContent.Steps); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", stepDefinitionFileName, err)
	}
	return fileContent.Steps, nil
}
