package tracker

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/store"
)

const (
	stubProjectUUID = "550e8400-e29b-41d4-a716-446655440000"
	// The three opaque values carry deliberate surrounding whitespace: the
	// resolver must hand them to the factory exactly as stored.
	stubTarget  = "  team-1  "
	stubLabel   = "  wf::canceled  "
	stubProject = "  " + stubProjectUUID + "  "
)

// stubSeamView is the minimal local read surface ResolveSeam needs. It records
// every config key it is asked for, in order, and counts Repo reads, so a test
// can prove which reads the resolver makes and which it skips.
type stubSeamView struct {
	values    map[string]string
	configErr map[string]error
	repo      store.Repo
	repoErr   error
	keys      []string
	repoReads int
}

func (s *stubSeamView) Config(_ context.Context, _, key string) (string, bool, error) {
	s.keys = append(s.keys, key)
	if err := s.configErr[key]; err != nil {
		return "", false, err
	}
	value, present := s.values[key]
	return value, present, nil
}

func (s *stubSeamView) Repo(context.Context, string) (store.Repo, error) {
	s.repoReads++
	if s.repoErr != nil {
		return store.Repo{}, s.repoErr
	}
	return s.repo, nil
}

func configuredSeamView(backend string) *stubSeamView {
	return &stubSeamView{
		values: map[string]string{
			store.TrackerBackendKey:       backend,
			store.TrackerTargetKey:        stubTarget,
			store.TrackerCanceledLabelKey: stubLabel,
			store.TrackerProjectKey:       stubProject,
		},
		configErr: map[string]error{},
		repo:      store.Repo{ID: "repo-1", RemoteURL: "git@example.com:acme/wip.git"},
	}
}

type stubSeam struct{}

func (stubSeam) Deliver(context.Context, store.OutboxEntry) (Result, error) {
	return Result{}, nil
}

// factoryCapture records what the registry handed the provider factory.
type factoryCapture struct{ inputs []FactoryInput }

func capturingRegistry(name string, factoryErr error) (*Registry, *factoryCapture) {
	capture := &factoryCapture{}
	registry := NewRegistry()
	registry.Register(name, func(input FactoryInput) (Seam, error) {
		capture.inputs = append(capture.inputs, input)
		if factoryErr != nil {
			return nil, factoryErr
		}
		return stubSeam{}, nil
	})
	return registry, capture
}

func TestReadSeamConfigReadsEveryTrackerKeyThenTheRepoRow(t *testing.T) {
	view := configuredSeamView("  fake  ")

	config, err := ReadSeamConfig(context.Background(), view, "repo-1")
	if err != nil {
		t.Fatalf("read seam config: %v", err)
	}
	wantKeys := []string{
		store.TrackerBackendKey, store.TrackerTargetKey,
		store.TrackerCanceledLabelKey, store.TrackerProjectKey,
	}
	if !reflect.DeepEqual(view.keys, wantKeys) {
		t.Fatalf("config reads = %v, want %v", view.keys, wantKeys)
	}
	if view.repoReads != 1 {
		t.Fatalf("repo reads = %d, want exactly one", view.repoReads)
	}
	want := SeamConfig{
		Backend: "fake", Target: stubTarget, CanceledLabel: stubLabel,
		Project: stubProject, Repo: view.repo,
	}
	if !reflect.DeepEqual(config, want) {
		t.Fatalf("seam config = %+v, want %+v; only the backend is trimmed", config, want)
	}
}

func TestReadSeamConfigStopsAtAnUnconfiguredBackend(t *testing.T) {
	for _, backend := range []string{"", "none", "  none  ", "absent"} {
		t.Run(fmt.Sprintf("backend %q", backend), func(t *testing.T) {
			view := configuredSeamView(backend)
			if backend == "absent" {
				delete(view.values, store.TrackerBackendKey)
			}
			view.repoErr = errors.New("the repo row must not be read")

			config, err := ReadSeamConfig(context.Background(), view, "repo-1")
			if err != nil {
				t.Fatalf("unconfigured backend errored: %v", err)
			}
			if !reflect.DeepEqual(view.keys, []string{store.TrackerBackendKey}) {
				t.Fatalf("config reads = %v, want only the backend key", view.keys)
			}
			if view.repoReads != 0 {
				t.Fatalf("repo reads = %d, want none without a backend", view.repoReads)
			}
			if config.Target != "" || config.CanceledLabel != "" || config.Project != "" || config.Repo != (store.Repo{}) {
				t.Fatalf("unconfigured seam config = %+v, want the remaining fields zero", config)
			}
		})
	}
}

