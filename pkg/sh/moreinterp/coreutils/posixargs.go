package coreutils

import (
	"errors"
	"fmt"
	"strings"

	"github.com/richardartoul/swarmd/pkg/sh/internal"
)

func parseUtilityOptions(args []string, specs []internal.OptionSpec) ([]internal.ParsedOption, []string, error) {
	return parseUtilityOptionsWithConfig(args, specs, internal.ParseOptionsConfig{})
}

func parseUtilityOptionsToFirstOperand(args []string, specs []internal.OptionSpec) ([]internal.ParsedOption, []string, error) {
	return parseUtilityOptionsWithConfig(args, specs, internal.ParseOptionsConfig{
		StopAtOperand: true,
	})
}

func parseUtilityOptionsWithConfig(args []string, specs []internal.OptionSpec, cfg internal.ParseOptionsConfig) ([]internal.ParsedOption, []string, error) {
	opts, operands, err := internal.ParseOptions(args, specs, internal.ParseOptionsConfig{
		StopAtOperand: cfg.StopAtOperand,
	})
	if err != nil {
		return nil, nil, formatUtilityOptionError(err)
	}
	return opts, operands, nil
}

func formatUtilityOptionError(err error) error {
	var unknown *internal.UnknownOptionError
	if errors.As(err, &unknown) {
		name := unknown.Option
		if strings.HasPrefix(name, "--") {
			name = "-" + strings.TrimPrefix(name, "--")
		}
		return fmt.Errorf("flag provided but not defined: %s", name)
	}
	var missing *internal.MissingOptionValueError
	if errors.As(err, &missing) {
		return fmt.Errorf("flag needs an argument: %s", missing.Option)
	}
	return err
}
