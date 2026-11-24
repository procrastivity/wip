package sync

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tiers"
)

type createRepoPayload struct {
	Name   string `json:"name"`
	Slug   string `json:"slug"`
	Remote string `json:"remote_url,omitempty"`
	Key    string `json:"creation_key"`
}

type pendingCreation struct {
	Version                                              int
	LocalRepo, Connection, Server, Account, Organization string
	Payload                                              createRepoPayload
}

func pendingCreationPath(repo string, create bool) (string, error) {
	path, err := credentialPath()
	if create {
		path, err = credentialDir()
	}
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), fmt.Sprintf("create-%x.json", sha256.Sum256([]byte(repo)))), nil
}

func readPendingCreation(repo string) (*pendingCreation, error) {
	path, err := pendingCreationPath(repo, false)
	if err != nil {
		return nil, err
	}
	raw, err := readPrivate(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pending pendingCreation
	if json.Unmarshal(raw, &pending) != nil || pending.Version != 1 || pending.LocalRepo != repo || pending.Payload.Key == "" {
		return nil, fmt.Errorf("sync: invalid pending Repo creation record")
	}
	return &pending, nil
}

// Publish a complete private record without overwriting another operation.
// A concurrent initializer either wins the link or reuses the winner's exact
// request. No lock spans HTTP, prompts or an unknown remote outcome.
func retainCreation(pending pendingCreation) (*pendingCreation, error) {
	var retained *pendingCreation
	err := withRegistryLock(func(_ string) error {
		if err := checkResetPending(pending.Connection); err != nil {
			return err
		}
		var err error
		retained, err = retainCreationLocked(pending)
		return err
	})
	return retained, err
}

func retainCreationLocked(pending pendingCreation) (*pendingCreation, error) {
	path, err := pendingCreationPath(pending.LocalRepo, true)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".create-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	err = json.NewEncoder(f).Encode(pending)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if err = os.Link(f.Name(), path); err == nil {
		return &pending, syncDirectory(filepath.Dir(path))
	}
	if !os.IsExist(err) {
		return nil, err
	}
	existing, err := readPendingCreation(pending.LocalRepo)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("sync: pending creation changed concurrently; retry init")
	}
	// A distinct payload/target cannot borrow another caller's operation key.
	a, b := *existing, pending
	a.Payload.Key = ""
	b.Payload.Key = ""
	if a != b {
		return nil, fmt.Errorf("sync: another creation request is pending; retry init to reconcile it")
	}
	return existing, nil
}

var repoSlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func promptCreation(repo store.Repo, selected selection, groups []organizationRepos, ui *onboardingUI) (*pendingCreation, error) {
	orgs := []remoteIdentity{}
	labels := []string{"Keep local"}
	for _, group := range groups {
		var org remoteIdentity
		if err := json.Unmarshal(group.Organization, &org); err != nil {
			return nil, err
		}
		orgs = append(orgs, org)
		labels = append(labels, org.Name+" ("+org.ID+")")
	}
	n, err := ui.choose("Choose organization for the new shared project", labels)
	if err != nil || n == 0 {
		return nil, err
	}
	name, err := ui.line("Project name", "Project")
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var slug string
	for {
		slug, err = ui.line("Project slug (lowercase letters, digits, hyphens)", "")
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if repoSlug.MatchString(slug) {
			break
		}
	}
	pending := pendingCreation{Version: 1, LocalRepo: repo.ID, Connection: selected.Connection, Server: selected.Server, Account: selected.Account, Organization: orgs[n-1].ID, Payload: createRepoPayload{Name: name, Slug: slug, Remote: repo.RemoteURL, Key: uuid.NewString()}}
	n, err = ui.choose(fmt.Sprintf("Create %q in %q via %s on %s (account %s), remote %q?", name, orgs[n-1].Name, selected.Connection, selected.Server, selected.Account, repo.RemoteURL), []string{"Keep local", "Create and pair"})
	if err != nil || n == 0 {
		return nil, err
	}
	return retainCreation(pending)
}

