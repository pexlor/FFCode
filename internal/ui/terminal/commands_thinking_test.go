package terminal

import (
	"bytes"
	"context"
	"testing"
)

type fakeThinkingController struct {
	effort string
}

func (c *fakeThinkingController) SetThinkingEnabled(enabled bool) error {
	if enabled {
		c.effort = "medium"
	} else {
		c.effort = "off"
	}
	return nil
}

func (c *fakeThinkingController) ThinkingEnabled() (bool, error) { return c.effort != "off", nil }
func (c *fakeThinkingController) SetThinkingEffort(effort string) error {
	c.effort = effort
	return nil
}
func (c *fakeThinkingController) ThinkingEffort() (string, error) { return c.effort, nil }

func TestRunThinkingSetsEffort(t *testing.T) {
	controller := &fakeThinkingController{effort: "off"}
	var output bytes.Buffer
	result := runThinking(context.Background(), &CommandContext{Thinking: controller, Out: &output}, "high")
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if controller.effort != "high" || output.String() != "thinking: high (applies to subsequent requests)\n" {
		t.Fatalf("effort=%q output=%q", controller.effort, output.String())
	}
}

func TestRunThinkingOnMapsToMedium(t *testing.T) {
	controller := &fakeThinkingController{effort: "off"}
	result := runThinking(context.Background(), &CommandContext{Thinking: controller, Out: &bytes.Buffer{}}, "on")
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if controller.effort != "medium" {
		t.Fatalf("effort = %q", controller.effort)
	}
}

func TestRunThinkingStatusShowsEffort(t *testing.T) {
	controller := &fakeThinkingController{effort: "low"}
	var output bytes.Buffer
	result := runThinking(context.Background(), &CommandContext{Thinking: controller, Out: &output}, "status")
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if output.String() != "thinking: low\n" {
		t.Fatalf("output = %q", output.String())
	}
}
