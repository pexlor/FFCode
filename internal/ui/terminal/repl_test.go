package terminal

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"MyCode/internal/agent"
)

var ansiSequence = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestAssistantLabelAndFirstTextRenderOnSameLine(t *testing.T) {
	var status bytes.Buffer
	var output bytes.Buffer
	renderer := newAgentEventRenderer(&status, &output)

	if err := renderer.render(agent.TextEvent{Text: "模型回复"}); err != nil {
		t.Fatalf("render text: %v", err)
	}
	if err := renderer.render(agent.DoneEvent{}); err != nil {
		t.Fatalf("finish render: %v", err)
	}

	plain := ansiSequence.ReplaceAllString(output.String(), "")
	firstLine := strings.SplitN(plain, "\n", 2)[0]
	if !strings.Contains(firstLine, "● 模型回复") {
		t.Fatalf("assistant label and text should share the first line; got %q", plain)
	}
}
