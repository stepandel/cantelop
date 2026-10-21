package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

// parseFlags parses a command that takes no positional arguments. Like every
// command, it accepts flags in any position and reports a parse error with the
// command's usage appended.
func parseFlags(flags *flag.FlagSet, args []string, usage string) error {
	positionals, err := parseInterspersedFlags(flags, args, usage)
	if err == nil && len(positionals) != 0 {
		err = fmt.Errorf("unexpected argument %q\n%s", positionals[0], usage)
	}
	return err
}

// parseInterspersedFlags parses flags before, between and after positional
// arguments and returns the positional arguments in order. Arguments after
// "--" are positional.
func parseInterspersedFlags(flags *flag.FlagSet, args []string, usage string) ([]string, error) {
	flags.SetOutput(io.Discard)
	var positionals []string
	for {
		if err := flags.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, errors.New(usage)
			}
			return nil, fmt.Errorf("%w\n%s", err, usage)
		}
		rest := flags.Args()
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			return append(positionals, rest...), nil
		}
		if len(rest) == 0 {
			return positionals, nil
		}
		positionals = append(positionals, rest[0])
		args = rest[1:]
	}
}
