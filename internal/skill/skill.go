// Package skill loads and activates local agent SOPs.
package skill

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Mode string

const (
	Inline Mode = "inline"
	Fork   Mode = "fork"
)

type Scope string

const (
	Project Scope = "project"
	User    Scope = "user"
	Builtin Scope = "builtin"
)

type Source struct {
	Scope Scope
	Root  string
}

type SourceRef struct {
	Scope Scope
	Path  string
}

type Definition struct {
	Name, Description, ArgumentHint string
	Mode                            Mode
	AllowedTools                    []string
	Body                            string
	Source                          SourceRef
	SHA256                          string
}

type CatalogItem struct {
	Name, Description, ArgumentHint string
	Mode                            Mode
	Source                          SourceRef
}

type Catalog struct{ Items []CatalogItem }

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Registry holds an immutable-on-read snapshot. A complete reload is swapped
// in only after every source has been scanned successfully.
type Registry struct {
	mu      sync.RWMutex
	sources []Source
	items   map[string]Definition
	catalog Catalog
}

func NewRegistry(sources []Source) *Registry {
	return &Registry{sources: append([]Source(nil), sources...), items: make(map[string]Definition)}
}

func (r *Registry) Reload() error {
	if r == nil {
		return fmt.Errorf("skill registry is nil")
	}
	all := make(map[string][]Definition)
	for _, source := range r.sources {
		definitions, err := scanSource(source)
		if err != nil {
			return err
		}
		for _, definition := range definitions {
			all[definition.Name] = append(all[definition.Name], definition)
		}
	}
	resolved := make(map[string]Definition, len(all))
	for name, candidates := range all {
		best, err := selectDefinition(candidates)
		if err != nil {
			return err
		}
		resolved[name] = best
	}
	items := make([]CatalogItem, 0, len(resolved))
	for _, definition := range resolved {
		items = append(items, CatalogItem{Name: definition.Name, Description: definition.Description, ArgumentHint: definition.ArgumentHint, Mode: definition.Mode, Source: definition.Source})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	r.mu.Lock()
	r.items = resolved
	r.catalog = Catalog{Items: items}
	r.mu.Unlock()
	return nil
}

func (r *Registry) Catalog() Catalog {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return Catalog{Items: append([]CatalogItem(nil), r.catalog.Items...)}
}

func (r *Registry) Resolve(name string) (Definition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	definition, ok := r.items[normalizeName(name)]
	if !ok {
		return Definition{}, fmt.Errorf("skill %q is not available", name)
	}
	return definition, nil
}

func normalizeName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func validateDefinition(definition Definition) error {
	if !validName.MatchString(definition.Name) {
		return fmt.Errorf("invalid skill name %q", definition.Name)
	}
	if strings.TrimSpace(definition.Description) == "" {
		return fmt.Errorf("skill %q has an empty description", definition.Name)
	}
	if definition.Mode == "" {
		definition.Mode = Inline
	}
	if definition.Mode != Inline && definition.Mode != Fork {
		return fmt.Errorf("skill %q has unsupported mode %q", definition.Name, definition.Mode)
	}
	return nil
}

func selectDefinition(candidates []Definition) (Definition, error) {
	priority := map[Scope]int{Builtin: 1, User: 2, Project: 3}
	seenScopes := make(map[Scope]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, exists := seenScopes[candidate.Source.Scope]; exists {
			return Definition{}, fmt.Errorf("duplicate skill %q in %s scope", candidate.Name, candidate.Source.Scope)
		}
		seenScopes[candidate.Source.Scope] = struct{}{}
	}
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if priority[candidate.Source.Scope] > priority[best.Source.Scope] {
			best = candidate
		}
	}
	return best, nil
}

type ActiveSkill struct {
	Definition Definition
	Arguments  string
	Rendered   string
	LoadedAt   time.Time
}

// Manager owns task-local active skills. It intentionally does not persist
// them yet; transcript persistence will be added with session integration.
type Manager struct {
	registry  *Registry
	toolKnown func(string) bool
	mu        sync.RWMutex
	active    []ActiveSkill
}

