// Package tracker implements `wip plumbing tracker`: the operator-facing
// window onto the provider seam that internal/tracker owns.
//
// Three of its verbs are reads — `read` is the only one that touches the
// network, `refs` and `capabilities` never do — and the `propose` trio writes
// exactly one queued outbox candidate each. Nothing here delivers: the human
// boundary stays where MODEL §10 and the Matter's D15 put it, at `wip plumbing
// outbox approve` followed by `wip plumbing outbox flush`.
//
// The seam package is imported as `seam` because this package is itself named
// tracker: internal/tracker is the provider-neutral boundary, and
// internal/verbs/tracker is the verb surface over it.
package tracker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	seam "github.com/procrastivity/wip/internal/tracker"
	"github.com/procrastivity/wip/internal/wiperr"
	"github.com/procrastivity/wip/internal/writesurface"
)

// outboxApproveVerb and outboxFlushVerb are the two commands that move a
// queued proposal into effect. Named once so approvalSentence and the
// propose Shorts (which must each name them, F1) share one source of truth
// and cannot drift apart.
const (
	outboxApproveVerb = "wip plumbing outbox approve"
	outboxFlushVerb   = "wip plumbing outbox flush"
)

// approvalSentence is the one thing every propose verb's help has to say, and
// the group's Long repeats: a proposal is local until a person approves and
// flushes it. Written once so the four copies cannot drift.
const approvalSentence = "Nothing reaches the tracker until a person runs " + outboxApproveVerb + " and " + outboxFlushVerb + "."

// refFormats is the addressing note every ref-taking verb carries. The verbs
// themselves treat a reference as opaque — only the configured backend
// interprets it — so the help text is the only place a person learns which
// spelling their backend wants.
const refFormats = "A reference is opaque to wip and is spelled the way the configured backend spells it. " +
	"github: the full issue URL, e.g. https://github.com/owner/repo/issues/12. " +
	"gitlab: the full issue URL, e.g. https://gitlab.com/group/project/-/issues/12. " +
	"linear: the issue identifier, e.g. ABC-123."

// Command constructs the `wip plumbing tracker` command group. providers is
// injected at the CLI registration point, exactly as the outbox group takes
// it: this package registers no provider and names none.
func Command(streams *iostreams.Streams, providers *seam.Registry) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tracker",
		Short: "read the configured tracker and propose work for it",
		Long: "wip plumbing tracker is the operator's window onto the configured provider seam.\n\n" +
			"read contacts the tracker; refs and capabilities are local reads that contact nothing. " +
			"The propose verbs queue one outbox candidate each and deliver none of it. " +
			approvalSentence,
	}
	cmd.AddCommand(
		capabilitiesCommand(streams, providers),
		proposeCommand(streams),
		readCommand(streams, providers),
		refsCommand(streams),
	)
	return plumbing(cmd)
}

// plumbing annotates one command with its manifest surface kind. Every command
// in this package goes through it — the group, the propose group and every
// leaf — because the manifest walk hard-errors on an unannotated leaf
// (internal/manifest/verbs.go's collect) and annotating the groups too costs
// nothing and leaves no leaf to forget.
func plumbing(cmd *cobra.Command) *cobra.Command {
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

// openRepo resolves the current worktree's Repo and opens the store, the same
// way the outbox verbs do. Every verb in this package is Repo-tier.
func openRepo(cmd *cobra.Command) (*store.Store, store.Repo, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, store.Repo{}, err
	}
	s, err := tiers.OpenStore()
	if err != nil {
		return nil, store.Repo{}, err
	}
	repo, err := writesurface.CurrentRepo(cmd.Context(), s, dir)
	if err != nil {
		_ = s.Close()
		return nil, store.Repo{}, err
	}
	return s, repo, nil
}

func actor(cmd *cobra.Command) store.Actor {
	return store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole)
}

// emit writes payload as one JSON line. Every verb's --json path ends here, so
// the encoding decisions (one line, no HTML escaping surprises, trailing
// newline) are made once.
func emit(streams *iostreams.Streams, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(streams.Out, string(b))
	return err
}

