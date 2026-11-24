package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/procrastivity/wip/internal/store"
)

type organizationRepos struct {
	Organization json.RawMessage   `json:"organization"`
	Repos        []json.RawMessage `json:"repos"`
}

func discoverRepos(ctx context.Context, selected selection) ([]organizationRepos, error) {
	c, err := onlineConnection(ctx, selected)
	if err != nil {
		return nil, err
	}
	api := newAPI(c.Server, c.Token)
	orgs, err := api.list(ctx, "/api/organizations")
	if err != nil {
		return nil, err
	}
	var out []organizationRepos
	for _, org := range orgs {
		var identity struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(org, &identity); err != nil {
			return nil, err
		}
		if !destinationID(identity.ID) {
			return nil, fmt.Errorf("sync: invalid organization identity")
		}
		repos, err := api.list(ctx, "/api/organizations/"+identity.ID+"/repos")
		if err != nil {
			return nil, err
		}
		out = append(out, organizationRepos{org, repos})
	}
	return out, nil
}

func destinationID(id string) bool { return id != "" && !strings.ContainsAny(id, "/?#%\\ \t\r\n") }

type pairGeneration struct{ Repo, Connection string }

func applyPair(ctx context.Context, s *store.Store, repo string, selected selection, org, remote string, expected *pairGeneration) (store.ExchangeStatus, json.RawMessage, error) {
	status, err := s.ExchangeStatus(ctx, repo)
	if err != nil {
		return status, nil, err
	}
	if !destinationID(org) || !destinationID(remote) {
		return status, nil, fmt.Errorf("sync: invalid destination IDs")
	}
	path := "/api/organizations/" + org + "/repos/" + remote
	if status.Paired {
		if status.Binding.Endpoint != selected.Server+path {
			return status, nil, fmt.Errorf("sync: destination differs from retained binding")
		}
		selected, err = selectBound(selected.Connection, selected.Server, status)
		if err != nil {
			return status, nil, err
		}
	}
	var generation pairGeneration
	if expected != nil {
		generation = *expected
	} else {
		generation.Repo = status.Generation
		generation.Connection, err = s.ExchangeConnectionGeneration(ctx, selected.Connection)
		if err != nil {
			return status, nil, err
		}
	}
	c, err := onlineConnection(ctx, selected)
	if err != nil {
		return status, nil, err
	}
	api := newAPI(c.Server, c.Token)
	var destination json.RawMessage
	if err := api.request(ctx, "GET", path, nil, &destination); err != nil {
		return status, nil, err
	}
	status, err = s.PairExchangeConnectionAt(ctx, repo, selected.Server+path, selected.Connection, c.Account, generation.Repo, generation.Connection)
	if err == nil {
		err = api.request(ctx, "POST", path+"/origins", map[string]string{"installation_id": status.Binding.InstallationID}, nil)
	}
	if err == nil {
		err = withRegistryLock(func(_ string) error {
			if err := checkResetPending(selected.Connection); err != nil {
				return err
			}
			current, err := s.ExchangeStatus(ctx, repo)
			if err != nil {
				return err
			}
			if !current.Paired || current.Generation != status.Generation {
				return fmt.Errorf("sync: pairing retired during registration; hold retained")
			}
			return s.ReleaseExchangeHold(ctx, repo, status.Generation)
		})
	}
	return status, destination, err
}

func runBound(ctx context.Context, s *store.Store, repo string) (store.ExchangeStatus, error) {
	status, err := s.ExchangeStatus(ctx, repo)
	if err != nil {
		return status, err
	}
	if !status.Active {
		return status, fmt.Errorf("sync: Repo is not actively paired")
	}
	selected, err := selectBound("", "", status)
	if err != nil {
		return status, err
	}
	connectionGeneration, err := s.ExchangeConnectionGeneration(ctx, selected.Connection)
	if err != nil {
		return status, err
	}
	c, err := onlineConnection(ctx, selected)
	if err != nil {
		return status, err
	}
	if _, err := s.PairExchangeConnectionAt(ctx, repo, status.Binding.Endpoint, selected.Connection, c.Account, status.Generation, connectionGeneration); err != nil {
		return status, err
	}
	api := newAPI(c.Server, c.Token)
	return s.RunExchange(ctx, store.ExchangeClient{HTTP: api.http, Token: c.Token}, repo)
}
