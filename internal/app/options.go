package app

import (
	"flag"
	"fmt"
	"io"
)

type Options struct {
	Workspace string
}

func parseWorkspaceOption(arguments []string) (string, error) {
	flags := flag.NewFlagSet("MyCode", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	cwd := flags.String("cwd", "", "workspace directory")
	if err := flags.Parse(arguments); err != nil {
		return "", err
	}
	if flags.NArg() != 0 {
		return "", fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	return *cwd, nil
}

func parseOptions(arguments []string) (Options, error) {
	workspace, err := parseWorkspaceOption(arguments)
	if err != nil {
		return Options{}, err
	}
	return Options{Workspace: workspace}, nil
}

func validateStandaloneOption(arguments []string, name string) error {
	if len(arguments) != 1 {
		return fmt.Errorf("%s does not accept arguments", name)
	}
	return nil
}

func printUsage(out io.Writer) {
	fmt.Fprint(out, `MyCode - terminal coding assistant

Usage:
  MyCode [option]
  MyCode --cwd <directory>

Options:
  -h, --help       Show this help message
  -v, --version    Show the MyCode version
      --cwd <dir>  Use an explicit workspace directory
`)
}
