package agent

import (
	"testing"

	"FFCode/internal/llm"
)

func TestDefaultVerificationClassifier(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    VerificationScope
		ok      bool
	}{
		{name: "go package", command: "go test ./internal/agent", want: VerificationPackage, ok: true},
		{name: "go full", command: "go test ./...", want: VerificationFull, ok: true},
		{name: "go vet package", command: "go vet ./internal/agent", want: VerificationPackage, ok: true},
		{name: "pytest", command: "python -m pytest tests/test_api.py -q", want: VerificationFocused, ok: true},
		{name: "django runner", command: "python tests/runtests.py auth_tests", want: VerificationFocused, ok: true},
		{name: "npm", command: "npm test", want: VerificationFull, ok: true},
		{name: "pnpm", command: "pnpm test", want: VerificationFull, ok: true},
		{name: "yarn", command: "yarn test", want: VerificationFull, ok: true},
		{name: "cargo", command: "cargo test parser", want: VerificationFocused, ok: true},
		{name: "make", command: "make test", want: VerificationFocused, ok: true},
		{name: "just", command: "just test", want: VerificationFocused, ok: true},
		{name: "diff check", command: "git diff --check", want: VerificationFallback, ok: true},
		{name: "compile only", command: "python -m py_compile module.py", want: VerificationFallback, ok: true},
		{name: "read only", command: "git status --short", want: VerificationUnknown, ok: false},
	}

	classifier := defaultVerificationClassifier{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := classifier.Classify(llm.ToolCallComplete{
				ToolName:  "Bash",
				Arguments: map[string]any{"command": test.command},
			})
			if got != test.want || ok != test.ok {
				t.Fatalf("Classify(%q) = %q, %t; want %q, %t", test.command, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestVerificationClassifierRejectsNonBashAndMissingCommand(t *testing.T) {
	classifier := defaultVerificationClassifier{}
	for _, call := range []llm.ToolCallComplete{
		{ToolName: "ReadFile", Arguments: map[string]any{"command": "go test ./..."}},
		{ToolName: "Bash", Arguments: map[string]any{}},
		{ToolName: "Bash", Arguments: map[string]any{"command": 42}},
	} {
		if scope, ok := classifier.Classify(call); ok || scope != VerificationUnknown {
			t.Fatalf("Classify(%+v) = %q, %t", call, scope, ok)
		}
	}
}
