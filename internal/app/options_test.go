package app

import (
	"strings"
	"testing"
)

func TestParseWorkspaceOption(t *testing.T) {
	got, err := parseWorkspaceOption([]string{"--cwd", "cmd/ffcode"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "cmd/ffcode" {
		t.Fatalf("cwd = %q, want cmd/ffcode", got)
	}
}

func TestParseWorkspaceOptionRejectsUnknownArgument(t *testing.T) {
	if _, err := parseWorkspaceOption([]string{"--unknown"}); err == nil {
		t.Fatal("unknown option was accepted")
	}
}

func TestParseOptionsDefaultsToTextOutput(t *testing.T) {
	options, err := parseOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.OutputFormat != OutputText {
		t.Fatalf("output format = %q, want %q", options.OutputFormat, OutputText)
	}
}

func TestParseOptionsAcceptsJSONLOutput(t *testing.T) {
	options, err := parseOptions([]string{"--cwd", "workspace", "--output-format", "jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	if options.Workspace != "workspace" || options.OutputFormat != OutputJSONL {
		t.Fatalf("options = %+v", options)
	}
}

func TestParseOptionsRejectsUnknownOutputFormat(t *testing.T) {
	_, err := parseOptions([]string{"--output-format", "xml"})
	if err == nil || !strings.Contains(err.Error(), "output format") {
		t.Fatalf("error = %v", err)
	}
}
