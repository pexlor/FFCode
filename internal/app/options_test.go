package app

import "testing"

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
