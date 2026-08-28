package gitlab

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

// Options replaces transport inputs in hermetic tests. Production uses the
// zero value and reads WIP_GITLAB_TOKEN, GITLAB_TOKEN, then the glab config.
type Options struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

// Factory returns the static registration factory for the GitLab backend.
func Factory(options Options) tracker.Factory {
	return func(input tracker.FactoryInput) (tracker.Seam, error) {
		return New(input, options)
	}
}

// Adapter maps provider-neutral outbox entries to GitLab Issues REST calls.
type Adapter struct {
	host          string // lower-case, no port; from the remote (G1)
	project       string // unescaped full path, e.g. "group/sub/project"
	projectID     string // url.PathEscape(project), e.g. "group%2Fsub%2Fproject"
	baseURL       string // Options.BaseURL or "https://<host>/api/v4", no trailing slash
	token         string
	canceledLabel string // FactoryInput.CanceledLabel, trimmed; "" = none (G7)
	client        *http.Client
}

var _ tracker.StateReader = (*Adapter)(nil)

// New constructs an adapter for the repository identified by
// input.Repo.RemoteURL. input.Target is ignored (G2).
func New(input tracker.FactoryInput, options Options) (*Adapter, error) {
	host, path, err := project(input.Repo.RemoteURL)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(options.Token)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("WIP_GITLAB_TOKEN"))
	}
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GITLAB_TOKEN"))
	}
	if token == "" {
		token, err = glabToken(host)
		if err != nil {
			return nil, fmt.Errorf("gitlab tracker: %w", err)
		}
	}
	if token == "" {
		return nil, fmt.Errorf("gitlab tracker: set WIP_GITLAB_TOKEN or GITLAB_TOKEN, or log in with glab for %s", host)
	}
	baseURL := strings.TrimRight(options.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://" + host + "/api/v4"
	}
	client := options.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &Adapter{
		host:          host,
		project:       path,
		projectID:     url.PathEscape(path),
		baseURL:       baseURL,
		token:         token,
		canceledLabel: strings.TrimSpace(input.CanceledLabel),
		client:        client,
	}, nil
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
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "gitlab tracker: unsupported outbox kind " + entry.Kind}, nil
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
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "gitlab tracker: malformed create payload"}, nil
	}
	marker := idempotencyMarker(entry.IdempotencyKey)
	ref, found, err := a.findIssue(ctx, marker)
	if err != nil {
		return resultForError(err)
	}
	if found {
		return tracker.Result{Outcome: tracker.Converged, Ref: ref}, nil
	}

	description := strings.TrimSpace(payload.Detail)
	if payload.Provenance != "" {
		if description != "" {
			description += "\n\n"
		}
		description += "Source: " + payload.Provenance
	}
	if description != "" {
		description += "\n\n"
	}
	description += marker
	request := struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}{Title: payload.Title, Description: description}
	var response issue
	if err := a.request(ctx, http.MethodPost, a.issuesPath(), nil, request, &response); err != nil {
		return resultForError(err)
	}
	if _, err := a.issueReference(response.WebURL); err != nil {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "gitlab tracker: create response has no valid issue reference"}, nil
	}
	return tracker.Result{Outcome: tracker.Delivered, Ref: response.WebURL}, nil
}

type commentPayload struct {
	Stage  string `json:"stage"`
	Title  string `json:"title"`
	Action string `json:"action"`
}

func (a *Adapter) comment(ctx context.Context, entry store.OutboxEntry) (tracker.Result, error) {
	iid, err := a.issueReference(entry.Ref)
	if err != nil {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: err.Error()}, nil
	}
	var payload commentPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil || strings.TrimSpace(payload.Title) == "" {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "gitlab tracker: malformed comment payload"}, nil
	}
	marker := idempotencyMarker(entry.IdempotencyKey)
	found, err := a.findNote(ctx, iid, marker)
	if err != nil {
		return resultForError(err)
	}
	if found {
		return tracker.Result{Outcome: tracker.Converged}, nil
	}
	text := fmt.Sprintf("Stage %q %s.\n\n%s", payload.Title, payload.Action, marker)
	request := struct {
		Body string `json:"body"`
	}{Body: text}
	if err := a.request(ctx, http.MethodPost, a.issuePath(iid)+"/notes", nil, request, nil); err != nil {
		return resultForError(err)
	}
	return tracker.Result{Outcome: tracker.Delivered}, nil
}

