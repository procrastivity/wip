// Package content implements write-surface's four content verbs: `wip
// brief`, `wip workplan`, `wip body` (create-once) and `wip finding add`
// (append). Argument shape is not this Stage's call — it is the `agent-path`
// workplan's resolved decision, reused verbatim here: stdin (default) or
// `--file <path>` for the three create-once verbs, and a positional argument
// (default) plus stdin/`--file` for `wip finding add`.
package content

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/tiers"
	"github.com/procrastivity/wip/internal/writesurface"
)

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

// readFileOrStdin implements the create-once verbs' shape (agent-path
// step-01): `--file <path>` when given, else the whole of stdin. Neither
// present is a usage error (a plain, non-wiperr error, which chassis's exit
// mapping routes to code 2) rather than a silent empty write.
func readFileOrStdin(filePath string) ([]byte, error) {
	if filePath != "" {
		return os.ReadFile(filePath)
	}
	stat, err := os.Stdin.Stat()
	if err == nil && stat.Mode()&os.ModeCharDevice != 0 {
		return nil, fmt.Errorf("no input: pipe content on stdin, or pass --file <path>")
	}
	return io.ReadAll(os.Stdin)
}

func writeOnceCommand(use, short string, kind store.ContentKind) func(*iostreams.Streams) *cobra.Command {
	return func(streams *iostreams.Streams) *cobra.Command {
		var filePath string
		cmd := &cobra.Command{
			Use:   use,
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				flags := cliflags.FromContext(cmd.Context())
				data, err := readFileOrStdin(filePath)
				if err != nil {
					return err
				}
				s, repo, err := openRepo(cmd)
				if err != nil {
					return err
				}
				defer func() { _ = s.Close() }()

				n, err := writesurface.WriteOnce(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), repo.ID, args[0], kind, data)
				if err != nil {
					return err
				}
				if flags.JSON {
					b, err := json.Marshal(struct {
						Node  string `json:"node"`
						Kind  string `json:"kind"`
						Bytes int    `json:"bytes"`
					}{Node: n.ID, Kind: string(kind), Bytes: len(data)})
					if err != nil {
						return err
					}
					_, err = fmt.Fprintln(streams.Out, string(b))
					return err
				}
				_, err = fmt.Fprintf(streams.Out, "wrote %s (%d bytes) on %s\n", kind, len(data), args[0])
				return err
			},
		}
		cmd.Flags().StringVar(&filePath, "file", "", "read content from this path instead of stdin (D45: a one-time input, never round-tripped)")
		surface.Annotate(cmd, surface.Plumbing)
		return cmd
	}
}

// BriefCommand constructs `wip brief <locator>`.
func BriefCommand(streams *iostreams.Streams) *cobra.Command {
	return writeOnceCommand("brief <locator>", "write a node's Brief (create-once)", store.KindBrief)(streams)
}

// WorkplanCommand constructs `wip workplan <locator>`.
func WorkplanCommand(streams *iostreams.Streams) *cobra.Command {
	return writeOnceCommand("workplan <locator>", "write a node's Workplan (create-once)", store.KindWorkplan)(streams)
}

// BodyCommand constructs `wip body <locator>`.
func BodyCommand(streams *iostreams.Streams) *cobra.Command {
	return writeOnceCommand("body <locator>", "write a node's own prose body (create-once)", store.KindBody)(streams)
}

// FindingCommand constructs the `wip finding` parent command and its `add`
// verb.
func FindingCommand(streams *iostreams.Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "finding",
		Short: "append findings to a node",
	}
	cmd.AddCommand(findingAddCommand(streams))
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}

func findingAddCommand(streams *iostreams.Streams) *cobra.Command {
	var filePath string
	cmd := &cobra.Command{
		Use:   "add <locator> [text]",
		Short: "append one finding — a positional argument, or stdin/--file for a longer one",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cliflags.FromContext(cmd.Context())

			var data []byte
			if len(args) == 2 {
				if filePath != "" {
					return fmt.Errorf("pass either a positional finding or --file, not both")
				}
				data = []byte(args[1])
			} else {
				d, err := readFileOrStdin(filePath)
				if err != nil {
					return err
				}
				data = d
			}

			s, repo, err := openRepo(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()

			n, err := writesurface.AppendFinding(cmd.Context(), s, store.ActorFor(cliflags.FromContext(cmd.Context()).AsRole), repo.ID, args[0], data)
			if err != nil {
				return err
			}
			if flags.JSON {
				b, err := json.Marshal(struct {
					Node  string `json:"node"`
					Bytes int    `json:"bytes"`
				}{Node: n.ID, Bytes: len(data)})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(streams.Out, string(b))
				return err
			}
			_, err = fmt.Fprintf(streams.Out, "appended finding (%d bytes) on %s\n", len(data), args[0])
			return err
		},
	}
	cmd.Flags().StringVar(&filePath, "file", "", "read the finding from this path instead of a positional argument or stdin")
	surface.Annotate(cmd, surface.Plumbing)
	return cmd
}
