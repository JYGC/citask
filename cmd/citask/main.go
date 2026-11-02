package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/JYGC/citask/internal/config"
	"github.com/JYGC/citask/internal/store"
	"github.com/JYGC/citask/internal/wiring"
)

func main() {
	if err := runCommand(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "citask:", err)
		os.Exit(1)
	}
}

func runCommand(arguments []string) error {
	switch {
	case len(arguments) == 1 && arguments[0] == "poll":
		return pollAllProjects()
	case len(arguments) == 2 && arguments[0] == "check-config":
		_, err := config.LoadFromFile(arguments[1])
		return err
	default:
		return errors.New("usage: citask poll | citask check-config <file>")
	}
}

func pollAllProjects() error {
	configuration, err := config.Load()
	if err != nil {
		return err
	}
	leftoverProcessStopper := store.NewLeftoverProcessStopper(wiring.ProcessStopGracePeriod)
	return wiring.BuildPollAllProjects(wiring.PollSettingsFromConfiguration(configuration), leftoverProcessStopper).Execute(context.Background())
}