// none renders an unset opaque config value for a human. The JSON surface
// keeps the empty string: "none" is a reading aid, not data.
func none(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

// -- read ---------------------------------------------------------------

type stateJSON struct {
	// Class is the provider-neutral lifecycle class (backlog, active,
	// nonterminal, completed, canceled, terminal).
	Class string `json:"class"`
	// Display is the provider's own state name, kept verbatim for a person.
	Display string `json:"display"`
}

// readJSON is `tracker read`'s payload. Capability discriminates the two
// answers a backend can give: "content" when it implements ContentReader,
// "state" when only StateReader is available — so a caller can tell an absent
// title from an empty one. State is always present; title, body, url and
// updatedAt are absent under the state fallback and omitted when the provider
// reports them empty.
//
// The observed lease is deliberately not published. It is the flush path's own
// concurrency token, minted and consumed inside the seam, and there is no verb
// that would accept one back (the propose verbs take no --lease).
type readJSON struct {
	Ref        string    `json:"ref"`
	Backend    string    `json:"backend"`
	Capability string    `json:"capability"`
	Title      string    `json:"title,omitempty"`
	Body       string    `json:"body,omitempty"`
	URL        string    `json:"url,omitempty"`
	State      stateJSON `json:"state"`
	UpdatedAt  string    `json:"updatedAt,omitempty"`
}

func readCommand(streams *iostreams.Streams, providers *seam.Registry) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "read <ref>",
		Short: "read one tracker item through the configured backend — read-only, emits no event",
		Long: "read one tracker item through the configured backend.\n\n" +
			"This is the one verb in this group that contacts the tracker. It emits no event and " +
			"queues nothing: the answer is the provider's current observation, not a durable fact.\n\n" +
			refFormats + "\n\n" +
			"A backend that can read bodies answers with the title, body, url and state. " +
			"A backend that can only read lifecycle answers with the state alone. " +
			"A backend that can read neither is not a failure of this repo's configuration and is reported as such.",
		Args: cobra.ExactArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ref := strings.TrimSpace(args[0])
		if ref == "" {
			return wiperr.New("validation.missing-reference", "a tracker read needs the reference it reads")
		}
		s, repo, err := openRepo(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()

		provider, config, err := resolveSeam(cmd, s, providers, repo.ID)
		if err != nil {
			return err
		}

		if reader, ok := provider.(seam.ContentReader); ok {
			content, err := reader.ReadContent(cmd.Context(), ref)
			if err != nil {
				return err
			}
			return writeRead(cmd, streams, readContentJSON(ref, config.Backend, content))
		}
		if reader, ok := provider.(seam.StateReader); ok {
			state, err := reader.ReadState(cmd.Context(), ref)
			if err != nil {
				return err
			}
			return writeRead(cmd, streams, readJSON{
				Ref: ref, Backend: config.Backend, Capability: "state",
				State: stateJSON{Class: string(state.Class), Display: state.Display},
			})
		}
		return wiperr.New("validation.tracker-read-unsupported",
			fmt.Sprintf("the %s backend cannot read tracker items", config.Backend))
	}
	return plumbing(cmd)
}

