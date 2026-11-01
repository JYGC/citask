package domain

import (
	"errors"
	"fmt"
	"path/filepath"
)

// ReadStepDefinitionsStepName is how a broken step definition file appears in reports, so no step in
// that file may use it.
const ReadStepDefinitionsStepName = "read-step-definitions"

func ValidateStepDefinitions(steps []StepDefinition) error {
	if len(steps) == 0 {
		return errors.New("Steps must list at least one step, because a run that tests nothing would report a pass")
	}

	var validationErrors []error
	namesOfEarlierSteps := map[string]bool{}
	for stepIndex, step := range steps {
		stepLabel := fmt.Sprintf("step %d (%q)", stepIndex+1, step.Name)
		validationErrors = append(validationErrors, validateStepFields(stepLabel, step)...)
		if namesOfEarlierSteps[step.Name] {
			validationErrors = append(validationErrors, fmt.Errorf("%s has a duplicate Name", stepLabel))
		}
		for _, dependency := range step.DependsOn {
			if !namesOfEarlierSteps[dependency] {
				validationErrors = append(validationErrors, fmt.Errorf("%s has DependsOn %q, which is not an earlier step", stepLabel, dependency))
			}
		}
		namesOfEarlierSteps[step.Name] = true
	}
	return errors.Join(validationErrors...)
}

func validateStepFields(stepLabel string, step StepDefinition) []error {
	var validationErrors []error
	if step.Name == "" {
		validationErrors = append(validationErrors, fmt.Errorf("%s must have a Name", stepLabel))
	}
	if step.Name == ReadStepDefinitionsStepName {
		validationErrors = append(validationErrors, fmt.Errorf("%s uses a reserved Name", stepLabel))
	}
	if step.WorkingDirectory == "" {
		validationErrors = append(validationErrors, fmt.Errorf("%s must have a WorkingDirectory", stepLabel))
	} else if !filepath.IsLocal(step.WorkingDirectory) {
		validationErrors = append(validationErrors, fmt.Errorf("%s has WorkingDirectory %q, which leaves the repo", stepLabel, step.WorkingDirectory))
	}
	if len(step.Command) == 0 || step.Command[0] == "" {
		validationErrors = append(validationErrors, fmt.Errorf("%s must have a Command", stepLabel))
	}
	return validationErrors
}
