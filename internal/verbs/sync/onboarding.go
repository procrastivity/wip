package sync

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/procrastivity/wip/internal/config"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/wiperr"
	"golang.org/x/term"
)

// OnboardingResult keeps local initialization and optional network outcomes
// separate. A retained binding is never undone by a failed registration/pass.
type OnboardingResult struct {
	State   string                 `json:"state"`
	Message string                 `json:"message"`
	Binding *store.ExchangeBinding `json:"binding,omitempty"`
	Sync    *store.ExchangeStatus  `json:"sync,omitempty"`
}

type onboardingConfig struct {
	OnInit      bool   `json:"on_init"`
	DefaultJoin string `json:"default_join"`
}

type remoteIdentity struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Remote string `json:"remote_url"`
}

type onboardingCandidate struct {
	Selection    selection
	Organization remoteIdentity
	Repo         remoteIdentity
}

// Onboard runs only after local init has committed and its store is closed.
// Unconfigured init does not read credentials, discover servers, or create any
// exchange state. The terminal is optional; configured auto joining also works
// with JSON/piped input, while ask never consumes non-terminal input.
func Onboard(ctx context.Context, repo store.Repo, input io.Reader, output io.Writer, allowPrompt bool) (*OnboardingResult, error) {
	incomplete := func(err error) (*OnboardingResult, error) {
		return &OnboardingResult{State: "incomplete", Message: "Local initialization succeeded; onboarding incomplete: " + err.Error()}, nil
	}
	c, err := config.Load()
	if err != nil {
		return incomplete(err)
	}
	raw, err := json.Marshal(c["sync"])
	if err != nil {
		return incomplete(err)
	}
	var block struct{ Onboarding onboardingConfig }
	if err := json.Unmarshal(raw, &block); err != nil {
		return incomplete(fmt.Errorf("invalid sync onboarding configuration"))
	}
	if !block.Onboarding.OnInit {
		return nil, nil
	}
	interactive := false
	if f, ok := input.(*os.File); ok {
		interactive = allowPrompt && term.IsTerminal(int(f.Fd()))
	}
	ui := onboardingUI{bufio.NewReader(input), output}
	s, err := tiers.OpenStore()
	if err != nil {
		return incomplete(err)
	}
	status, err := s.ExchangeStatus(ctx, repo.ID)
	held := false
	if err == nil {
		held, err = s.ExchangeOnboardingHeld(ctx, repo.ID)
	}
	if err == nil && held && interactive {
		var n int
		n, err = ui.choose("This project was kept local by connection forget. Reconnect explicitly?", []string{"Keep local", "Reconnect"})
		if err == nil && n == 1 {
			err = withRegistryLock(func(_ string) error {
				current, err := s.ExchangeStatus(ctx, repo.ID)
				if err != nil {
					return err
				}
				if current.Generation != status.Generation {
					return fmt.Errorf("project reset changed while prompting; retry init")
				}
				return s.ReleaseExchangeHold(ctx, repo.ID, status.Generation)
			})
			held = err != nil
		}
	}
	closeErr := s.Close()
	if err != nil {
		return incomplete(err)
	}
	if closeErr != nil {
		return incomplete(closeErr)
	}
	if held {
		return &OnboardingResult{State: "local", Message: "Kept local after connection forget; run init in a terminal and choose Reconnect, or explicitly pair."}, nil
	}
	if status.Paired {
		result := &OnboardingResult{State: "retained", Message: fmt.Sprintf("Retained pairing: %s (active=%v). Use wip plumbing sync run to sync; init never changes this destination.", status.Binding.Endpoint, status.Active), Binding: &status.Binding}
		if !interactive || !status.Active {
			return result, nil
		}
		choice, err := ui.choose(result.Message, []string{"Keep local", "Sync now"})
		if err != nil {
			return incomplete(err)
		}
		if choice != 1 {
			return result, nil
		}
		return onboardingRun(ctx, repo.ID, result)
	}
	// Reconcile an uncertain creation before route selection or cross-server
	// search can offer another destination. Changed rules do not change the
	// operation's retained server, account or payload.
	pending, err := readPendingCreation(repo.ID)
	if err != nil {
		return incomplete(err)
	}
	if pending != nil {
		if !interactive {
			return incomplete(fmt.Errorf("pending creation must be reconciled by running wip init in a terminal"))
		}
		selected, err := selectInitial(pending.Connection, pending.Server, repo.RemoteURL)
		if err != nil {
			return incomplete(err)
		}
		if pending.Account != selected.Account {
			return incomplete(fmt.Errorf("pending creation belongs to another account; restore that connection to reconcile it"))
		}
		n, err := ui.choose("Resume pending creation of "+pending.Payload.Name+" in organization "+pending.Organization+" via "+pending.Connection+" on "+pending.Server, []string{"Keep local", "Resume the same creation request"})
		if err != nil {
			return incomplete(err)
		}
		if n == 0 {
			return &OnboardingResult{State: "local", Message: "Pending creation retained; no request sent."}, nil
		}
		return onboardingCreate(ctx, repo, selected, *pending, &ui)
	}
	routing, err := readRouting("")
	if err != nil {
		return incomplete(err)
	}
	policy := joinPolicy{Mode: block.Onboarding.DefaultJoin}
	if policy.Mode == "" {
		policy.Mode = "ask"
	}
	var selected selection
	rule, routeErr := selectRoute(repo.RemoteURL, routing.Routes, routing)
	if routeErr == nil {
		selected, err = selectInitial(rule.Connection, "", repo.RemoteURL)
		if err != nil {
			return incomplete(err)
		}
		if rule.Join.Mode != "" {
			policy.Mode = rule.Join.Mode
		}
		policy.OrganizationID = rule.Join.OrganizationID
		if interactive {
			if _, err := fmt.Fprintf(output, "Remote %q matches route %q: %s on %s (account %s).\n", repo.RemoteURL, rule.Repo, selected.Connection, selected.Server, selected.Account); err != nil {
				return incomplete(err)
			}
		}
	}
	if policy.Mode != "ask" && policy.Mode != "auto" {
		return incomplete(fmt.Errorf("join mode must be ask or auto"))
	}
	if policy.Mode == "auto" && !destinationID(policy.OrganizationID) {
		return incomplete(fmt.Errorf("auto join requires an organization_id"))
	}
	if !interactive && (routeErr != nil || policy.Mode != "auto") {
		return &OnboardingResult{State: "local", Message: "Local project ready. Run wip init in a terminal to choose a connection/project."}, nil
	}
	if routeErr != nil {
		if _, err := fmt.Fprintf(output, "No connection selected: %v\n", routeErr); err != nil {
			return incomplete(err)
		}
		choices, err := onboardingConnections()
		if err != nil {
			return incomplete(err)
		}
		labels := []string{"Keep local", "Find this project across my connections"}
		for _, choice := range choices {
			labels = append(labels, choice.Connection+" ("+choice.Server+")")
		}
		n, err := ui.choose("Choose connection", labels)
		if err != nil {
			return incomplete(err)
		}
		if n == 0 {
			return &OnboardingResult{State: "local", Message: "Kept local; no destination selected."}, nil
		}
		if n == 1 {
			return onboardingSearch(ctx, repo, choices, &ui)
		}
		selected = choices[n-2]
		// An explicitly chosen connection is prompted, not auto permission
		// inherited from a rule which did not match.
		policy = joinPolicy{Mode: "ask"}
	}
	lookup, cancel := context.WithTimeout(ctx, 30*time.Second)
	groups, err := discoverRepos(lookup, selected)
	cancel()
	if err != nil {
		return incomplete(fmt.Errorf("%s lookup incomplete (%v); sign in with wip plumbing sync login if needed", selected.Connection, err))
	}
	// /api/me may have pinned an account for legacy credentials during lookup.
	selected, err = selectInitial(selected.Connection, selected.Server, repo.RemoteURL)
	if err != nil {
		return incomplete(err)
	}
	var candidates []onboardingCandidate
	if repo.RemoteURL != "" {
		candidates, err = matchingCandidates(selected, groups, repo.RemoteURL, policy.OrganizationID)
		if err != nil {
			return incomplete(err)
		}
	}
	if policy.Mode == "auto" {
		if len(candidates) == 1 {
			return onboardingPair(ctx, repo, candidates[0], true, nil)
		}
		if !interactive {
			return incomplete(fmt.Errorf("auto join found %d matching Repos in the configured organization; choose a destination with interactive init", len(candidates)))
		}
		if _, err := fmt.Fprintln(output, "Automatic join did not select a unique project; choose explicitly instead."); err != nil {
			return incomplete(err)
		}
	}
	return onboardingChoose(ctx, repo, selected, groups, candidates, &ui)
}

