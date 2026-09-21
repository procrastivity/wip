package tracker

import (
	"context"
	"errors"
	"strings"

	"github.com/procrastivity/wip/internal/store"
)

// SeamView is the local read surface the shared resolver needs: the Repo-tier
// tracker config and the Repo row itself. store.View satisfies it, and
// AlignmentView's own Config and Repo methods match it exactly, so an
// alignment view can be handed straight to ResolveSeam.
type SeamView interface {
	Config(context.Context, string, string) (string, bool, error)
	Repo(context.Context, string) (store.Repo, error)
}

var _ SeamView = store.View{}

// ErrNoBackend reports that this repo configures no provider backend, either
// because tracker.backend is unset or because it is the "none" sentinel. Its
// text is the refusal the flush verb has always printed, so callers can return
// it unwrapped.
var ErrNoBackend = errors.New("tracker: no provider seam configured")

// SeamConfig is the Repo-tier configuration one provider seam is constructed
// from. Backend is trimmed because the registry looks it up by name; Target,
// CanceledLabel and Project stay exactly as stored, because they are opaque
// provider tokens that only the configured backend interprets. Repo is the
// zero Repo when Backend names no provider.
type SeamConfig struct {
	Backend       string
	Target        string
	CanceledLabel string
	Project       string
	Repo          store.Repo
}

// ReadSeamConfig reads the four tracker.* config keys and the Repo row for one
// repo. It reads the backend first and returns immediately when no backend is
// configured, so an unconfigured repo costs one read and never fails on an
// unrelated missing row. The returned SeamConfig is only meaningful when the
// error is nil; on a read failure it is the zero SeamConfig. ResolveSeam, by
// contrast, returns the populated config alongside a factory construction
// error.
func ReadSeamConfig(ctx context.Context, v SeamView, repoID string) (SeamConfig, error) {
	backend, _, err := v.Config(ctx, repoID, store.TrackerBackendKey)
	if err != nil {
		return SeamConfig{}, err
	}
	config := SeamConfig{Backend: strings.TrimSpace(backend)}
	if !backendConfigured(config.Backend) {
		return config, nil
	}
	target, _, err := v.Config(ctx, repoID, store.TrackerTargetKey)
	if err != nil {
		return SeamConfig{}, err
	}
	canceledLabel, _, err := v.Config(ctx, repoID, store.TrackerCanceledLabelKey)
	if err != nil {
		return SeamConfig{}, err
	}
	project, _, err := v.Config(ctx, repoID, store.TrackerProjectKey)
	if err != nil {
		return SeamConfig{}, err
	}
	repo, err := v.Repo(ctx, repoID)
	if err != nil {
		return SeamConfig{}, err
	}
	config.Target, config.CanceledLabel, config.Project, config.Repo = target, canceledLabel, project, repo
	return config, nil
}

// ResolveSeam constructs the configured provider seam for one repo. It is the
// single place that turns Repo-tier config into a FactoryInput, so every call
// site builds the same seam from the same four values. It returns ErrNoBackend
// when the repo configures no backend; every other failure is the store read
// error or the provider's own construction error, unwrapped. The SeamConfig is
// returned alongside the seam for callers that need the backend name.
func ResolveSeam(ctx context.Context, v SeamView, providers *Registry, repoID string) (Seam, SeamConfig, error) {
	config, err := ReadSeamConfig(ctx, v, repoID)
	if err != nil {
		return nil, SeamConfig{}, err
	}
	if !backendConfigured(config.Backend) {
		return nil, config, ErrNoBackend
	}
	seam, err := providers.Resolve(config.Backend, FactoryInput{
		Repo:          config.Repo,
		Target:        config.Target,
		CanceledLabel: config.CanceledLabel,
		Project:       config.Project,
	})
	if err != nil {
		return nil, config, err
	}
	return seam, config, nil
}

// backendConfigured reports whether a trimmed tracker.backend value names a
// provider. The "none" sentinel is accepted defensively and means the same
// as unset; the verb surface itself stores the empty string, rewriting
// "none" to "" before storing.
func backendConfigured(backend string) bool {
	return backend != "" && backend != "none"
}

// Configured reports whether c names a provider backend to construct a seam
// from. It is the same test ReadSeamConfig and ResolveSeam apply to Backend
// internally, exposed so a caller holding a SeamConfig (capabilities, for
// one) need not repeat the "" / "none" sentinel check by hand.
func (c SeamConfig) Configured() bool {
	return backendConfigured(c.Backend)
}