func readContentJSON(ref, backend string, content seam.Content) readJSON {
	out := readJSON{
		Ref: ref, Backend: backend, Capability: "content",
		Title: content.Title, Body: content.Body, URL: content.URL,
		State: stateJSON{Class: string(content.State.Class), Display: content.State.Display},
	}
	if !content.UpdatedAt.IsZero() {
		out.UpdatedAt = content.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func writeRead(cmd *cobra.Command, streams *iostreams.Streams, payload readJSON) error {
	if cliflags.FromContext(cmd.Context()).JSON {
		return emit(streams, payload)
	}
	heading := payload.Title
	if heading == "" {
		heading = payload.Ref
	}
	if _, err := fmt.Fprintf(streams.Out, "%s\n  ref: %s\n  state: %s\n", heading, payload.Ref, displayState(payload.State)); err != nil {
		return err
	}
	if payload.URL != "" {
		if _, err := fmt.Fprintf(streams.Out, "  url: %s\n", payload.URL); err != nil {
			return err
		}
	}
	if payload.UpdatedAt != "" {
		if _, err := fmt.Fprintf(streams.Out, "  updated: %s\n", payload.UpdatedAt); err != nil {
			return err
		}
	}
	if payload.Capability == "state" {
		_, err := fmt.Fprintf(streams.Out, "  (the %s backend reads lifecycle only; it cannot read titles or bodies)\n", payload.Backend)
		return err
	}
	if body := strings.TrimRight(payload.Body, "\n"); body != "" {
		_, err := fmt.Fprintf(streams.Out, "\n%s\n", body)
		return err
	}
	return nil
}

// displayState renders class and the provider's own word together, and the
// class alone when the provider reported no word or the same one.
func displayState(s stateJSON) string {
	if s.Display == "" || s.Display == s.Class {
		return s.Class
	}
	return s.Class + " (" + s.Display + ")"
}

// resolveSeam constructs the configured seam and turns the two refusals a
// person can act on into coded errors. Every other failure — a store read, a
// provider's own construction error — travels unwrapped, as it does in the
// flush verb.
func resolveSeam(cmd *cobra.Command, s *store.Store, providers *seam.Registry, repoID string) (seam.Seam, seam.SeamConfig, error) {
	provider, config, err := seam.ResolveSeam(cmd.Context(), s.View, providers, repoID)
	if errors.Is(err, seam.ErrNoBackend) {
		return nil, config, wiperr.New("validation.no-tracker-backend",
			"this repo configures no tracker backend; set one with wip plumbing outbox backend <name>")
	}
	if err != nil {
		return nil, config, err
	}
	if provider == nil {
		return nil, config, wiperr.New("validation.no-tracker-backend",
			fmt.Sprintf("the %s backend constructed no seam", config.Backend))
	}
	return provider, config, nil
}

// -- refs ---------------------------------------------------------------

// refJSON is one live binding: the Matter both ways, the reference, and the
// disposition the local model expects the reference to be in. An empty
// disposition is a real answer — every Matter bound to that reference is still
// Planned, so nothing is deliverable yet — and is never omitted, so a consumer
// reads a field rather than an absence.
type refJSON struct {
	Matter      string `json:"matter"`
	Locator     string `json:"locator"`
	Ref         string `json:"ref"`
	Disposition string `json:"disposition"`
}

type refsJSON struct {
	Repo       string    `json:"repo"`
	References []refJSON `json:"references"`
}

func refsCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "refs [node-locator]",
		Short: "list this repo's live tracker bindings, local only, contacts no tracker — read-only, emits no event",
		Long: "list this repo's live tracker bindings.\n\n" +
			"With no argument, every live (Matter, reference) binding in this repo. With a node " +
			"locator, the bindings of that node's Matter — a Stage or Step resolves to the Matter " +
			"that owns it, because a reference binds to a Matter (D44).\n\n" +
			"The disposition is what this store's own model expects the reference to be in, not what " +
			"the tracker says: this verb constructs no provider seam and contacts no tracker. " +
			"An empty disposition means every Matter bound to that reference is still planned.",
		Args: cobra.MaximumNArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		s, repo, err := openRepo(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()

		var bindings []store.MatterReference
		if len(args) == 1 {
			bindings, err = matterBindings(cmd, s, repo.ID, args[0])
		} else {
			bindings, err = s.RepoTrackerReferences(cmd.Context(), repo.ID)
		}
		if err != nil {
			return err
		}

		payload := refsJSON{Repo: repo.ID, References: make([]refJSON, 0, len(bindings))}
		// Two Matters legitimately share one reference, and the aggregate is
		// per reference, so the disposition of a repeated reference is read
		// once and reused.
		dispositions := make(map[string]string, len(bindings))
		for _, binding := range bindings {
			disposition, seen := dispositions[binding.Ref]
			if !seen {
				d, _, err := s.TrackerAggregate(cmd.Context(), binding.Ref)
				if err != nil {
					return err
				}
				disposition = string(d)
				dispositions[binding.Ref] = disposition
			}
			payload.References = append(payload.References, refJSON{
				Matter: binding.Matter, Locator: binding.Locator, Ref: binding.Ref,
				Disposition: disposition,
			})
		}

		if cliflags.FromContext(cmd.Context()).JSON {
			return emit(streams, payload)
		}
		if len(payload.References) == 0 {
			_, err := fmt.Fprintln(streams.Out, "no live tracker references")
			return err
		}
		for _, r := range payload.References {
			disposition := r.Disposition
			if disposition == "" {
				disposition = "-"
			}
			if _, err := fmt.Fprintf(streams.Out, "%-24s %-9s %s\n", r.Locator, disposition, r.Ref); err != nil {
				return err
			}
		}
		return nil
	}
	return plumbing(cmd)
}

// matterBindings answers the locator form. The locator may name any live node;
// the bindings belong to the Matter that owns it, which is the node itself when
// the node is a Matter (store.Node.Matter).
func matterBindings(cmd *cobra.Command, s *store.Store, repoID, locator string) ([]store.MatterReference, error) {
	node, err := writesurface.ResolveNode(cmd.Context(), s.View, repoID, locator)
	if err != nil {
		return nil, err
	}
	matter, err := s.Node(cmd.Context(), node.Matter)
	if err != nil {
		return nil, err
	}
	refs, err := s.TrackerReferences(cmd.Context(), matter.ID)
	if err != nil {
		return nil, err
	}
	out := make([]store.MatterReference, 0, len(refs))
	for _, ref := range refs {
		out = append(out, store.MatterReference{Matter: matter.ID, Locator: matter.Locator, Ref: ref})
	}
	return out, nil
}

// -- capabilities -------------------------------------------------------

// seamStatusJSON reports what the configured backend can actually do. A
// construction failure — a missing token, an unparseable remote — is data
// here, not a failure of this verb: the whole point is to be able to ask what
// is wrong, so constructed is false, error carries the provider's own words,
// and the exit code stays 0.
//
// Deliver is always true once constructed: the Seam interface requires a
// Deliver method, so every constructed seam has one. It is reported for
// shape completeness alongside readState/readContent, not because it ever
// discriminates one backend from another.
type seamStatusJSON struct {
	Constructed bool   `json:"constructed"`
	Error       string `json:"error,omitempty"`
	Deliver     bool   `json:"deliver"`
	ReadState   bool   `json:"readState"`
	ReadContent bool   `json:"readContent"`
}

type capabilitiesJSON struct {
	Repo          string         `json:"repo"`
	Backend       string         `json:"backend"`
	Target        string         `json:"target"`
	Project       string         `json:"project"`
	CanceledLabel string         `json:"canceledLabel"`
	PushLevel     string         `json:"pushLevel"`
	BacklogPush   string         `json:"backlogPush"`
	Seam          seamStatusJSON `json:"seam"`
}

func capabilitiesCommand(streams *iostreams.Streams, providers *seam.Registry) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capabilities",
		Short: "report this repo's tracker configuration and what the backend can do, contacts no tracker — read-only, emits no event",
		Long: "report this repo's tracker configuration and what the configured backend can do.\n\n" +
			"The seam is constructed locally, which is where a missing token or an unusable remote " +
			"shows up. That is reported as data — seam.constructed is false and seam.error carries " +
			"the reason — and the verb still exits 0: a broken configuration is exactly what this " +
			"verb exists to describe.\n\n" +
			"No tracker is contacted. The three capability flags are read off the constructed seam's " +
			"own type, not from a request.",
		Args: cobra.NoArgs,
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		s, repo, err := openRepo(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()

		config, err := seam.ReadSeamConfig(cmd.Context(), s.View, repo.ID)
		if err != nil {
			return err
		}
		level, err := s.EffectiveTrackerPushLevel(cmd.Context(), repo.ID)
		if err != nil {
			return err
		}
		mode, err := s.EffectiveTrackerBacklogPush(cmd.Context(), repo.ID)
		if err != nil {
			return err
		}

		payload := capabilitiesJSON{
			Repo: repo.ID, Backend: config.Backend, Target: config.Target,
			Project: config.Project, CanceledLabel: config.CanceledLabel,
			PushLevel: string(level), BacklogPush: string(mode),
		}
		// ReadSeamConfig returns early with only Backend set when no backend
		// is configured, so Configured() (which folds the "" and "none"
		// sentinel together) reports false and there is nothing to
		// construct.
		if config.Configured() {
			payload.Seam = seamStatus(providers, config)
		}

		if cliflags.FromContext(cmd.Context()).JSON {
			return emit(streams, payload)
		}
		if _, err := fmt.Fprintf(streams.Out,
			"backend: %s\ntarget: %s\nproject: %s\ncanceled-label: %s\npush-level: %s\nbacklog-push: %s\n",
			none(payload.Backend), none(payload.Target), none(payload.Project),
			none(payload.CanceledLabel), payload.PushLevel, payload.BacklogPush); err != nil {
			return err
		}
		switch {
		case payload.Seam.Constructed:
			_, err = fmt.Fprintf(streams.Out, "seam: constructed; deliver=%t readState=%t readContent=%t\n",
				payload.Seam.Deliver, payload.Seam.ReadState, payload.Seam.ReadContent)
		case payload.Seam.Error != "":
			_, err = fmt.Fprintf(streams.Out, "seam: not constructed: %s\n", payload.Seam.Error)
		default:
			_, err = fmt.Fprintln(streams.Out, "seam: not constructed (no tracker backend configured)")
		}
		return err
	}
	return plumbing(cmd)
}