func NewManager(registry *Registry, toolKnown func(string) bool) *Manager {
	return &Manager{registry: registry, toolKnown: toolKnown}
}

func (m *Manager) Load(name, arguments string) (ActiveSkill, error) {
	if m == nil || m.registry == nil {
		return ActiveSkill{}, fmt.Errorf("skill manager is not configured")
	}
	definition, err := m.registry.Resolve(name)
	if err != nil {
		return ActiveSkill{}, err
	}
	if definition.Mode == Fork {
		return ActiveSkill{}, fmt.Errorf("skill %q uses fork mode, which is not implemented yet", definition.Name)
	}
	for _, toolName := range definition.AllowedTools {
		if m.toolKnown != nil && !m.toolKnown(toolName) {
			return ActiveSkill{}, fmt.Errorf("skill %q references unknown tool %q", definition.Name, toolName)
		}
	}
	active := ActiveSkill{Definition: definition, Arguments: arguments, Rendered: render(definition.Body, definition.Name, arguments), LoadedAt: time.Now()}
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := range m.active {
		if m.active[index].Definition.Name == definition.Name {
			m.active[index] = active
			return active, nil
		}
	}
	if len(m.active) >= 3 {
		return ActiveSkill{}, fmt.Errorf("at most 3 skills may be active; unload one first")
	}
	m.active = append(m.active, active)
	return active, nil
}

func (m *Manager) Active() []ActiveSkill {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]ActiveSkill(nil), m.active...)
}

func (m *Manager) Unload(name string) bool {
	if m == nil {
		return false
	}
	name = normalizeName(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	for index, active := range m.active {
		if active.Definition.Name == name {
			m.active = append(m.active[:index], m.active[index+1:]...)
			return true
		}
	}
	return false
}

func (m *Manager) Instructions() string {
	active := m.Active()
	if len(active) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("# Active Skills\n\n")
	for _, item := range active {
		fmt.Fprintf(&builder, "## %s\n\n%s\n\n", item.Definition.Name, item.Rendered)
	}
	return strings.TrimSpace(builder.String())
}

// CatalogPrompt is deliberately limited to metadata so the initial request
// does not pay for every Skill body.
func (m *Manager) CatalogPrompt() string {
	if m == nil || m.registry == nil {
		return ""
	}
	catalog := m.registry.Catalog()
	if len(catalog.Items) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("# Available Skills\n\nLoad a skill with the load_skill tool only when its SOP is relevant.\n")
	for _, item := range catalog.Items {
		fmt.Fprintf(&builder, "- %s [%s]: %s", item.Name, item.Mode, item.Description)
		if item.ArgumentHint != "" {
			fmt.Fprintf(&builder, " Args: %s", item.ArgumentHint)
		}
		builder.WriteByte('\n')
	}
	return strings.TrimSpace(builder.String())
}

// AllowedTools returns the intersection of all declared active whitelists.
// Skills without a whitelist do not constrain the current tool set.
func (m *Manager) AllowedTools() map[string]struct{} {
	active := m.Active()
	var allowed map[string]struct{}
	for _, item := range active {
		if len(item.Definition.AllowedTools) == 0 {
			continue
		}
		current := make(map[string]struct{}, len(item.Definition.AllowedTools))
		for _, name := range item.Definition.AllowedTools {
			current[strings.ToLower(name)] = struct{}{}
		}
		if allowed == nil {
			allowed = current
			continue
		}
		for name := range allowed {
			if _, ok := current[name]; !ok {
				delete(allowed, name)
			}
		}
	}
	return allowed
}

func render(body, name, arguments string) string {
	parts := strings.Fields(arguments)
	replacer := []string{"$ARGUMENTS", arguments, "$ARGUMENT", arguments, "$0", name}
	for index, part := range parts {
		replacer = append(replacer, fmt.Sprintf("$%d", index+1), part)
	}
	return strings.NewReplacer(replacer...).Replace(body)
}

func checksum(content string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(content))) }
