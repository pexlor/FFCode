package prompt

import (
	_ "embed"
	"os"
	"strings"
	"time"
)

//go:embed system_prompt.md
var staticSystemPrompt string

func BuildSystemPrompt() (string, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return BuildSystemPromptForWorkspace(workingDirectory)
}

func BuildSystemPromptForWorkspace(workspace string) (string, error) {
	staticPrompt, err := buildStaticPrompt()
	if err != nil {
		return "", err
	}

	environmentPrompt, err := buildEnvironmentPrompt(workspace)
	if err != nil {
		return "", err
	}

	// todo: 后续添加 Agent.md
	sections := []string{
		staticPrompt,
		environmentPrompt,
	}
	return strings.Join(compactSections(sections), "\n\n"), nil
}

func buildEnvironmentPrompt(workingDirectory string) (string, error) {
	var builder strings.Builder

	now := time.Now()

	builder.WriteString("# Environment\n\n")
	builder.WriteString("- Current working directory: ")
	builder.WriteString(workingDirectory)
	builder.WriteString("\n")
	builder.WriteString("- Current time: ")
	builder.WriteString(now.Format("2006-01-02 15:04:05 MST"))
	return builder.String(), nil
}

func buildStaticPrompt() (string, error) {
	return staticSystemPrompt, nil
}

func compactSections(sections []string) []string {
	result := make([]string, 0, len(sections))
	for _, section := range sections {
		section = strings.TrimSpace(section)
		if section != "" {
			result = append(result, section)
		}
	}
	return result
}
