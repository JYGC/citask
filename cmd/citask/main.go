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
	if len(arguments) != 1 || arguments[0] != "poll" {
		return errors.New("usage: citask poll")
	}
	configuration, err := config.Load()
	if err != nil {
		return err
	}
	leftoverProcessStopper := store.NewLeftoverProcessStopper(wiring.ProcessStopGracePeriod)
	return wiring.BuildPollAllProjects(wiring.PollSettingsFromConfiguration(configuration), leftoverProcessStopper).Execute(context.Background())
}
