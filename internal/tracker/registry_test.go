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
	var received FactoryInput
	registry.Register("second", func(FactoryInput) (Seam, error) { return registrySeam{}, nil })
	registry.Register("first", func(input FactoryInput) (Seam, error) {
		received = input
		return registrySeam{}, nil
	})
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("names = %v", got)
	}
	input := FactoryInput{Repo: store.Repo{ID: "repo-1"}, Target: "target-1", CanceledLabel: "label-1"}
	if _, err := registry.Resolve("first", input); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !reflect.DeepEqual(received, input) {
		t.Fatalf("factory input = %+v, want %+v", received, input)
	}
	if _, err := registry.Resolve("missing", FactoryInput{}); err == nil {
		t.Fatal("missing provider resolved")
	}
}
