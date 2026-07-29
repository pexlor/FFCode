package agent

import (
	"strings"

	"MyCode/internal/llm"
)

type VerificationScope string

const (
	VerificationUnknown  VerificationScope = "unknown"
	VerificationFocused  VerificationScope = "focused"
	VerificationPackage  VerificationScope = "package"
	VerificationFull     VerificationScope = "full"
	VerificationFallback VerificationScope = "fallback"
)

type VerificationClassifier interface {
	Classify(llm.ToolCallComplete) (VerificationScope, bool)
}

type defaultVerificationClassifier struct{}

func (defaultVerificationClassifier) Classify(call llm.ToolCallComplete) (VerificationScope, bool) {
	if !strings.EqualFold(strings.TrimSpace(call.ToolName), "bash") {
		return VerificationUnknown, false
	}
	command, ok := call.Arguments["command"].(string)
	if !ok {
		return VerificationUnknown, false
	}
	command = strings.ToLower(strings.TrimSpace(command))
	if command == "" {
		return VerificationUnknown, false
	}

	for _, marker := range []string{"git diff --check", "py_compile", "compileall"} {
		if strings.Contains(command, marker) {
			return VerificationFallback, true
		}
	}
	if strings.Contains(command, "go test ./...") || isJavaScriptFullTest(command) {
		return VerificationFull, true
	}
	for _, marker := range []string{"pytest", "tests/runtests.py", "cargo test", "make test", "just test"} {
		if strings.Contains(command, marker) {
			return VerificationFocused, true
		}
	}
	if strings.Contains(command, "go test ") || strings.Contains(command, "go vet ") {
		return VerificationPackage, true
	}
	return VerificationUnknown, false
}

func isJavaScriptFullTest(command string) bool {
	for _, prefix := range []string{"npm test", "pnpm test", "yarn test"} {
		if command == prefix || strings.HasPrefix(command, prefix+" ") {
			return true
		}
	}
	return false
}
