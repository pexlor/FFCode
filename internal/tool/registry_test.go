package tool

import (
	"context"
	"reflect"
	"testing"
)

type registryTestTool struct{ name string }

func (t registryTestTool) Name() string        { return t.name }
func (t registryTestTool) Description() string { return t.name }
func (t registryTestTool) Schema() *ToolSchema { return &ToolSchema{Name: t.name} }
func (t registryTestTool) Execute(context.Context, map[string]any) ToolResult {
	return ToolResult{}
}

func TestRegistrySchemasHaveStableNameOrder(t *testing.T) {
	registry := NewRegistry()
	for _, name := range []string{"WriteFile", "Grep", "Bash", "ReadFile", "EditFile"} {
		registry.Register(registryTestTool{name: name})
	}
	want := []string{"Bash", "EditFile", "Grep", "ReadFile", "WriteFile"}

	for range 20 {
		schemas := registry.Schemas()
		got := make([]string, len(schemas))
		for index, schema := range schemas {
			got[index] = schema.Name
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("schema names = %v, want %v", got, want)
		}
	}
}