type issue struct {
	IID         int      `json:"iid"`
	Description string   `json:"description"`
	WebURL      string   `json:"web_url"`
	State       string   `json:"state"` // "opened" | "closed"
	Labels      []string `json:"labels"`
	UpdatedAt   string   `json:"updated_at"`
}

type note struct {
	Body   string `json:"body"`
	System bool   `json:"system"`
}

type label struct {
	Name string `json:"name"`
}

type issueStateUpdate struct {
	StateEvent string `json:"state_event"`          // "close" | "reopen"
	AddLabels  string `json:"add_labels,omitempty"` // canceled label when configured
}

type statePayload struct {
	Disposition store.TrackerDisposition `json:"disposition"`
}

func stateUpdate(disposition store.TrackerDisposition, canceledLabel string) (issueStateUpdate, error) {
	switch disposition {
	case store.TrackerActive:
		return issueStateUpdate{StateEvent: "reopen"}, nil
	case store.TrackerCompleted:
		return issueStateUpdate{StateEvent: "close"}, nil
	case store.TrackerCanceled:
		return issueStateUpdate{StateEvent: "close", AddLabels: canceledLabel}, nil
	default:
		return issueStateUpdate{}, fmt.Errorf("gitlab tracker: unsupported state disposition %q", disposition)
	}
}

func (a *Adapter) readIssue(ctx context.Context, iid int) (issue, error) {
	var response issue
	err := a.request(ctx, http.MethodGet, a.issuePath(iid), nil, nil, &response)
	return response, err
}

// ReadState implements tracker.StateReader.
func (a *Adapter) ReadState(ctx context.Context, ref string) (tracker.LiveState, error) {
	iid, err := a.issueReference(ref)
	if err != nil {
		return tracker.LiveState{}, err
	}
	observed, err := a.readIssue(ctx, iid)
	if err != nil {
		return tracker.LiveState{}, err
	}
	if strings.TrimSpace(observed.UpdatedAt) == "" || (observed.State != "opened" && observed.State != "closed") {
		return tracker.LiveState{}, fmt.Errorf("gitlab tracker: issue read returned malformed state or updated_at")
	}

	if observed.State == "opened" {
		return tracker.LiveState{Class: tracker.LiveNonterminal, Display: "opened", Lease: observed.UpdatedAt}, nil
	}
	if a.canceledLabel != "" && hasLabel(observed, a.canceledLabel) {
		return tracker.LiveState{Class: tracker.LiveCanceled, Display: "closed (" + a.canceledLabel + ")", Lease: observed.UpdatedAt}, nil
	}
	return tracker.LiveState{Class: tracker.LiveCompleted, Display: "closed", Lease: observed.UpdatedAt}, nil
}

func (a *Adapter) writeIssueState(ctx context.Context, iid int, update issueStateUpdate) (issue, error) {
	var response issue
	err := a.request(ctx, http.MethodPut, a.issuePath(iid), nil, update, &response)
	return response, err
}

func (a *Adapter) state(ctx context.Context, entry store.OutboxEntry) (tracker.Result, error) {
	var payload statePayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "gitlab tracker: malformed state payload"}, nil
	}
	update, err := stateUpdate(payload.Disposition, a.canceledLabel)
	if err != nil {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: err.Error()}, nil
	}
	iid, err := a.issueReference(entry.Ref)
	if err != nil {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: err.Error()}, nil
	}

	observed, err := a.readIssue(ctx, iid)
	if err != nil {
		return resultForError(err)
	}
	if observed.UpdatedAt == "" || (observed.State != "opened" && observed.State != "closed") {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "gitlab tracker: issue read returned malformed state or updated_at"}, nil
	}

	if entry.Lease == "" {
		if observed.State != "opened" {
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
		if issueAtCandidate(observed, update, a.canceledLabel) {
			return tracker.Result{Outcome: tracker.Converged, Lease: observed.UpdatedAt}, nil
		}
		// A terminal state with a competing reason must not be overwritten,
		// even when its lease still matches the prior observation.
		if observed.State == "closed" || entry.Lease != observed.UpdatedAt {
			return leaseMismatch(observed), nil
		}
	}

	if update.AddLabels != "" {
		exists, err := a.labelExists(ctx, update.AddLabels)
		if err != nil {
			return resultForError(err)
		}
		if !exists {
			return tracker.Result{
				Outcome: tracker.PermanentRefusal,
				Reason: fmt.Sprintf(
					"gitlab tracker: label %q does not exist in project %s; create it or clear tracker.canceled-label",
					update.AddLabels, a.project,
				),
			}, nil
		}
	}

	written, err := a.writeIssueState(ctx, iid, update)
	if err != nil {
		return resultForError(err)
	}
	if written.UpdatedAt == "" {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "gitlab tracker: state response has no updated_at lease"}, nil
	}
	if !issueAtCandidate(written, update, a.canceledLabel) {
		return tracker.Result{Outcome: tracker.PermanentRefusal, Reason: "gitlab tracker: state response does not match requested state"}, nil
	}
	return tracker.Result{Outcome: tracker.Delivered, Lease: written.UpdatedAt}, nil
}

