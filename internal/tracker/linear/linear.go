// Package linear implements the Linear tracker provider.
package linear

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tracker"
)

const defaultBaseURL = "https://api.linear.app/graphql"

var (
	uuidPattern       = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	identifierPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[1-9][0-9]*$`)
)

// Options replaces transport inputs in hermetic tests. Production uses the
// zero value and reads WIP_LINEAR_TOKEN, then LINEAR_API_KEY.
type Options struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

// Factory returns the static registration factory for the Linear backend.
func Factory(options Options) tracker.Factory {
	return func(input tracker.FactoryInput) (tracker.Seam, error) {
		return New(input, options)
	}
}

// Adapter maps provider-neutral outbox entries to Linear GraphQL operations.
type Adapter struct {
	target  string
	baseURL string
	token   string
	client  *http.Client
}

var _ tracker.StateReader = (*Adapter)(nil)

// New constructs an adapter for one configured Linear team UUID.
func New(input tracker.FactoryInput, options Options) (*Adapter, error) {
	target := strings.TrimSpace(input.Target)
	if !validUUID(target) {
		return nil, fmt.Errorf("linear tracker: target %q is not a team UUID", input.Target)
	}
	token := strings.TrimSpace(options.Token)
	if token == "" {
		for _, key := range []string{"WIP_LINEAR_TOKEN", "LINEAR_API_KEY"} {
			if token = strings.TrimSpace(os.Getenv(key)); token != "" {
				break
			}
		}
	}
	if token == "" {
		return nil, fmt.Errorf("linear tracker: set WIP_LINEAR_TOKEN or LINEAR_API_KEY")
	}
	baseURL := strings.TrimSpace(options.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	client := options.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &Adapter{target: strings.ToLower(target), baseURL: baseURL, token: token, client: client}, nil
}

// Deliver implements tracker.Seam.
func (a *Adapter) Deliver(ctx context.Context, entry store.OutboxEntry) (tracker.Result, error) {
	switch entry.Kind {
	case "create":
		return a.create(ctx, entry)
	case "comment":
		return a.comment(ctx, entry)
	case "state":
		return a.state(ctx, entry)
	default:
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "linear tracker: unsupported outbox kind " + entry.Kind}, nil
	}
}

type createPayload struct {
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Provenance string `json:"provenance"`
}

func (a *Adapter) create(ctx context.Context, entry store.OutboxEntry) (tracker.Result, error) {
	var payload createPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil || strings.TrimSpace(payload.Title) == "" || strings.TrimSpace(entry.IdempotencyKey) == "" {
		return refusal("malformed create payload"), nil
	}
	clientID := deterministicUUID("create", entry.IdempotencyKey)
	if ref, found, err := a.findIssue(ctx, clientID); err != nil {
		return resultForError(err)
	} else if found {
		return tracker.Result{Outcome: tracker.Converged, Ref: ref}, nil
	}
	started, err := a.selectWorkflowState(ctx, "started")
	if err != nil {
		return resultForError(err)
	}

	description := strings.TrimSpace(payload.Detail)
	if payload.Provenance != "" {
		if description != "" {
			description += "\n\n"
		}
		description += "Source: " + payload.Provenance
	}
	variables := map[string]any{"input": map[string]any{
		"id": clientID, "teamId": a.target, "stateId": started.ID,
		"title": payload.Title, "description": description,
	}}
	var data struct {
		IssueCreate struct {
			Success bool  `json:"success"`
			Issue   issue `json:"issue"`
		} `json:"issueCreate"`
	}
	err = a.request(ctx, "CreateIssue", `mutation CreateIssue($input: IssueCreateInput!) {
  issueCreate(input: $input) { success issue { identifier } }
}`, variables, &data)
	if err == nil && data.IssueCreate.Success && validIdentifier(data.IssueCreate.Issue.Identifier) {
		return tracker.Result{Outcome: tracker.Delivered, Ref: data.IssueCreate.Issue.Identifier}, nil
	}
	if err == nil {
		err = permanentError("create response has no successful issue identifier")
	}
	if ref, found, reconcileErr := a.findIssue(ctx, clientID); reconcileErr != nil {
		return resultForError(reconcileErr)
	} else if found {
		return tracker.Result{Outcome: tracker.Converged, Ref: ref}, nil
	}
	return resultForError(err)
}

type commentPayload struct {
	Stage  string `json:"stage"`
	Title  string `json:"title"`
	Action string `json:"action"`
}

func (a *Adapter) comment(ctx context.Context, entry store.OutboxEntry) (tracker.Result, error) {
	if !validIdentifier(entry.Ref) {
		return refusal(fmt.Sprintf("reference %q is not a Linear issue identifier", entry.Ref)), nil
	}
	var payload commentPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil || strings.TrimSpace(payload.Title) == "" || strings.TrimSpace(entry.IdempotencyKey) == "" {
		return refusal("malformed comment payload"), nil
	}
	clientID := deterministicUUID("comment", entry.IdempotencyKey)
	if found, err := a.findComment(ctx, clientID); err != nil {
		return resultForError(err)
	} else if found {
		return tracker.Result{Outcome: tracker.Converged}, nil
	}
	if _, err := a.readIssue(ctx, entry.Ref); err != nil {
		return resultForError(err)
	}
	variables := map[string]any{"input": map[string]any{
		"id": clientID, "issueId": entry.Ref,
		"body": fmt.Sprintf("Stage %q %s.", payload.Title, payload.Action),
	}}
	var data struct {
		CommentCreate struct {
			Success bool `json:"success"`
			Comment struct {
				ID string `json:"id"`
			} `json:"comment"`
		} `json:"commentCreate"`
	}
	err := a.request(ctx, "CreateComment", `mutation CreateComment($input: CommentCreateInput!) {
  commentCreate(input: $input) { success comment { id } }
}`, variables, &data)
	if err == nil && data.CommentCreate.Success && strings.EqualFold(data.CommentCreate.Comment.ID, clientID) {
		return tracker.Result{Outcome: tracker.Delivered}, nil
	}
	if err == nil {
		err = permanentError("comment response has no successful comment identity")
	}
	if found, reconcileErr := a.findComment(ctx, clientID); reconcileErr != nil {
		return resultForError(reconcileErr)
	} else if found {
		return tracker.Result{Outcome: tracker.Converged}, nil
	}
	return resultForError(err)
}

type workflowState struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Position   float64 `json:"position"`
	ArchivedAt string  `json:"archivedAt"`
}

type issue struct {
	ID         string        `json:"id"`
	Identifier string        `json:"identifier"`
	UpdatedAt  string        `json:"updatedAt"`
	State      workflowState `json:"state"`
	Team       struct {
		ID string `json:"id"`
	} `json:"team"`
}

func (a *Adapter) findIssue(ctx context.Context, id string) (string, bool, error) {
	var data struct {
		Issues struct {
			Nodes []issue `json:"nodes"`
		} `json:"issues"`
	}
	err := a.request(ctx, "FindIssueByClientID", `query FindIssueByClientID($id: ID!) {
  issues(filter: {id: {eq: $id}}, includeArchived: true, first: 1) { nodes { id identifier team { id } } }
}`, map[string]any{"id": id}, &data)
	if err != nil {
		return "", false, err
	}
	if len(data.Issues.Nodes) == 0 {
		return "", false, nil
	}
	item := data.Issues.Nodes[0]
	ref := item.Identifier
	if len(data.Issues.Nodes) != 1 || !strings.EqualFold(item.ID, id) || !validIdentifier(ref) {
		return "", false, permanentError("deduplicated issue has no unique valid identifier")
	}
	if !strings.EqualFold(item.Team.ID, a.target) {
		return "", false, permanentError(fmt.Sprintf("deduplicated issue %q does not belong to target team", ref))
	}
	return ref, true, nil
}

func (a *Adapter) findComment(ctx context.Context, id string) (bool, error) {
	var data struct {
		Comments struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"comments"`
	}
	err := a.request(ctx, "FindCommentByClientID", `query FindCommentByClientID($id: ID!) {
  comments(filter: {id: {eq: $id}}, includeArchived: true, first: 1) { nodes { id } }
}`, map[string]any{"id": id}, &data)
	if err != nil {
		return false, err
	}
	if len(data.Comments.Nodes) == 0 {
		return false, nil
	}
	if len(data.Comments.Nodes) != 1 || !strings.EqualFold(data.Comments.Nodes[0].ID, id) {
		return false, permanentError("deduplicated comment has no unique matching identity")
	}
	return true, nil
}