func matchingCandidates(selected selection, groups []organizationRepos, key, scope string) ([]onboardingCandidate, error) {
	var out []onboardingCandidate
	for _, group := range groups {
		var org remoteIdentity
		if err := json.Unmarshal(group.Organization, &org); err != nil {
			return nil, err
		}
		if scope != "" && org.ID != scope {
			continue
		}
		for _, raw := range group.Repos {
			var r remoteIdentity
			if err := json.Unmarshal(raw, &r); err != nil {
				return nil, err
			}
			if !destinationID(r.ID) {
				return nil, fmt.Errorf("invalid server Repo identity")
			}
			// Laravel stores the canonical host/path key. Also accept raw Git
			// URLs from compatible servers, without parsing a canonical key as
			// scp syntax (notably keys containing a non-default port).
			if key != "" && r.Remote != key {
				normal, err := tiers.NormalizeRemote(r.Remote)
				if err != nil || normal != key {
					continue
				}
			}
			out = append(out, onboardingCandidate{selected, org, r})
		}
	}
	return out, nil
}

func onboardingConnections() ([]selection, error) {
	r, err := loadRegistry()
	if err != nil {
		return nil, err
	}
	var out []selection
	for name, c := range r.Connections {
		out = append(out, selection{Connection: name, Server: c.Server, Account: c.Account, CredentialPresent: c.Token != ""})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Connection < out[j].Connection })
	return out, nil
}

