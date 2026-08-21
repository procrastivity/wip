package tracker

import (
	"fmt"
	"sort"
	"strings"

	"github.com/procrastivity/wip/internal/store"
)

// FactoryInput contains the provider-neutral values needed to construct one
// provider seam.
type FactoryInput struct {
	Repo   store.Repo
	Target string
}

// Factory constructs one provider seam. Provider packages expose a Factory
// and the CLI registers it here without adding provider names to the store's
// event or projection vocabulary.
type Factory func(FactoryInput) (Seam, error)

// Registry maps configured backend names to concrete provider factories.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry returns an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

// Register adds one backend. Duplicate and malformed registrations panic
// because registration is static program construction, not user input.
func (r *Registry) Register(name string, factory Factory) {
	name = strings.TrimSpace(name)
	if name == "" || factory == nil {
		panic("tracker: a provider registration needs a name and factory")
	}
	if _, exists := r.factories[name]; exists {
		panic("tracker: duplicate provider registration " + name)
	}
	r.factories[name] = factory
}

// Names lists registered backend names in stable order.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Resolve constructs the configured provider seam from provider-neutral input.
func (r *Registry) Resolve(name string, input FactoryInput) (Seam, error) {
	if r == nil {
		return nil, fmt.Errorf("tracker: backend %q is not registered", name)
	}
	factory, ok := r.factories[name]
	if !ok {
		return nil, fmt.Errorf("tracker: backend %q is not registered", name)
	}
	return factory(input)
}
