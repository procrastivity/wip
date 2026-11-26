// Package transfer exposes explicit local-file export and import operations.
package transfer

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/localtransfer"
	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	archive "github.com/procrastivity/wip/internal/transfer"
	"github.com/procrastivity/wip/internal/wiperr"
)

// ExportCommand builds the canonical offline export command family.
func ExportCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use: "export", Short: "browse a frozen recovery copy and export selected work offline",
		Long: "Export selected Matters and backlog entries without a checkout or Cloud account. " +
			"Use an untouched backup plus a separate frozen recovery copy with its sibling blobs. " +
			"Stop all writers during capture. Inspection and planning migrate only private scratch copies.",
	}
	var source, repo string
	list := &cobra.Command{
		Use: "list", Short: "list source Repos, or all Matter/backlog roots in an explicit Repo", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, _, cleanup, err := localtransfer.Inspect(cmd.Context(), source)
			if err != nil {
				return invalid(err)
			}
			defer cleanup()
			if repo == "" {
				repos, err := s.Repos(cmd.Context())
				if err != nil {
					return invalid(err)
				}
				return render(cmd, streams, "Source Repos (no original store opened):", repos)
			}
			inventory, err := s.TransferInventory(cmd.Context(), repo)
			if err != nil {
				return invalid(err)
			}
			return render(cmd, streams, "Source inventory, including terminal backlog:", inventory)
		},
	}
	list.Flags().StringVar(&source, "source-db", "", "explicit existing recovery-copy DB; sibling blobs are captured too")
	list.Flags().StringVar(&repo, "repo", "", "source native Repo ID; omit to list Repos")
	require(list, "source-db")

	var planSource, planRepo, planOut string
	var matters, backlog []string
	plan := &cobra.Command{
		Use: "plan", Short: "capture selected roots and their required Matter/content closure for review", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := localtransfer.PlanExport(cmd.Context(), planSource, planRepo, archive.Selection{Matters: matters, Backlog: backlog}, planOut)
			if err != nil {
				return invalid(err)
			}
			return render(cmd, streams, "Export plan saved with a private sibling snapshot. Preserve the lineage file for future exports:", p)
		},
	}
	plan.Flags().StringVar(&planSource, "source-db", "", "explicit frozen recovery-copy database")
	plan.Flags().StringVar(&planRepo, "repo", "", "explicit source native Repo ID")
	plan.Flags().StringSliceVar(&matters, "matter", nil, "selected Matter ID (repeatable or comma-separated)")
	plan.Flags().StringSliceVar(&backlog, "backlog", nil, "selected backlog ID, including planned entries (repeatable)")
	plan.Flags().StringVar(&planOut, "out", "", "new private review-plan file; never overwritten")
	require(plan, "source-db", "repo", "out")
	var writePlan, writeOut string
	write := &cobra.Command{
		Use: "write", Short: "write the reviewed captured snapshot to a portable archive", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := localtransfer.WriteExport(writePlan, writeOut)
			if err != nil {
				return invalid(err)
			}
			return render(cmd, streams, "Portable archive written; no network action:", struct {
				Path   string `json:"path"`
				Digest string `json:"digest"`
			}{writeOut, p.Digest})
		},
	}
	write.Flags().StringVar(&writePlan, "plan", "", "reviewed local export-plan file")
	write.Flags().StringVar(&writeOut, "out", "", "new portable archive file; never overwritten")
	require(write, "plan", "out")
	cmd.AddCommand(list, plan, write)
	annotate(cmd)
	return cmd
}

