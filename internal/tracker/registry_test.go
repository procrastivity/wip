package tracker

import (
	"context"
	"reflect"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

type registrySeam struct{}

func (registrySeam) Deliver(context.Context, store.OutboxEntry) (Result, error) {
	return Result{}, nil
}

func TestRegistryResolvesNamesInStableOrder(t *testing.T) {
	registry := NewRegistry()
	registry.Register("second", func(store.Repo) (Seam, error) { return registrySeam{}, nil })
	registry.Register("first", func(store.Repo) (Seam, error) { return registrySeam{}, nil })
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("names = %v", got)
	}
	if _, err := registry.Resolve("first", store.Repo{}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := registry.Resolve("missing", store.Repo{}); err == nil {
		t.Fatal("missing provider resolved")
	}
}
