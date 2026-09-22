// Package matter implements `wip plumbing matter create` — write-surface's birth verb
// for the addressable root (MODEL §1, D2).
package matter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/wiperr"
	"github.com/procrastivity/wip/internal/writesurface"
)

// Command constructs the `wip plumbing matter` parent command and its `create` verb.
func Command(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "matter",
		Short: "create matters — the addressable root",
	}
	cmd.AddCommand(createCommand(streams))
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func createCommand(streams *iostreams.Streams) *cobra.Command {
	var title, locator string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "birth a matter, entering Planned",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags := cliflags.FromContext(cmd.Context())

			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			s, err := tiers.OpenStore()
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			repo, err := writesurface.CurrentRepo(cmd.Context(), s, dir)
			if err != nil {
				return err
			}

			registry := operation.NewRegistry()
			if err := registry.Register(operation.MatterCreateV1, matterCreateHandler(s)); err != nil {
				return err
			}
			result := registry.Dispatch(cmd.Context(), operation.Request{
				Operation: operation.MatterCreateV1.Metadata().Operation,
				Actor:     operation.Actor(store.ActorFor(flags.AsRole)),
				Context:   operation.Context{Repo: repo.ID},
				Input:     operation.MatterCreateInput{Title: title, Locator: locator},
				Blobs:     []operation.BlobInput{},
			})
			output, err := matterCreateOutput(result)
			if err != nil {
				return err
			}

			if flags.JSON {
				b, err := json.Marshal(struct {
					ID      string `json:"id"`
					Locator string `json:"locator"`
					Title   string `json:"title"`
				}{ID: output.ID, Locator: output.Locator, Title: output.Title})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "created matter %s (%s)\n", output.Locator, output.ID)
			return err
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "the matter's title")
	cmd.Flags().StringVar(&locator, "locator", "", "the matter's locator (default: derived from the title)")
	_ = cmd.MarkFlagRequired("title")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func matterCreateHandler(s *store.Store) operation.Handler {
	return func(ctx context.Context, request operation.Request) operation.Result {
		input := request.Input.(operation.MatterCreateInput)
		node, err := writesurface.CreateMatter(
			ctx,
			s,
			store.Actor(request.Actor),
			request.Context.Repo,
			input.Title,
			input.Locator,
		)
		if err != nil {
			return matterCreateFailure(err)
		}
		return operation.Result{
			Code: operation.ResultSucceeded,
			Output: operation.MatterCreateOutput{
				ID:      node.ID,
				Locator: node.Locator,
				Title:   node.Title,
			},
		}
	}
}

func matterCreateFailure(err error) operation.Result {
	var structured *wiperr.Error
	if !errors.As(err, &structured) {
		return operation.Result{
			Code: operation.ResultFailed,
			Problem: &operation.Problem{
				Code:    operation.ProblemExecutionFailed,
				Message: err.Error(),
			},
		}
	}

	code := operation.ResultRejected
	if strings.HasPrefix(structured.Code, "refusal.") {
		code = operation.ResultRefused
	} else if strings.HasPrefix(structured.Code, "internal.") {
		code = operation.ResultFailed
	}
	return operation.Result{
		Code: code,
		Problem: &operation.Problem{
			Code:    operation.ProblemCode(structured.Code),
			Message: structured.Message,
		},
	}
}

func matterCreateOutput(result operation.Result) (operation.MatterCreateOutput, error) {
	if result.Code == operation.ResultSucceeded {
		return result.Output.(operation.MatterCreateOutput), nil
	}
	if result.Problem == nil {
		return operation.MatterCreateOutput{}, errors.New("matter.create returned no problem")
	}
	if result.Problem.Code == operation.ProblemExecutionFailed || result.Problem.Code == operation.ProblemInvalidResult {
		// Preserve the legacy adapter's treatment of unexpected execution errors:
		// only structured wip errors enter the structured error renderer.
		return operation.MatterCreateOutput{}, errors.New(result.Problem.Message)
	}
	return operation.MatterCreateOutput{}, wiperr.New(string(result.Problem.Code), result.Problem.Message)
}