func onboardingSearch(ctx context.Context, repo store.Repo, choices []selection, ui *onboardingUI) (*OnboardingResult, error) {
	if repo.RemoteURL == "" {
		return &OnboardingResult{State: "incomplete", Message: "No selected remote identity to search for. Run init and choose a connection to browse projects explicitly."}, nil
	}
	var candidates []onboardingCandidate
	for _, selected := range choices {
		lookup, cancel := context.WithTimeout(ctx, 30*time.Second)
		groups, err := discoverRepos(lookup, selected)
		cancel()
		if err != nil {
			if _, e := fmt.Fprintf(ui.out, "%s lookup incomplete: %v\n", selected.Connection, err); e != nil {
				return nil, e
			}
			continue
		}
		found, err := matchingCandidates(selected, groups, repo.RemoteURL, "")
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, found...)
	}
	return onboardingSelect(ctx, repo, candidates, ui)
}

func onboardingChoose(ctx context.Context, repo store.Repo, selected selection, groups []organizationRepos, matches []onboardingCandidate, ui *onboardingUI) (*OnboardingResult, error) {
	n, err := ui.choose(fmt.Sprintf("%s on %s (account %s): %d matching accessible Repos", selected.Connection, selected.Server, selected.Account, len(matches)), []string{"Keep local", "Join a matching project", "Browse all accessible projects", "Create a shared project", "Find this project across my connections"})
	if err != nil {
		return nil, err
	}
	switch n {
	case 1:
		return onboardingSelect(ctx, repo, matches, ui)
	case 2:
		all, err := matchingCandidates(selected, groups, "", "")
		if err != nil {
			return nil, err
		}
		return onboardingSelect(ctx, repo, all, ui)
	case 3:
		pending, err := promptCreation(repo, selected, groups, ui)
		if err != nil {
			return nil, err
		}
		if pending != nil {
			return onboardingCreate(ctx, repo, selected, *pending, ui)
		}
	case 4:
		choices, err := onboardingConnections()
		if err != nil {
			return nil, err
		}
		return onboardingSearch(ctx, repo, choices, ui)
	}
	return &OnboardingResult{State: "local", Message: "Kept local; no pairing or sync performed."}, nil
}