// seamStatus constructs the configured seam and describes it. The factory's
// error is captured rather than returned — see seamStatusJSON — and a factory
// that answers with no seam at all is reported the same way, so nothing here
// can type-assert on nil and call it a capability.
func seamStatus(providers *seam.Registry, config seam.SeamConfig) seamStatusJSON {
	provider, err := providers.Resolve(config.Backend, seam.FactoryInput{
		Repo:          config.Repo,
		Target:        config.Target,
		CanceledLabel: config.CanceledLabel,
		Project:       config.Project,
	})
	switch {
	case err != nil:
		return seamStatusJSON{Error: err.Error()}
	case provider == nil:
		return seamStatusJSON{Error: "the " + config.Backend + " factory returned no seam"}
	}
	_, readsState := provider.(seam.StateReader)
	_, readsContent := provider.(seam.ContentReader)
	return seamStatusJSON{
		// Deliver is constant-true for any constructed seam: the Seam
		// interface requires it, so there is nothing to type-assert.
		Constructed: true, Deliver: true,
		ReadState: readsState, ReadContent: readsContent,
	}
}

// -- propose ------------------------------------------------------------

// proposalJSON is the born outbox candidate, plus the one thing a caller has
// to do next. NextStep is spelled out rather than implied because a proposal
// that is never approved is the failure mode this whole path is shaped to make
// visible.
type proposalJSON struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	State          string          `json:"state"`
	Subject        string          `json:"subject"`
	Reference      string          `json:"reference,omitempty"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Payload        json.RawMessage `json:"payload"`
	NextStep       string          `json:"nextStep"`
}

func proposeCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "propose",
		Short: "queue operator-authored tracker work for approval",
		Long: "queue operator-authored tracker work for approval.\n\n" +
			"Each verb below writes exactly one queued outbox candidate — a candidate a person " +
			"wrote, rather than one a Matter's lifecycle produced. " + approvalSentence,
	}
	cmd.AddCommand(proposeCreateCommand(streams), proposeCommentCommand(streams), proposeStateCommand(streams))
	return plumbing(cmd)
}

// proposer is one propose verb's write: the args are the verb's own, and the
// returned entry is the queued candidate.
type proposer func(*cobra.Command, []string, *store.Store, store.Repo) (store.OutboxEntry, error)

func proposeRunE(streams *iostreams.Streams, propose proposer) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		s, repo, err := openRepo(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		entry, err := propose(cmd, args, s, repo)
		if err != nil {
			// A coded refusal (blank title, unknown disposition, ...) is a
			// clean rejection nothing was queued for, and passes through
			// untouched. Anything else travelled far enough that the write
			// may already have landed, so it gets one sentence of context —
			// the two are not distinguishable by type, only by whether they
			// carry a *wiperr.Error.
			var werr *wiperr.Error
			if errors.As(err, &werr) {
				return err
			}
			return fmt.Errorf("%w (the proposal may already be committed; check wip plumbing outbox list before retrying)", err)
		}
		payload := proposalJSON{
			ID: entry.ID, Kind: entry.Kind, State: entry.State, Subject: entry.Subject,
			Reference: entry.Ref, IdempotencyKey: entry.IdempotencyKey, Payload: entry.Payload,
			NextStep: "wip plumbing outbox approve " + entry.ID,
		}
		if cliflags.FromContext(cmd.Context()).JSON {
			return emit(streams, payload)
		}
		_, err = fmt.Fprintf(streams.Out, "proposed tracker %s as outbox %s (%s); approve with %s\n",
			payload.Kind, payload.ID, payload.State, payload.NextStep)
		return err
	}
}

func proposeCreateCommand(streams *iostreams.Streams) *cobra.Command {
	var title, detail string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "propose a new tracker item; nothing reaches the tracker until " + outboxApproveVerb + " and " + outboxFlushVerb,
		Long: "propose a new tracker item.\n\n" +
			"The proposal names no reference: the item does not exist until a provider answers, and " +
			"the reference arrives on the creation confirmation. " + approvalSentence,
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(&title, "title", "", "the proposed item's title")
	cmd.Flags().StringVar(&detail, "detail", "", "the proposed item's body (optional)")
	_ = cmd.MarkFlagRequired("title")
	cmd.RunE = proposeRunE(streams, func(cmd *cobra.Command, _ []string, s *store.Store, repo store.Repo) (store.OutboxEntry, error) {
		return writesurface.ProposeTrackerCreate(cmd.Context(), s, actor(cmd), repo.ID, strings.TrimSpace(title), detail)
	})
	return plumbing(cmd)
}

func proposeCommentCommand(streams *iostreams.Streams) *cobra.Command {
	var body string
	cmd := &cobra.Command{
		Use:   "comment <ref>",
		Short: "propose free body text on an existing tracker item; nothing reaches the tracker until " + outboxApproveVerb + " and " + outboxFlushVerb,
		Long: "propose free body text on an existing tracker item.\n\n" +
			refFormats + "\n\n" + approvalSentence,
		Args: cobra.ExactArgs(1),
	}
	cmd.Flags().StringVar(&body, "body", "", "the comment's body")
	_ = cmd.MarkFlagRequired("body")
	cmd.RunE = proposeRunE(streams, func(cmd *cobra.Command, args []string, s *store.Store, repo store.Repo) (store.OutboxEntry, error) {
		return writesurface.ProposeTrackerComment(cmd.Context(), s, actor(cmd), repo.ID, strings.TrimSpace(args[0]), body)
	})
	return plumbing(cmd)
}

func proposeStateCommand(streams *iostreams.Streams) *cobra.Command {
	var disposition string
	cmd := &cobra.Command{
		Use:   "state <ref>",
		Short: "propose one provider-neutral disposition on an existing tracker item; nothing reaches the tracker until " + outboxApproveVerb + " and " + outboxFlushVerb,
		Long: "propose one provider-neutral disposition on an existing tracker item.\n\n" +
			"The disposition is active, completed or canceled; the configured backend maps it to its " +
			"own state vocabulary. There is no lease to pass: the flush path reads the lease it " +
			"needs from the repo's own push records at delivery time.\n\n" +
			refFormats + "\n\n" + approvalSentence,
		Args: cobra.ExactArgs(1),
	}
	cmd.Flags().StringVar(&disposition, "disposition", "", "active, completed or canceled")
	_ = cmd.MarkFlagRequired("disposition")
	cmd.RunE = proposeRunE(streams, func(cmd *cobra.Command, args []string, s *store.Store, repo store.Repo) (store.OutboxEntry, error) {
		return writesurface.ProposeTrackerState(cmd.Context(), s, actor(cmd), repo.ID,
			strings.TrimSpace(args[0]), store.TrackerDisposition(strings.TrimSpace(disposition)))
	})
	return plumbing(cmd)
}