func TestResolveSeamReturnsErrNoBackendForUnsetAndNone(t *testing.T) {
	for _, backend := range []string{"", "none", "  none  "} {
		t.Run(fmt.Sprintf("backend %q", backend), func(t *testing.T) {
			view := configuredSeamView(backend)
			registry, capture := capturingRegistry("fake", nil)

			seam, _, err := ResolveSeam(context.Background(), view, registry, "repo-1")
			if !errors.Is(err, ErrNoBackend) {
				t.Fatalf("error = %v, want ErrNoBackend", err)
			}
			if err.Error() != "tracker: no provider seam configured" {
				t.Fatalf("message = %q; the flush verb's refusal text is a CLI contract", err.Error())
			}
			if seam != nil {
				t.Fatalf("seam = %+v, want none", seam)
			}
			if len(capture.inputs) != 0 {
				t.Fatalf("factory ran without a backend: %+v", capture.inputs)
			}
		})
	}
}

func TestResolveSeamHandsEveryConfiguredValueToTheFactory(t *testing.T) {
	view := configuredSeamView("fake")
	registry, capture := capturingRegistry("fake", nil)

	seam, config, err := ResolveSeam(context.Background(), view, registry, "repo-1")
	if err != nil || seam == nil {
		t.Fatalf("resolve seam = %+v, err %v", seam, err)
	}
	want := FactoryInput{
		Repo: view.repo, Target: stubTarget,
		CanceledLabel: stubLabel, Project: stubProject,
	}
	if len(capture.inputs) != 1 || !reflect.DeepEqual(capture.inputs[0], want) {
		t.Fatalf("factory input = %+v, want exactly one %+v", capture.inputs, want)
	}
	if config.Backend != "fake" {
		t.Fatalf("returned config backend = %q, want the trimmed name", config.Backend)
	}
}

func TestResolveSeamSurfacesConstructionAndReadFailures(t *testing.T) {
	// A factory that validates its project the way the Linear provider does.
	projectStrictRegistry := func() *Registry {
		registry := NewRegistry()
		registry.Register("fake", func(input FactoryInput) (Seam, error) {
			if project := strings.TrimSpace(input.Project); project != "" && project != stubProjectUUID {
				return nil, fmt.Errorf("fake tracker: project %q is not a project UUID", input.Project)
			}
			return stubSeam{}, nil
		})
		return registry
	}

	t.Run("malformed project", func(t *testing.T) {
		view := configuredSeamView("fake")
		view.values[store.TrackerProjectKey] = "not-a-uuid"

		seam, _, err := ResolveSeam(context.Background(), view, projectStrictRegistry(), "repo-1")
		if err == nil || seam != nil {
			t.Fatalf("malformed project resolved: seam=%+v err=%v", seam, err)
		}
		if errors.Is(err, ErrNoBackend) {
			t.Fatalf("malformed project reported as no backend: %v", err)
		}
		if !strings.Contains(err.Error(), "is not a project UUID") {
			t.Fatalf("error = %q, want the provider's construction failure verbatim", err.Error())
		}
	})

	t.Run("well-formed project constructs", func(t *testing.T) {
		view := configuredSeamView("fake")

		seam, _, err := ResolveSeam(context.Background(), view, projectStrictRegistry(), "repo-1")
		if err != nil || seam == nil {
			t.Fatalf("well-formed project = %+v, err %v", seam, err)
		}
	})

	t.Run("config read failure", func(t *testing.T) {
		view := configuredSeamView("fake")
		view.configErr[store.TrackerCanceledLabelKey] = errors.New("config table is gone")
		registry, capture := capturingRegistry("fake", nil)

		seam, _, err := ResolveSeam(context.Background(), view, registry, "repo-1")
		if err == nil || !strings.Contains(err.Error(), "config table is gone") || seam != nil {
			t.Fatalf("config failure = %v, seam %+v", err, seam)
		}
		if len(capture.inputs) != 0 {
			t.Fatalf("factory ran after a failed read: %+v", capture.inputs)
		}
	})

	t.Run("repo read failure", func(t *testing.T) {
		view := configuredSeamView("fake")
		view.repoErr = errors.New("store: no repo repo-1")
		registry, capture := capturingRegistry("fake", nil)

		seam, _, err := ResolveSeam(context.Background(), view, registry, "repo-1")
		if err == nil || !strings.Contains(err.Error(), "no repo repo-1") || seam != nil {
			t.Fatalf("repo failure = %v, seam %+v", err, seam)
		}
		if len(capture.inputs) != 0 {
			t.Fatalf("factory ran without a repo row: %+v", capture.inputs)
		}
	})

	t.Run("unregistered backend", func(t *testing.T) {
		view := configuredSeamView("jira")
		registry, _ := capturingRegistry("fake", nil)

		seam, config, err := ResolveSeam(context.Background(), view, registry, "repo-1")
		if err == nil || seam != nil {
			t.Fatalf("unregistered backend resolved: seam=%+v err=%v", seam, err)
		}
		if errors.Is(err, ErrNoBackend) {
			t.Fatalf("unregistered backend reported as no backend: %v", err)
		}
		if !strings.Contains(err.Error(), `backend "jira" is not registered`) {
			t.Fatalf("error = %q, want the registry's refusal", err.Error())
		}
		if config.Backend != "jira" {
			t.Fatalf("returned config backend = %q, want the attempted name", config.Backend)
		}
	})
}