func onboardingSelect(ctx context.Context, repo store.Repo, candidates []onboardingCandidate, ui *onboardingUI) (*OnboardingResult, error) {
	labels := []string{"Keep local"}
	for _, c := range candidates {
		labels = append(labels, fmt.Sprintf("%s / %s via %s on %s (account %s)", c.Organization.Name, c.Repo.Name, c.Selection.Connection, c.Selection.Server, c.Selection.Account))
	}
	n, err := ui.choose("Choose shared project (only successful lookups are listed)", labels)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return &OnboardingResult{State: "local", Message: "Kept local; no pairing performed."}, nil
	}
	c := candidates[n-1]
	n, err = ui.choose("Pair with "+labels[n]+"? Supported project work syncs regardless of age; unsupported work stays local.", []string{"Keep local", "Confirm pairing"})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return &OnboardingResult{State: "local", Message: "Pairing cancelled."}, nil
	}
	result, err := onboardingPair(ctx, repo, c, false, nil)
	if err != nil {
		return result, err
	}
	return offerFirstSync(ctx, repo.ID, result, ui)
}

func onboardingPair(ctx context.Context, repo store.Repo, c onboardingCandidate, firstSync bool, expected *pairGeneration) (*OnboardingResult, error) {
	pairCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	s, err := tiers.OpenStore()
	if err != nil {
		cancel()
		return nil, err
	}
	status, _, err := applyPair(pairCtx, s, repo.ID, c.Selection, c.Organization.ID, c.Repo.ID, expected)
	closeErr := s.Close()
	cancel()
	result := &OnboardingResult{State: "paired", Message: "Paired. Supported originals sync regardless of age; unsupported work stays local and inspectable."}
	if status.Paired {
		result.Binding = &status.Binding
	}
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return onboardingFailed(result, err)
	}
	if firstSync {
		return onboardingRun(ctx, repo.ID, result)
	}
	return result, nil
}

// offerFirstSync is shared by prompted creation and joining. Auto mode skips
// the prompt and directly calls the same one-pass exchange operation.
func offerFirstSync(ctx context.Context, repo string, result *OnboardingResult, ui *onboardingUI) (*OnboardingResult, error) {
	n, err := ui.choose("Run first sync of supported work?", []string{"Not now", "Sync now"})
	if err != nil {
		return result, err
	}
	if n == 1 {
		return onboardingRun(ctx, repo, result)
	}
	return result, nil
}

func onboardingRun(ctx context.Context, repo string, result *OnboardingResult) (*OnboardingResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	s, err := tiers.OpenStore()
	if err != nil {
		return onboardingFailed(result, err)
	}
	status, err := runBound(ctx, s, repo)
	closeErr := s.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return onboardingFailed(result, err)
	}
	result.State = "synced"
	result.Message = "One sync pass completed. Ongoing sync remains explicit; waiting/conflict decisions are not forced through."
	result.Binding = &status.Binding
	result.Sync = &status
	return result, nil
}

func onboardingFailed(result *OnboardingResult, err error) (*OnboardingResult, error) {
	result.State = "failed"
	result.Message = "Local initialization succeeded; onboarding failed: " + err.Error() + ". Retained state was not rolled back. Retry with wip init, or wip plumbing sync run if paired."
	return result, wiperr.New("sync.failed", result.Message)
}

type onboardingUI struct {
	in  *bufio.Reader
	out io.Writer
}

func (ui *onboardingUI) line(label, defaultValue string) (string, error) {
	if _, err := fmt.Fprintf(ui.out, "%s [%s]: ", label, defaultValue); err != nil {
		return "", err
	}
	line, err := ui.in.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	if err == io.EOF {
		return "", io.EOF
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = defaultValue
	}
	return line, nil
}

func (ui *onboardingUI) choose(label string, options []string) (int, error) {
	if _, err := fmt.Fprintf(ui.out, "%s\n", label); err != nil {
		return 0, err
	}
	for i, option := range options {
		if _, err := fmt.Fprintf(ui.out, "  %d. %q\n", i, option); err != nil {
			return 0, err
		}
	}
	for {
		line, err := ui.line("Choice", "0")
		if err == io.EOF {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		n, err := strconv.Atoi(line)
		if err == nil && n >= 0 && n < len(options) {
			return n, nil
		}
		if _, err := fmt.Fprintln(ui.out, "Choose one of the listed numbers."); err != nil {
			return 0, err
		}
	}
}
