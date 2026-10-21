package wipdremote

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/reporoute"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdjournal"
	"github.com/procrastivity/wip/internal/wipdseed"
	"github.com/procrastivity/wip/internal/wipdwire"
)

type routeMutationLifecycle struct {
	resolver     *wipdseed.LocalRouteResolver
	profileRoot  string
	journal      *wipdjournal.Journal
	runCanonical func(context.Context, operation.Command, func(context.Context, wipd.CommandStartSnapshot, operation.Command) error) (wipd.CommandStartResult, error)
}

func configureRouteMutations(server *wipd.Server, profileRoot string, journals ...*wipdjournal.Journal) error {
	resolver, err := loadPrebindingResolver(profileRoot)
	if err != nil || resolver == nil {
		return err
	}
	var journal *wipdjournal.Journal
	if len(journals) != 0 {
		journal = journals[0]
	}
	lifecycle := &routeMutationLifecycle{resolver: resolver, profileRoot: profileRoot, journal: journal}
	if server.ConnectedCommandsConfigured() {
		lifecycle.runCanonical = server.RunConnectedCanonicalTerminal
	}
	return server.ConfigureRouteMutations(lifecycle)
}

func (handler *routeMutationLifecycle) ExecuteRouteCommand(ctx context.Context, peer *wipd.PeerProcess, command operation.Command) (operation.Result, error) {
	if handler == nil || handler.resolver == nil || ctx == nil || peer == nil ||
		(command.Request.Operation != operation.RepoRouteBindV1.Metadata().Operation && command.Request.Operation != operation.RepoAdoptRemoteV1.Metadata().Operation) {
		return operation.Result{}, errors.New("wipdremote: invalid route mutation request")
	}
	checkout, err := peer.DiscoverGitCheckout(ctx, "origin")
	if err != nil {
		return operation.Result{}, err
	}
	var key string
	switch command.Request.Operation {
	case operation.RepoRouteBindV1.Metadata().Operation:
		input, ok := command.Request.Input.(operation.RepoRouteBindInput)
		if !ok {
			return operation.Result{}, errors.New("wipdremote: invalid route-bind input")
		}
		key = input.NormalizedKey
	case operation.RepoAdoptRemoteV1.Metadata().Operation:
		input, ok := command.Request.Input.(operation.RepoAdoptRemoteInput)
		if !ok {
			return operation.Result{}, errors.New("wipdremote: invalid adoption input")
		}
		key = input.NormalizedKey
	}
	if key == "" || key != checkout.NormalizedRemote {
		return operation.Result{Code: operation.ResultRefused, Problem: &operation.Problem{
			Code: operation.ProblemRouteIdentityFenced, Message: "route mutation key differs from the authenticated checkout",
		}}, nil
	}
	selected, err := reporoute.Select(handler.resolver.Entries, checkout.NormalizedRemote)
	if err != nil || selected.DomainID != command.AuthorityDomainID {
		return operation.Result{Code: operation.ResultRefused, Problem: &operation.Problem{
			Code: operation.ProblemRouteIdentityFenced, Message: "resolved route identity differs from the command",
		}}, nil
	}
	connection, ok := handler.resolver.Connections[selected.ConnectionProfile]
	if !ok {
		return operation.Result{}, errors.New("wipdremote: resolved route connection is unavailable")
	}
	route, routeErr := handler.resolver.ResolveRoute(ctx, peer)
	if routeErr != nil {
		if !errors.Is(routeErr, authoritystore.ErrRouteUnbound) {
			return operation.Result{Code: operation.ResultRefused, Problem: &operation.Problem{
				Code: operation.ProblemRouteIdentityFenced, Message: "authority route cannot be verified for mutation",
			}}, nil
		}
		route = operation.RouteResolveOutput{DomainID: selected.DomainID, Epoch: connection.AuthorityProfile.Epoch(), ConnectionProfile: selected.ConnectionProfile}
	} else if route.DomainID != command.AuthorityDomainID || route.Epoch != command.ExpectedAuthorityEpoch ||
		route.RepoID != nil && *route.RepoID != command.Request.Context.Repo {
		return operation.Result{Code: operation.ResultRefused, Problem: &operation.Problem{
			Code: operation.ProblemRouteIdentityFenced, Message: "resolved route identity differs from the command",
		}}, nil
	}
	if route.Epoch != command.ExpectedAuthorityEpoch {
		return operation.Result{Code: operation.ResultRefused, Problem: &operation.Problem{
			Code: operation.ProblemRouteIdentityFenced, Message: "selected authority epoch differs from the command",
		}}, nil
	}
	credential, certificate, err := wipdseed.LoadEnvironmentCredential(connection.ProfileRoot, connection.AuthorityProfile,
		connection.OwnerRoot, connection.CADElegation, time.Now().UTC())
	if err != nil {
		return operation.Result{}, err
	}
	defer clear(credential.PrivateKeyPKCS8)
	defer clear(certificate.PrivateKey.(ed25519.PrivateKey))
	if command.EnvironmentID != credential.EnvironmentID || command.Request.Context.Clone == "" || command.Request.Context.Worktree == "" {
		return operation.Result{Code: operation.ResultRefused, Problem: &operation.Problem{
			Code: operation.ProblemRouteIdentityFenced, Message: "route mutation is not bound to the active attached checkout",
		}}, nil
	}
	if err = handler.validateRegisteredCheckout(ctx, checkout, command.Request.Context); err != nil {
		return operation.Result{Code: operation.ResultRefused, Problem: &operation.Problem{
			Code: operation.ProblemRouteIdentityFenced, Message: "route mutation IDs do not identify the authenticated local checkout",
		}}, nil
	}
	stateDir := filepath.Join(handler.profileRoot, "environment-attach-state", command.AuthorityDomainID, command.Request.Context.Repo)
	definitions := operation.Step11Catalogue()
	operations := make([]operation.ID, 0, len(definitions))
	for _, definition := range definitions {
		operations = append(operations, definition.Metadata().Operation)
	}
	sort.Slice(operations, func(i, j int) bool {
		return operations[i].Name < operations[j].Name || operations[i].Name == operations[j].Name && operations[i].Version < operations[j].Version
	})
	client, err := wipdseed.OpenStep11CommandExchangeClientForRepoWithCredential(ctx, connection.AuthorityProfile,
		connection.AuthorityRoots, stateDir, connection.ProfileRoot, command.Request.Context.Repo, operations)
	if err != nil {
		return operation.Result{}, fmt.Errorf("open route mutation authority exchange: %w", err)
	}
	defer func() { _ = client.Close() }()
	journal := handler.journal
	ownedJournal := false
	if journal == nil {
		journal, err = openStep11Journal(handler.profileRoot, connection.AuthorityProfile.M5LabRepoID(), command.AuthorityDomainID,
			command.ExpectedAuthorityEpoch, command.EnvironmentID, credential.OwnerKeyID)
		if err != nil {
			return operation.Result{}, err
		}
		ownedJournal = true
	}
	if ownedJournal {
		defer func() { _ = journal.Close() }()
	}
	if handler.runCanonical == nil {
		if _, err = reconcilePrebindingJournal(ctx, connection.AuthorityProfile, client, journal); err != nil {
			return operation.Result{}, fmt.Errorf("reconcile prebinding route journal: %w", err)
		}
	}
	var semantic operation.Result
	var terminalReceipt []byte
	var prepared wipdjournal.Entry
	submit := func(submitContext context.Context, _ wipd.CommandStartSnapshot, submitted operation.Command) error {
		entry, prepareErr := journal.PrepareCanonicalSubmission(submitted, "wipd.command-submit/1", nil)
		if prepareErr != nil {
			return prepareErr
		}
		prepared = entry
		submission, encodeErr := entry.SubmissionPayload()
		if encodeErr != nil {
			return encodeErr
		}
		frames, exchangeErr := client.Exchange(submitContext, "command.submit", submission)
		if exchangeErr != nil {
			return fmt.Errorf("%w: route mutation response is uncertain: %v", wipd.ErrOutcomeUnknown, exchangeErr)
		}
		receipt, terminalErr := terminalFromRouteSubmit(submitContext, client, command.AuthorityDomainID, entry, frames)
		if terminalErr != nil {
			return terminalErr
		}
		terminalReceipt = receipt
		var decodeErr error
		semantic, decodeErr = decodeRouteMutationResult(submitted, receipt)
		return decodeErr
	}
	if handler.runCanonical != nil {
		connected, runErr := handler.runCanonical(ctx, command, submit)
		if runErr != nil {
			return operation.Result{}, runErr
		}
		if connected.Returned {
			return connected.SemanticResult, nil
		}
		return operation.Result{}, errors.New("wipdremote: connected route mutation did not install a terminal result")
	}
	if _, err = journal.PrepareCanonicalSubmission(command, "wipd.command-submit/1", nil); err != nil {
		return operation.Result{}, err
	}
	if err = submit(ctx, wipd.CommandStartSnapshot{}, command); err != nil {
		return operation.Result{}, err
	}
	if err = installPrebindingAuthorityOutcome(ctx, connection.AuthorityProfile, client, journal, prepared,
		semantic.Code, terminalReceipt); err != nil {
		return operation.Result{}, fmt.Errorf("install prebinding route terminal in shared journal: %w", err)
	}
	return semantic, nil
}

