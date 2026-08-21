// Package github implements the GitHub Issues tracker provider.
package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tracker"
)

const apiVersion = "2026-03-10"

// Options replaces transport inputs in hermetic tests. Production uses the
// zero value and reads a token from WIP_GITHUB_TOKEN, GH_TOKEN, or GITHUB_TOKEN.
type Options struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

// Factory returns the static registration factory for the GitHub backend.
func Factory(options Options) tracker.Factory {
	return func(input tracker.FactoryInput) (tracker.Seam, error) {
		return New(input.Repo, options)
	}
}

// Adapter maps provider-neutral outbox entries to GitHub Issues REST calls.
type Adapter struct {
	owner   string
	repo    string
	baseURL string
	token   string
	client  *http.Client
}

var _ tracker.StateReader = (*Adapter)(nil)

// New constructs an adapter for the repository identified by repo.RemoteURL.
func New(repo store.Repo, options Options) (*Adapter, error) {
	owner, name, err := repository(repo.RemoteURL)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(options.Token)
	if token == "" {
		for _, key := range []string{"WIP_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
			if token = strings.TrimSpace(os.Getenv(key)); token != "" {
				break
			}
		}
	}
	if token == "" {
		return nil, fmt.Errorf("github tracker: set WIP_GITHUB_TOKEN, GH_TOKEN, or GITHUB_TOKEN")
	}
	baseURL := strings.TrimRight(options.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	client := options.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &Adapter{owner: owner, repo: name, baseURL: baseURL, token: token, client: client}, nil
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
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "github tracker: unsupported outbox kind " + entry.Kind}, nil
	}
}

type createPayload struct {
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Provenance string `json:"provenance"`
}

func (a *Adapter) create(ctx context.Context, entry store.OutboxEntry) (tracker.Result, error) {
	var payload createPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil || strings.TrimSpace(payload.Title) == "" {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "github tracker: malformed create payload"}, nil
	}
	marker := idempotencyMarker(entry.IdempotencyKey)
	ref, found, err := a.findIssue(ctx, marker)
	if err != nil {
		return resultForError(err)
	}
	if found {
		return tracker.Result{Outcome: tracker.Converged, Ref: ref}, nil
	}

	body := strings.TrimSpace(payload.Detail)
	if payload.Provenance != "" {
		if body != "" {
			body += "\n\n"
		}
		body += "Source: " + payload.Provenance
	}
	if body != "" {
		body += "\n\n"
	}
	body += marker
	request := struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{Title: payload.Title, Body: body}
	var response issue
	if err := a.request(ctx, http.MethodPost, a.issuesPath(), request, &response); err != nil {
		return resultForError(err)
	}
	if _, _, _, err := issueReference(response.HTMLURL); err != nil {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "github tracker: create response has no valid issue reference"}, nil
	}
	return tracker.Result{Outcome: tracker.Delivered, Ref: response.HTMLURL}, nil
}

type commentPayload struct {
	Stage  string `json:"stage"`
	Title  string `json:"title"`
	Action string `json:"action"`
}

func (a *Adapter) comment(ctx context.Context, entry store.OutboxEntry) (tracker.Result, error) {
	owner, name, number, err := issueReference(entry.Ref)
	if err != nil {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: err.Error()}, nil
	}
	var payload commentPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil || strings.TrimSpace(payload.Title) == "" {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "github tracker: malformed comment payload"}, nil
	}
	marker := idempotencyMarker(entry.IdempotencyKey)
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", url.PathEscape(owner), url.PathEscape(name), number)
	found, err := a.findComment(ctx, path, marker)
	if err != nil {
		return resultForError(err)
	}
	if found {
		return tracker.Result{Outcome: tracker.Converged}, nil
	}
	text := fmt.Sprintf("Stage %q %s.\n\n%s", payload.Title, payload.Action, marker)
	if err := a.request(ctx, http.MethodPost, path, struct {
		Body string `json:"body"`
	}{text}, nil); err != nil {
		return resultForError(err)
	}
	return tracker.Result{Outcome: tracker.Delivered}, nil
}

type issue struct {
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	State       string `json:"state"`
	StateReason string `json:"state_reason"`
	UpdatedAt   string `json:"updated_at"`
}

type issueStateUpdate struct {
	State       string `json:"state"`
	StateReason string `json:"state_reason,omitempty"`
}

type statePayload struct {
	Disposition store.TrackerDisposition `json:"disposition"`
}

