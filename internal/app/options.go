package app

import (
	"flag"
	"fmt"
	"io"
)

type Options struct {
	Workspace    string
	OutputFormat string
}

const (
	OutputText  = "text"
	OutputJSONL = "jsonl"
)

func parseWorkspaceOption(arguments []string) (string, error) {
	options, err := parseOptions(arguments)
	return options.Workspace, err
}

func parseOptions(arguments []string) (Options, error) {
	flags := flag.NewFlagSet("MyCode", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	cwd := flags.String("cwd", "", "workspace directory")
	outputFormat := flags.String("output-format", OutputText, "output format: text or jsonl")
	if err := flags.Parse(arguments); err != nil {
		return Options{}, err
	}
	if flags.NArg() != 0 {
		return Options{}, fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if *outputFormat != OutputText && *outputFormat != OutputJSONL {
		return Options{}, fmt.Errorf("unsupported output format %q", *outputFormat)
	}
	return Options{Workspace: *cwd, OutputFormat: *outputFormat}, nil
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
  MyCode --cwd <directory> [--output-format text|jsonl]

Options:
  -h, --help       Show this help message
  -v, --version    Show the MyCode version
      --cwd <dir>  Use an explicit workspace directory
      --output-format <format>
                   Output format: text (default) or jsonl
`)
}