func onboardingCreate(ctx context.Context, repo store.Repo, selected selection, pending pendingCreation, ui *onboardingUI) (*OnboardingResult, error) {
	result := &OnboardingResult{State: "pending", Message: "Creation request retained for recovery."}
	// Creation and subsequent pairing are one authorization attempt: a reset
	// during POST must not let its old response borrow a new pairing generation.
	s, err := tiers.OpenStore()
	if err != nil {
		return onboardingFailed(result, err)
	}
	status, err := s.ExchangeStatus(ctx, repo.ID)
	var generation pairGeneration
	if err == nil {
		generation.Repo = status.Generation
		generation.Connection, err = s.ExchangeConnectionGeneration(ctx, selected.Connection)
	}
	closeErr := s.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = checkCreationCurrent(pending)
	}
	if err != nil {
		return onboardingFailed(result, err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	c, err := onlineConnection(requestCtx, selected)
	var remote remoteIdentity
	attempted := false
	if err == nil && (c.Account != pending.Account || c.Server != pending.Server) {
		err = fmt.Errorf("pending creation origin/account differs from login")
	}
	if err == nil {
		attempted = true
		err = newAPI(c.Server, c.Token).request(requestCtx, "POST", "/api/organizations/"+pending.Organization+"/repos", pending.Payload, &remote)
	}
	cancel()
	if err != nil {
		var status *httpStatusError
		// Validation/conflict responses are definitive no-create outcomes for
		// this request, so the next interactive attempt can correct its fields.
		if errors.As(err, &status) && (status.status == 422 || status.status == 409) {
			_ = removeCreation(pending)
		} else if attempted {
			err = fmt.Errorf("remote creation outcome may be unknown; the request may have been retired locally; do not create a replacement by slug: %w", err)
		}
		return onboardingFailed(result, err)
	}
	if !destinationID(remote.ID) {
		return onboardingFailed(result, fmt.Errorf("server returned no valid Repo identity; creation request retained"))
	}
	endpoint := pending.Server + "/api/organizations/" + pending.Organization + "/repos/" + remote.ID
	if err := checkCreationCurrent(pending); err != nil {
		return onboardingFailed(result, fmt.Errorf("remote Repo may already exist at %s; old creation response cannot pair after local retirement/replacement: %w", endpoint, err))
	}
	// Keep the operation through pairing as well. A lost response or failed
	// origin registration must not send the user back to a fresh creation.
	result, err = onboardingPair(ctx, repo, onboardingCandidate{Selection: selected, Organization: remoteIdentity{ID: pending.Organization}, Repo: remote}, false, &generation)
	if err != nil {
		if result == nil {
			result = &OnboardingResult{}
		}
		return onboardingFailed(result, fmt.Errorf("remote Repo may already exist at %s; local retirement was not undone: %w", endpoint, err))
	}
	err = removeCreation(pending)
	if err != nil && !os.IsNotExist(err) {
		return onboardingFailed(result, err)
	}
	return offerFirstSync(ctx, repo.ID, result, ui)
}

func checkCreationCurrent(pending pendingCreation) error {
	current, err := readPendingCreation(pending.LocalRepo)
	if err != nil {
		return err
	}
	if current == nil || *current != pending {
		return fmt.Errorf("creation request retired, missing or replaced; old request will not be paired")
	}
	return nil
}

func removeCreation(pending pendingCreation) error {
	return withRegistryLock(func(_ string) error {
		current, err := readPendingCreation(pending.LocalRepo)
		if err != nil || current == nil {
			return err
		}
		if *current != pending {
			return fmt.Errorf("sync: pending creation changed concurrently; replacement untouched")
		}
		path, err := pendingCreationPath(pending.LocalRepo, false)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		return syncDirectory(filepath.Dir(path))
	})
}