// ImportCommand builds the canonical offline import command family.
func ImportCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use: "import", Short: "inspect and import portable work into an explicit existing Repo offline",
		Long: "Import never pairs, resumes, retargets, starts execution, queues tracker delivery or performs HTTP. " +
			"Supported imported work follows the target's existing pairing on its next ordinary sync. " +
			"Unpaired stays local; paused stays paused. Preserve both original stores and test on copies first.",
	}
	var content string
	inspect := &cobra.Command{
		Use: "inspect <archive>", Short: "validate an archive without a destination, or read one content segment", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			snapshot, err := archive.Read(args[0])
			if err != nil {
				return invalid(err)
			}
			if content != "" {
				for _, segment := range snapshot.Contents {
					if segment.ID == content {
						if cliflags.FromContext(cmd.Context()).JSON {
							return render(cmd, streams, "", struct {
								ID    string `json:"id"`
								Bytes []byte `json:"bytes"`
							}{segment.ID, segment.Data})
						}
						_, err := streams.Out.Write(segment.Data)
						return err
					}
				}
				return invalid(fmt.Errorf("content %s not found in archive", content))
			}
			return render(cmd, streams, "Validated snapshot-only archive; exact activity history is not included:", snapshot)
		},
	}
	inspect.Flags().StringVar(&content, "content", "", "read exact segment bytes by source content ID (JSON bytes are base64)")
	var target, repo, out string
	var renames []string
	var allowMigration bool
	plan := &cobra.Command{
		Use: "plan <archive>", Short: "preview mappings, collisions and current pairing on a disposable target copy", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mapping := map[string]string{}
			for _, rename := range renames {
				key, value, ok := strings.Cut(rename, "=")
				if !ok || key == "" || value == "" || mapping[key] != "" {
					return invalid(fmt.Errorf("--rename must be a unique SOURCE_MATTER_ID=target-locator"))
				}
				mapping[key] = value
			}
			p, err := localtransfer.PlanImport(cmd.Context(), args[0], target, repo, out, mapping, allowMigration)
			if err != nil {
				return invalid(err)
			}
			return render(cmd, streams, p.Sync.Notice+" Review the saved plan before apply:", p)
		},
	}
	plan.Flags().StringVar(&target, "target-db", "", "explicit existing destination DB; no default host-store fallback")
	plan.Flags().StringVar(&repo, "repo", "", "explicit existing target native Repo ID")
	plan.Flags().StringVar(&out, "out", "", "new private import-plan file; never overwritten")
	plan.Flags().StringSliceVar(&renames, "rename", nil, "explicit source Matter ID=target locator mapping")
	plan.Flags().BoolVar(&allowMigration, "allow-migration", false, "acknowledge target schema upgrade on apply (backup retained; migration commits separately)")
	require(plan, "target-db", "repo", "out")
	var applyPlan string
	apply := &cobra.Command{
		Use: "apply", Short: "apply the reviewed plan atomically after preserving a destination backup", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			actor := store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole)
			result, err := localtransfer.ApplyImport(cmd.Context(), actor, applyPlan)
			if err != nil {
				return invalid(err)
			}
			return render(cmd, streams, result.Notice, result)
		},
	}
	apply.Flags().StringVar(&applyPlan, "plan", "", "reviewed local import-plan file; retries reconcile its original operation")
	require(apply, "plan")
	cmd.AddCommand(inspect, plan, apply)
	annotate(cmd)
	return cmd
}

// PorcelainExportCommand registers the human-facing alias of plumbing export.
func PorcelainExportCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := ExportCommand(streams)
	alias(cmd, "plumbing export")
	return cmd
}

// PorcelainImportCommand registers the human-facing alias of plumbing import.
func PorcelainImportCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := ImportCommand(streams)
	alias(cmd, "plumbing import")
	return cmd
}

func alias(cmd *cobra.Command, canonical string) {
	for _, child := range cmd.Commands() {
		manifest.SetAliasOf(child, canonical+" "+child.Name())
	}
}

func annotate(cmd *cobra.Command) {
	for _, child := range cmd.Commands() {
		surface.Annotate(child, surface.Plumbing)
	}
}

func require(cmd *cobra.Command, names ...string) {
	for _, name := range names {
		if err := cmd.MarkFlagRequired(name); err != nil {
			panic(err)
		}
	}
}

func invalid(err error) error { return wiperr.New("transfer.invalid", err.Error()) }

func render(cmd *cobra.Command, streams *iostreams.Streams, heading string, value any) error {
	encoder := json.NewEncoder(streams.Out)
	if !cliflags.FromContext(cmd.Context()).JSON {
		if _, err := fmt.Fprintln(streams.Out, heading); err != nil {
			return err
		}
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(value)
}