type statePayload struct {
	Disposition store.TrackerDisposition `json:"disposition"`
}

func workflowType(disposition store.TrackerDisposition) (string, error) {
	switch disposition {
	case store.TrackerActive:
		return "started", nil
	case store.TrackerCompleted:
		return "completed", nil
	case store.TrackerCanceled:
		return "canceled", nil
	default:
		return "", fmt.Errorf("unsupported state disposition %q", disposition)
	}
}

func (a *Adapter) selectWorkflowState(ctx context.Context, stateType string) (workflowState, error) {
	var candidates []workflowState
	cursor := ""
	for {
		var data struct {
			Team *struct {
				States struct {
					Nodes    []workflowState `json:"nodes"`
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"states"`
			} `json:"team"`
		}
		variables := map[string]any{"teamId": a.target}
		if cursor != "" {
			variables["after"] = cursor
		}
		err := a.request(ctx, "WorkflowStates", `query WorkflowStates($teamId: String!, $after: String) {
  team(id: $teamId) { states(first: 250, after: $after) { nodes { id name type position archivedAt } pageInfo { hasNextPage endCursor } } }
}`, variables, &data)
		if err != nil {
			return workflowState{}, err
		}
		if data.Team == nil {
			return workflowState{}, permanentError("target team was not found")
		}
		for _, state := range data.Team.States.Nodes {
			if state.Type == stateType && state.ArchivedAt == "" && validUUID(state.ID) {
				candidates = append(candidates, state)
			}
		}
		page := data.Team.States.PageInfo
		if !page.HasNextPage || strings.TrimSpace(page.EndCursor) == "" {
			break
		}
		cursor = page.EndCursor
	}
	if len(candidates) == 0 {
		return workflowState{}, permanentError(fmt.Sprintf("target team has no live %s workflow state", stateType))
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Position != candidates[j].Position {
			return candidates[i].Position < candidates[j].Position
		}
		return strings.ToLower(candidates[i].ID) < strings.ToLower(candidates[j].ID)
	})
	return candidates[0], nil
}

func (a *Adapter) readIssue(ctx context.Context, ref string) (issue, error) {
	var data struct {
		Issue *issue `json:"issue"`
	}
	err := a.request(ctx, "ReadIssue", `query ReadIssue($ref: String!) {
  issue(id: $ref) { id identifier updatedAt team { id } state { id name type position } }
}`, map[string]any{"ref": ref}, &data)
	if err != nil {
		return issue{}, err
	}
	if data.Issue == nil {
		return issue{}, permanentError(fmt.Sprintf("issue %q was not found", ref))
	}
	if err := validateIssue(*data.Issue); err != nil {
		return issue{}, err
	}
	if !strings.EqualFold(data.Issue.Identifier, ref) {
		return issue{}, permanentError(fmt.Sprintf("issue read returned identifier %q for reference %q", data.Issue.Identifier, ref))
	}
	if !strings.EqualFold(data.Issue.Team.ID, a.target) {
		return issue{}, permanentError(fmt.Sprintf("issue %q does not belong to target team", ref))
	}
	return *data.Issue, nil
}

func validateIssue(observed issue) error {
	if !validIdentifier(observed.Identifier) || strings.TrimSpace(observed.UpdatedAt) == "" || !validUUID(observed.Team.ID) || !validUUID(observed.State.ID) || strings.TrimSpace(observed.State.Name) == "" || strings.TrimSpace(observed.State.Type) == "" {
		return permanentError("issue read returned malformed identifier, team, state, or updatedAt")
	}
	return nil
}

// ReadState implements tracker.StateReader.
func (a *Adapter) ReadState(ctx context.Context, ref string) (tracker.LiveState, error) {
	if !validIdentifier(ref) {
		return tracker.LiveState{}, fmt.Errorf("linear tracker: reference %q is not a Linear issue identifier", ref)
	}
	observed, err := a.readIssue(ctx, ref)
	if err != nil {
		return tracker.LiveState{}, err
	}
	class := tracker.LiveTerminal
	switch observed.State.Type {
	case "triage", "backlog", "unstarted":
		class = tracker.LiveBacklog
	case "started":
		class = tracker.LiveActive
	case "completed":
		class = tracker.LiveCompleted
	case "canceled":
		class = tracker.LiveCanceled
	case "duplicate":
		class = tracker.LiveTerminal
	}
	return tracker.LiveState{Class: class, Display: observed.State.Name, Lease: observed.UpdatedAt}, nil
}

func (a *Adapter) state(ctx context.Context, entry store.OutboxEntry) (tracker.Result, error) {
	if !validIdentifier(entry.Ref) {
		return refusal(fmt.Sprintf("reference %q is not a Linear issue identifier", entry.Ref)), nil
	}
	var payload statePayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return refusal("malformed state payload"), nil
	}
	desiredType, err := workflowType(payload.Disposition)
	if err != nil {
		return refusal(err.Error()), nil
	}
	observed, err := a.readIssue(ctx, entry.Ref)
	if err != nil {
		return resultForError(err)
	}
	if observed.State.Type == desiredType {
		return tracker.Result{Outcome: tracker.Converged, Lease: observed.UpdatedAt}, nil
	}
	if entry.Lease == "" {
		behindActive := desiredType == "started" && isBacklogType(observed.State.Type)
		activeToTerminal := (desiredType == "completed" || desiredType == "canceled") && observed.State.Type == "started"
		if !behindActive && !activeToTerminal {
			return leaseMismatch(observed), nil
		}
	} else {
		if desiredType == "started" && observed.State.Type == "completed" {
			return tracker.Result{Outcome: tracker.Converged, Lease: observed.UpdatedAt}, nil
		}
		if isTerminalType(observed.State.Type) || entry.Lease != observed.UpdatedAt {
			return leaseMismatch(observed), nil
		}
	}
	target, err := a.selectWorkflowState(ctx, desiredType)
	if err != nil {
		return resultForError(err)
	}

	var data struct {
		IssueUpdate struct {
			Success bool  `json:"success"`
			Issue   issue `json:"issue"`
		} `json:"issueUpdate"`
	}
	err = a.request(ctx, "UpdateIssueState", `mutation UpdateIssueState($ref: String!, $input: IssueUpdateInput!) {
  issueUpdate(id: $ref, input: $input) { success issue { id identifier updatedAt team { id } state { id name type position } } }
}`, map[string]any{"ref": entry.Ref, "input": map[string]any{"stateId": target.ID}}, &data)
	if err != nil {
		return resultForError(err)
	}
	written := data.IssueUpdate.Issue
	if !data.IssueUpdate.Success || validateIssue(written) != nil || !strings.EqualFold(written.Identifier, entry.Ref) || !strings.EqualFold(written.Team.ID, a.target) || written.State.Type != desiredType {
		return refusal("state response does not match requested workflow type or has no lease"), nil
	}
	return tracker.Result{Outcome: tracker.Delivered, Lease: written.UpdatedAt}, nil
}

func isBacklogType(stateType string) bool {
	return stateType == "triage" || stateType == "backlog" || stateType == "unstarted"
}

// L9: every type that is neither backlog-like nor started competes as
// terminal, including workflow types Linear has not defined yet.
func isTerminalType(stateType string) bool {
	return !isBacklogType(stateType) && stateType != "started"
}

func leaseMismatch(observed issue) tracker.Result {
	return tracker.Result{
		Outcome: tracker.LeaseMismatch,
		Reason: fmt.Sprintf("linear tracker: lease mismatch: observed workflow state %q of type %q at updatedAt %q",
			observed.State.Name, observed.State.Type, observed.UpdatedAt),
	}
}

type graphQLRequest struct {
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables,omitempty"`
	OperationName string         `json:"operationName"`
}

type graphQLError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

func (a *Adapter) request(ctx context.Context, operation, query string, variables map[string]any, output any) error {
	body, err := json.Marshal(graphQLRequest{Query: query, Variables: variables, OperationName: operation})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", a.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "wip-tracker")
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	encoded, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if readErr != nil {
		return readErr
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphQLError  `json:"errors"`
	}
	decodeErr := json.Unmarshal(encoded, &envelope)
	if len(envelope.Errors) > 0 {
		return &apiError{status: response.StatusCode, errors: envelope.Errors, rateLimited: hasRateLimitHeaders(response.Header)}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &apiError{status: response.StatusCode, message: strings.TrimSpace(string(encoded)), rateLimited: hasRateLimitHeaders(response.Header)}
	}
	if decodeErr != nil {
		return fmt.Errorf("linear tracker: decode response: %w", decodeErr)
	}
	if output == nil {
		return nil
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return permanentError("response has no data")
	}
	if err := json.Unmarshal(envelope.Data, output); err != nil {
		return fmt.Errorf("linear tracker: decode response data: %w", err)
	}
	return nil
}

type apiError struct {
	status      int
	errors      []graphQLError
	message     string
	rateLimited bool
}

func (e *apiError) Error() string {
	var details []string
	for _, item := range e.errors {
		code := strings.TrimSpace(item.Extensions.Code)
		if code == "" {
			details = append(details, item.Message)
		} else {
			details = append(details, code+": "+item.Message)
		}
	}
	message := strings.TrimSpace(strings.Join(details, "; "))
	if message == "" {
		message = e.message
	}
	if message == "" {
		return fmt.Sprintf("linear tracker: HTTP %d", e.status)
	}
	return fmt.Sprintf("linear tracker: HTTP %d: %s", e.status, message)
}

type classifiedError struct {
	outcome tracker.Outcome
	text    string
}

func (e *classifiedError) Error() string { return "linear tracker: " + e.text }

func permanentError(text string) error {
	return &classifiedError{outcome: tracker.PermanentRefusal, text: text}
}

func resultForError(err error) (tracker.Result, error) {
	var classified *classifiedError
	if errors.As(err, &classified) {
		return tracker.Result{Outcome: classified.outcome, Reason: classified.Error()}, nil
	}
	var api *apiError
	if !errors.As(err, &api) {
		return tracker.Result{}, err
	}
	outcome := tracker.PermanentRefusal
	permanentCode := false
	for _, item := range api.errors {
		if strings.EqualFold(strings.TrimSpace(item.Extensions.Code), "RATELIMITED") {
			outcome = tracker.RetryableFailure
		} else {
			permanentCode = true
		}
	}
	// Exhausted rate-limit headers must not soften an explicit permanent
	// GraphQL code (L6); they only decide otherwise-unclassified responses.
	if api.status == http.StatusRequestTimeout || api.status == http.StatusTooManyRequests || api.status >= 500 || (api.rateLimited && !permanentCode) {
		outcome = tracker.RetryableFailure
	}
	return tracker.Result{Outcome: outcome, Reason: api.Error()}, nil
}

func hasRateLimitHeaders(header http.Header) bool {
	if strings.TrimSpace(header.Get("Retry-After")) != "" {
		return true
	}
	for _, name := range []string{"X-RateLimit-Remaining", "X-RateLimit-Requests-Remaining"} {
		if strings.TrimSpace(header.Get(name)) == "0" {
			return true
		}
	}
	return false
}

func refusal(reason string) tracker.Result {
	return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "linear tracker: " + reason}
}

func deterministicUUID(kind, key string) string {
	sum := sha256.Sum256([]byte("wip:linear:" + kind + ":" + key))
	bytes := sum[:16]
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	hexadecimal := hex.EncodeToString(bytes)
	return hexadecimal[0:8] + "-" + hexadecimal[8:12] + "-" + hexadecimal[12:16] + "-" + hexadecimal[16:20] + "-" + hexadecimal[20:32]
}

func validUUID(value string) bool {
	return uuidPattern.MatchString(strings.TrimSpace(value))
}

func validIdentifier(value string) bool {
	return identifierPattern.MatchString(strings.TrimSpace(value))
}