func stateUpdate(disposition store.TrackerDisposition) (issueStateUpdate, error) {
	switch disposition {
	case store.TrackerActive:
		return issueStateUpdate{State: "open"}, nil
	case store.TrackerCompleted:
		return issueStateUpdate{State: "closed", StateReason: "completed"}, nil
	case store.TrackerCanceled:
		return issueStateUpdate{State: "closed", StateReason: "not_planned"}, nil
	default:
		return issueStateUpdate{}, fmt.Errorf("github tracker: unsupported state disposition %q", disposition)
	}
}

func (a *Adapter) readIssue(ctx context.Context, owner, name string, number int) (issue, error) {
	var response issue
	err := a.request(ctx, http.MethodGet, issuePath(owner, name, number), nil, &response)
	return response, err
}

// ReadState implements tracker.StateReader.
func (a *Adapter) ReadState(ctx context.Context, ref string) (tracker.LiveState, error) {
	owner, name, number, err := issueReference(ref)
	if err != nil {
		return tracker.LiveState{}, err
	}
	observed, err := a.readIssue(ctx, owner, name, number)
	if err != nil {
		return tracker.LiveState{}, err
	}
	if strings.TrimSpace(observed.UpdatedAt) == "" || (observed.State != "open" && observed.State != "closed") {
		return tracker.LiveState{}, fmt.Errorf("github tracker: issue read returned malformed state or updated_at")
	}

	live := tracker.LiveState{
		Class:   tracker.LiveNonterminal,
		Display: observed.State,
		Lease:   observed.UpdatedAt,
	}
	if observed.StateReason != "" {
		live.Display += " (" + observed.StateReason + ")"
	}
	if observed.State == "closed" {
		switch observed.StateReason {
		case "completed":
			live.Class = tracker.LiveCompleted
		case "not_planned":
			live.Class = tracker.LiveCanceled
		default:
			live.Class = tracker.LiveTerminal
		}
	}
	return live, nil
}

func (a *Adapter) writeIssueState(ctx context.Context, owner, name string, number int, update issueStateUpdate) (issue, error) {
	var response issue
	err := a.request(ctx, http.MethodPatch, issuePath(owner, name, number), update, &response)
	return response, err
}

func (a *Adapter) state(ctx context.Context, entry store.OutboxEntry) (tracker.Result, error) {
	var payload statePayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "github tracker: malformed state payload"}, nil
	}
	update, err := stateUpdate(payload.Disposition)
	if err != nil {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: err.Error()}, nil
	}
	owner, name, number, err := issueReference(entry.Ref)
	if err != nil {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: err.Error()}, nil
	}

	observed, err := a.readIssue(ctx, owner, name, number)
	if err != nil {
		return resultForError(err)
	}
	if observed.UpdatedAt == "" || (observed.State != "open" && observed.State != "closed") {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "github tracker: issue read returned malformed state or updated_at"}, nil
	}

	if entry.Lease == "" {
		if observed.State != "open" {
			return leaseMismatch(observed), nil
		}
		if payload.Disposition == store.TrackerActive {
			return tracker.Result{Outcome: tracker.Converged, Lease: observed.UpdatedAt}, nil
		}
	} else {
		// Never reopen a terminal issue. A terminal provider state is beyond an
		// active candidate when the candidate carries a prior observation.
		if payload.Disposition == store.TrackerActive && observed.State == "closed" {
			return tracker.Result{Outcome: tracker.Converged, Lease: observed.UpdatedAt}, nil
		}
		if issueAtCandidate(observed, update) {
			return tracker.Result{Outcome: tracker.Converged, Lease: observed.UpdatedAt}, nil
		}
		// A terminal state with a competing reason must not be overwritten,
		// even when its lease still matches the prior observation.
		if observed.State == "closed" || entry.Lease != observed.UpdatedAt {
			return leaseMismatch(observed), nil
		}
	}

	written, err := a.writeIssueState(ctx, owner, name, number, update)
	if err != nil {
		return resultForError(err)
	}
	if written.UpdatedAt == "" {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "github tracker: state response has no updated_at lease"}, nil
	}
	if !issueAtCandidate(written, update) {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "github tracker: state response does not match requested state"}, nil
	}
	return tracker.Result{Outcome: tracker.Delivered, Lease: written.UpdatedAt}, nil
}

func issueAtCandidate(observed issue, candidate issueStateUpdate) bool {
	if candidate.State == "open" {
		return observed.State == "open"
	}
	return observed.State == candidate.State && observed.StateReason == candidate.StateReason
}

func leaseMismatch(observed issue) tracker.Result {
	state := fmt.Sprintf("state %q", observed.State)
	if observed.StateReason != "" {
		state += fmt.Sprintf(" with state_reason %q", observed.StateReason)
	}
	return tracker.Result{
		Outcome: tracker.LeaseMismatch,
		Reason:  fmt.Sprintf("github tracker: lease mismatch: observed %s at updated_at %q", state, observed.UpdatedAt),
	}
}