func issueAtCandidate(observed issue, update issueStateUpdate, canceledLabel string) bool {
	if update.StateEvent == "reopen" {
		return observed.State == "opened"
	}
	if observed.State != "closed" {
		return false
	}
	if canceledLabel == "" {
		return true
	}
	return hasLabel(observed, canceledLabel) == (update.AddLabels != "")
}

func hasLabel(observed issue, name string) bool {
	for _, candidate := range observed.Labels {
		if candidate == name {
			return true
		}
	}
	return false
}

func leaseMismatch(observed issue) tracker.Result {
	reason := fmt.Sprintf("gitlab tracker: lease mismatch: observed state %q", observed.State)
	if len(observed.Labels) > 0 {
		reason += fmt.Sprintf(" with labels %v", observed.Labels)
	}
	reason += fmt.Sprintf(" at updated_at %q", observed.UpdatedAt)
	return tracker.Result{Outcome: tracker.LeaseMismatch, Reason: reason}
}

func (a *Adapter) findIssue(ctx context.Context, marker string) (string, bool, error) {
	for page := 1; ; page++ {
		var issues []issue
		query := url.Values{
			"state":    {"all"},
			"per_page": {"100"},
			"page":     {strconv.Itoa(page)},
		}
		if err := a.request(ctx, http.MethodGet, a.issuesPath(), query, nil, &issues); err != nil {
			return "", false, err
		}
		for _, candidate := range issues {
			if strings.Contains(candidate.Description, marker) {
				if _, err := a.issueReference(candidate.WebURL); err != nil {
					return "", false, fmt.Errorf("gitlab tracker: deduplicated issue has no valid reference")
				}
				return candidate.WebURL, true, nil
			}
		}
		if len(issues) < 100 {
			return "", false, nil
		}
	}
}

func (a *Adapter) findNote(ctx context.Context, iid int, marker string) (bool, error) {
	for page := 1; ; page++ {
		var notes []note
		query := url.Values{
			"per_page": {"100"},
			"page":     {strconv.Itoa(page)},
		}
		if err := a.request(ctx, http.MethodGet, a.issuePath(iid)+"/notes", query, nil, &notes); err != nil {
			return false, err
		}
		for _, candidate := range notes {
			if candidate.System {
				continue
			}
			if strings.Contains(candidate.Body, marker) {
				return true, nil
			}
		}
		if len(notes) < 100 {
			return false, nil
		}
	}
}

func (a *Adapter) labelExists(ctx context.Context, name string) (bool, error) {
	for page := 1; ; page++ {
		var labels []label
		query := url.Values{
			"search":   {name},
			"per_page": {"100"},
			"page":     {strconv.Itoa(page)},
		}
		if err := a.request(ctx, http.MethodGet, "/projects/"+a.projectID+"/labels", query, nil, &labels); err != nil {
			return false, err
		}
		for _, candidate := range labels {
			if candidate.Name == name {
				return true, nil
			}
		}
		if len(labels) < 100 {
			return false, nil
		}
	}
}

func (a *Adapter) issuesPath() string {
	return "/projects/" + a.projectID + "/issues"
}

func (a *Adapter) issuePath(iid int) string {
	return a.issuesPath() + "/" + strconv.Itoa(iid)
}