func terminalFromRouteSubmit(ctx context.Context, client attachCommandExchanger, domain string, entry wipdjournal.Entry,
	frames []wipdwire.Frame,
) ([]byte, error) {
	terminal := func(frame wipdwire.Frame) ([]byte, error) {
		if frame.Kind != "command.terminal" || validateTerminalIdentity(frame.Payload, domain, entry) != nil {
			return nil, errors.New("wipdremote: route mutation terminal identity is invalid")
		}
		return frame.Payload, nil
	}
	accepted := func(frame wipdwire.Frame) error {
		if frame.Kind != "submission.accepted" {
			return errors.New("wipdremote: route mutation submission was not acknowledged")
		}
		return validateAccepted(frame.Payload, domain, entry)
	}
	if len(frames) == 1 && frames[0].Kind == "command.terminal" {
		return terminal(frames[0])
	}
	if len(frames) == 2 && accepted(frames[0]) == nil {
		return terminal(frames[1])
	}
	if len(frames) != 1 || accepted(frames[0]) != nil {
		return nil, errors.New("wipdremote: invalid route mutation submission response")
	}
	query := wipdwire.ReceiptQuery{
		Schema: "wipd.receipt-query/1", DomainID: domain,
		CommandID: entry.Command.ID, RequestHash: entry.RequestHash,
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		frames, err := client.Exchange(ctx, "receipt.query", query)
		if err != nil {
			return nil, fmt.Errorf("%w: route mutation remains recoverable: %v", wipd.ErrOutcomeUnknown, err)
		}
		if len(frames) != 1 {
			return nil, errors.New("wipdremote: route mutation receipt query has invalid frame count")
		}
		switch frames[0].Kind {
		case "command.terminal":
			return terminal(frames[0])
		case "receipt.pending":
			if err = validateAccepted(frames[0].Payload, domain, entry); err != nil {
				return nil, err
			}
		case "receipt.not-found":
			var missing wipdwire.ReceiptNotFound
			if wipdwire.DecodeCanonical(frames[0].Payload, &missing, "schema", "domain_id", "command_id", "request_hash") != nil ||
				missing.Schema != "wipd.receipt-not-found/1" || missing.DomainID != domain ||
				missing.CommandID != entry.Command.ID || missing.RequestHash != entry.RequestHash {
				return nil, errors.New("wipdremote: route mutation absence does not match retained command")
			}
			submission, submitErr := entry.SubmissionPayload()
			if submitErr != nil {
				return nil, submitErr
			}
			retryFrames, retryErr := client.Exchange(ctx, "command.submit", submission)
			if retryErr != nil {
				return nil, fmt.Errorf("%w: exact route mutation retry is uncertain: %v", wipd.ErrOutcomeUnknown, retryErr)
			}
			if len(retryFrames) == 1 && retryFrames[0].Kind == "command.terminal" {
				return terminal(retryFrames[0])
			}
			if len(retryFrames) == 2 && accepted(retryFrames[0]) == nil {
				return terminal(retryFrames[1])
			}
			if len(retryFrames) != 1 || accepted(retryFrames[0]) != nil {
				return nil, errors.New("wipdremote: exact route mutation retry was not acknowledged")
			}
		default:
			return nil, errors.New("wipdremote: unexpected route mutation receipt state")
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: route mutation remains recoverable: %v", wipd.ErrOutcomeUnknown, ctx.Err())
		case <-deadline.C:
			return nil, fmt.Errorf("%w: route mutation remains pending", wipd.ErrOutcomeUnknown)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (handler *routeMutationLifecycle) validateRegisteredCheckout(ctx context.Context, checkout wipd.GitCheckout, identity operation.Context) error {
	local, err := store.Open(filepath.Join(handler.profileRoot, "local-registry.db"))
	if err != nil {
		return err
	}
	defer func() { _ = local.Close() }()
	clone, found, err := local.CloneByCommonDir(ctx, checkout.CommonDir)
	if err != nil || !found || clone.ID != identity.Clone || clone.Repo != identity.Repo {
		return errors.New("wipdremote: route mutation Clone is not registered for the live checkout")
	}
	worktree, found, err := local.WorktreeByName(ctx, clone.ID, checkout.WorktreeName)
	if err != nil || !found || worktree.ID != identity.Worktree || worktree.Clone != clone.ID {
		return errors.New("wipdremote: route mutation Worktree is not registered for the live checkout")
	}
	return nil
}

func decodeRouteMutationResult(command operation.Command, receipt []byte) (operation.Result, error) {
	fields, err := wipdwire.DecodeCanonicalMap(receipt,
		"schema", "domain_id", "authority_epoch", "identity_schema", "command_id", "request_hash", "operation", "environment", "result", "accepted_events")
	if err != nil {
		return operation.Result{}, err
	}
	resultFields, ok := fields["result"].(map[string]any)
	if !ok {
		return operation.Result{}, errors.New("wipdremote: malformed route mutation result")
	}
	code, ok := resultFields["code"].(string)
	if !ok {
		return operation.Result{}, errors.New("wipdremote: malformed route mutation result code")
	}
	result := operation.Result{Code: operation.ResultCode(code)}
	if result.Code != operation.ResultSucceeded {
		problem, _ := resultFields["problem_code"].(string)
		if problem == "" {
			problem = string(operation.ProblemExecutionFailed)
		}
		result.Problem = &operation.Problem{Code: operation.ProblemCode(problem), Message: "authority refused the route mutation"}
		return result, nil
	}
	outputBytes, ok := resultFields["output"].([]byte)
	if !ok {
		return operation.Result{}, errors.New("wipdremote: route mutation output is missing")
	}
	output, err := wipdwire.DecodeCanonicalMap(outputBytes, "repo_id", "domain_id", "epoch")
	if err != nil || output["repo_id"] != command.Request.Context.Repo || output["domain_id"] != command.AuthorityDomainID {
		return operation.Result{}, errors.New("wipdremote: route mutation output conflicts with command identity")
	}
	epoch, ok := output["epoch"].(uint64)
	if !ok || epoch != command.ExpectedAuthorityEpoch {
		return operation.Result{}, errors.New("wipdremote: route mutation output epoch conflicts with command")
	}
	switch command.Request.Operation {
	case operation.RepoRouteBindV1.Metadata().Operation:
		result.Output = operation.RepoRouteBindOutput{RepoID: command.Request.Context.Repo, DomainID: command.AuthorityDomainID, Epoch: epoch}
	case operation.RepoAdoptRemoteV1.Metadata().Operation:
		result.Output = operation.RepoAdoptRemoteOutput{RepoID: command.Request.Context.Repo, DomainID: command.AuthorityDomainID, Epoch: epoch}
	}
	return result, nil
}