func (a *Adapter) findIssue(ctx context.Context, marker string) (string, bool, error) {
	for page := 1; ; page++ {
		var issues []issue
		path := fmt.Sprintf("%s?state=all&per_page=100&page=%d", a.issuesPath(), page)
		if err := a.request(ctx, http.MethodGet, path, nil, &issues); err != nil {
			return "", false, err
		}
		for _, candidate := range issues {
			if strings.Contains(candidate.Body, marker) {
				if _, _, _, err := issueReference(candidate.HTMLURL); err != nil {
					return "", false, fmt.Errorf("github tracker: deduplicated issue has no valid reference")
				}
				return candidate.HTMLURL, true, nil
			}
		}
		if len(issues) < 100 {
			return "", false, nil
		}
	}
}

type comment struct {
	Body string `json:"body"`
}

func (a *Adapter) findComment(ctx context.Context, path, marker string) (bool, error) {
	for page := 1; ; page++ {
		var comments []comment
		paged := fmt.Sprintf("%s?per_page=100&page=%d", path, page)
		if err := a.request(ctx, http.MethodGet, paged, nil, &comments); err != nil {
			return false, err
		}
		for _, candidate := range comments {
			if strings.Contains(candidate.Body, marker) {
				return true, nil
			}
		}
		if len(comments) < 100 {
			return false, nil
		}
	}
}

func (a *Adapter) issuesPath() string {
	return "/repos/" + url.PathEscape(a.owner) + "/" + url.PathEscape(a.repo) + "/issues"
}

func issuePath(owner, name string, number int) string {
	return fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(owner), url.PathEscape(name), number)
}

func (a *Adapter) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "wip-tracker")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return &apiError{
			status:      response.StatusCode,
			message:     strings.TrimSpace(string(message)),
			rateLimited: response.StatusCode == http.StatusForbidden && isRateLimited(response.Header, message),
		}
	}
	if output == nil {
		_, err = io.Copy(io.Discard, response.Body)
		return err
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("github tracker: decode response: %w", err)
	}
	return nil
}

type apiError struct {
	status      int
	message     string
	rateLimited bool
}

func (e *apiError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("github tracker: HTTP %d", e.status)
	}
	return fmt.Sprintf("github tracker: HTTP %d: %s", e.status, e.message)
}

func resultForError(err error) (tracker.Result, error) {
	api, ok := err.(*apiError)
	if !ok {
		return tracker.Result{}, err
	}
	outcome := tracker.PermanentRefusal
	if api.status == http.StatusRequestTimeout || api.status == http.StatusTooManyRequests || api.status >= 500 || api.rateLimited {
		outcome = tracker.RetryableFailure
	}
	return tracker.Result{Outcome: outcome, Reason: api.Error()}, nil
}

func isRateLimited(header http.Header, body []byte) bool {
	if strings.TrimSpace(header.Get("Retry-After")) != "" || strings.TrimSpace(header.Get("X-RateLimit-Remaining")) == "0" {
		return true
	}
	var response struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(response.Message), "secondary rate limit")
}

func idempotencyMarker(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "<!-- wip-idempotency:" + hex.EncodeToString(sum[:]) + " -->"
}

func repository(remote string) (string, string, error) {
	remote = strings.TrimSpace(remote)
	if strings.HasPrefix(remote, "git@github.com:") {
		remote = "github.com/" + strings.TrimPrefix(remote, "git@github.com:")
	} else if parsed, err := url.Parse(remote); err == nil && parsed.Host != "" {
		remote = strings.ToLower(parsed.Host) + "/" + strings.TrimPrefix(parsed.Path, "/")
	}
	remote = strings.TrimSuffix(remote, ".git")
	parts := strings.Split(remote, "/")
	if len(parts) != 3 || !strings.EqualFold(parts[0], "github.com") || parts[1] == "" || parts[2] == "" {
		return "", "", fmt.Errorf("github tracker: Repo remote %q is not a GitHub repository", remote)
	}
	return parts[1], parts[2], nil
}

func issueReference(ref string) (string, string, int, error) {
	parsed, err := url.Parse(ref)
	if err != nil || !strings.EqualFold(parsed.Host, "github.com") {
		return "", "", 0, fmt.Errorf("github tracker: reference %q is not a GitHub issue URL", ref)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] != "issues" {
		return "", "", 0, fmt.Errorf("github tracker: reference %q is not a GitHub issue URL", ref)
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number < 1 {
		return "", "", 0, fmt.Errorf("github tracker: reference %q is not a GitHub issue URL", ref)
	}
	return parts[0], parts[1], number, nil
}