// issueReference validates that ref is an issue or work-item URL on a.host
// for a.project and returns the internal issue id (G3).
func (a *Adapter) issueReference(ref string) (int, error) {
	fail := fmt.Errorf("gitlab tracker: reference %q is not an issue URL for %s/%s", ref, a.host, a.project)
	parsed, err := url.Parse(ref)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return 0, fail
	}
	if !strings.EqualFold(parsed.Hostname(), a.host) {
		return 0, fail
	}
	parts := strings.SplitN(strings.Trim(parsed.Path, "/"), "/-/", 2)
	if len(parts) != 2 {
		return 0, fail
	}
	if !strings.EqualFold(parts[0], a.project) {
		return 0, fail
	}
	segments := strings.Split(parts[1], "/")
	if len(segments) != 2 {
		return 0, fail
	}
	kind := strings.ToLower(segments[0])
	if kind != "issues" && kind != "work_items" {
		return 0, fail
	}
	number, err := strconv.Atoi(segments[1])
	if err != nil || number < 1 {
		return 0, fail
	}
	return number, nil
}

func (a *Adapter) request(ctx context.Context, method, path string, query url.Values, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	target := a.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", a.token)
	req.Header.Set("User-Agent", "wip-tracker")
	req.Header.Set("Accept", "application/json")
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
		var hint string
		if response.StatusCode == http.StatusNotFound {
			hint = "project " + a.project + "; check the token has api scope"
		}
		return &apiError{
			status:      response.StatusCode,
			message:     strings.TrimSpace(string(message)),
			rateLimited: isRateLimited(response.Header),
			hint:        hint,
		}
	}
	if output == nil {
		_, err = io.Copy(io.Discard, response.Body)
		return err
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("gitlab tracker: decode response: %w", err)
	}
	return nil
}

type apiError struct {
	status      int
	message     string
	rateLimited bool
	hint        string
}

func (e *apiError) Error() string {
	text := fmt.Sprintf("gitlab tracker: HTTP %d", e.status)
	if e.message != "" {
		text += ": " + e.message
	}
	if e.hint != "" {
		text += " (" + e.hint + ")"
	}
	return text
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

func isRateLimited(header http.Header) bool {
	if strings.TrimSpace(header.Get("Retry-After")) != "" {
		return true
	}
	return strings.TrimSpace(header.Get("RateLimit-Remaining")) == "0"
}

func idempotencyMarker(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "<!-- wip-idempotency:" + hex.EncodeToString(sum[:]) + " -->"
}

// project parses a store-normalized or raw GitLab remote into a lower-case
// host and an unescaped project path with two or more segments (G1).
func project(remote string) (string, string, error) {
	remote = strings.TrimSpace(remote)
	fail := func() (string, string, error) {
		return "", "", fmt.Errorf("gitlab tracker: Repo remote %q is not a GitLab project path (host/group/project)", remote)
	}

	var host, path string
	switch {
	case strings.Contains(remote, "://"):
		parsed, err := url.Parse(remote)
		if err != nil {
			return fail()
		}
		host = strings.ToLower(parsed.Hostname())
		path = parsed.Path
	case func() bool {
		at := strings.Index(remote, "@")
		colon := strings.Index(remote, ":")
		return at >= 0 && colon >= 0 && at < colon
	}():
		at := strings.Index(remote, "@")
		colon := strings.Index(remote, ":")
		host = remote[at+1 : colon]
		path = remote[colon+1:]
	default:
		slash := strings.Index(remote, "/")
		if slash < 0 {
			return fail()
		}
		host = remote[:slash]
		path = remote[slash+1:]
		if idx := strings.LastIndex(host, ":"); idx >= 0 {
			if _, err := strconv.Atoi(host[idx+1:]); err == nil {
				host = host[:idx]
			}
		}
	}

	host = strings.ToLower(strings.TrimSpace(host))
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	segments := strings.Split(path, "/")
	if host == "" || strings.Contains(host, ":") || len(segments) < 2 {
		return fail()
	}
	for _, segment := range segments {
		if segment == "" {
			return fail()
		}
	}
	if host == "github.com" {
		return fail()
	}
	return host, strings.Join(segments, "/"), nil
}
