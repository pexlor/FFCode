package tool

import "fmt"

// Registry owns tool discovery and schema publication. Execution and
// authorization are deliberately handled by ToolsManager.
type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

func (r *Registry) Register(registered Tool) {
	if r == nil || registered == nil {
		return
	}
	r.tools[registered.Name()] = registered
}

func (r *Registry) Get(name string) Tool {
	if r == nil {
		return nil
	}
	return r.tools[name]
}

func (r *Registry) Schemas() []*ToolSchema {
	if r == nil {
		return nil
	}
	schemas := make([]*ToolSchema, 0, len(r.tools))
	for _, registered := range r.tools {
		schemas = append(schemas, registered.Schema())
	}
	return schemas
}

func (r *Registry) SelectSchemas(names []string) ([]*ToolSchema, error) {
	schemas := make([]*ToolSchema, 0, len(names))
	for _, name := range names {
		registered := r.Get(name)
		if registered == nil {
			return nil, fmt.Errorf("tool %q is not registered", name)
		}
		schemas = append(schemas, registered.Schema())
	}
	return schemas, nil
}
