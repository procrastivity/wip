package cli

import (
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/surface"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdseed"
)

func d115RepositoryCommand(streams *iostreams.Streams) *cobra.Command {
	repo := &cobra.Command{Use: "repo", Short: "repository identity operations"}
	surface.Annotate(repo, surface.Plumbing)
	create := &cobra.Command{Use: "create-local", Short: "create one new default domain and its initial Repo"}
	surface.Annotate(create, surface.Plumbing)
	repo.AddCommand(create)
	prepare := &cobra.Command{Use: "prepare", Args: cobra.NoArgs, Short: "pin a create-local signing request for owner action"}
	surface.Annotate(prepare, surface.Plumbing)
	prepare.Flags().String("grant-file", "", "owner-only canonical operator grant file")
	prepare.Flags().String("signing-request-out", "", "new owner-only signing-request output path")
	prepare.RunE = func(cmd *cobra.Command, _ []string) error {
		grantPath, _ := cmd.Flags().GetString("grant-file")
		out, _ := cmd.Flags().GetString("signing-request-out")
		if grantPath == "" || out == "" {
			return errors.New("repo create-local prepare requires --grant-file and --signing-request-out")
		}
		grantBytes, err := wipdseed.ReadD115OwnerFile(grantPath, 4096)
		if err != nil {
			return err
		}
		defer clear(grantBytes)
		client, err := d115Client(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		attempt, err := client.D115Prepare(cmd.Context(), encodeGrantArtifact(grantBytes))
		if err != nil {
			return err
		}
		if err = wipdseed.WriteD115OwnerFile(out, attempt.SigningRequest); err != nil {
			return err
		}
		_, err = fmt.Fprintf(streams.Out, "attempt_id: %s\nstate: %s\nsigning_request: %s\n", attempt.AttemptID, attempt.State, out)
		return err
	}
	importProof := &cobra.Command{Use: "import-proof", Args: cobra.NoArgs, Short: "import detached owner proof without submitting"}
	surface.Annotate(importProof, surface.Plumbing)
	importProof.Flags().String("attempt-id", "", "prepared create-local command ID")
	importProof.Flags().String("proof-file", "", "owner-only detached proof file")
	importProof.RunE = func(cmd *cobra.Command, _ []string) error {
		attemptID, _ := cmd.Flags().GetString("attempt-id")
		path, _ := cmd.Flags().GetString("proof-file")
		if attemptID == "" || path == "" {
			return errors.New("repo create-local import-proof requires --attempt-id and --proof-file")
		}
		proof, err := wipdseed.ReadD115OwnerFile(path, 2048)
		if err != nil {
			return err
		}
		defer clear(proof)
		client, err := d115Client(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		state, err := client.D115ImportProof(cmd.Context(), attemptID, proof)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(streams.Out, "attempt_id: %s\nstate: %s\n", state.AttemptID, state.State)
		return err
	}
	submit := &cobra.Command{Use: "submit", Args: cobra.NoArgs, Short: "explicitly submit an imported create-local attempt"}
	surface.Annotate(submit, surface.Plumbing)
	submit.Flags().String("attempt-id", "", "prepared create-local command ID")
	submit.RunE = func(cmd *cobra.Command, _ []string) error {
		attemptID, _ := cmd.Flags().GetString("attempt-id")
		if attemptID == "" {
			return errors.New("repo create-local submit requires --attempt-id")
		}
		client, err := d115Client(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		receipt, err := client.D115Submit(cmd.Context(), attemptID)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(streams.Out, "domain_id: %s\nrepo_id: %s\ncommand_id: %s\n", receipt.DomainID, receipt.RepoID, receipt.CommandID)
		return err
	}
	status := &cobra.Command{Use: "status", Args: cobra.NoArgs, Short: "inspect a durable create-local attempt"}
	surface.Annotate(status, surface.Plumbing)
	status.Flags().String("attempt-id", "", "prepared create-local command ID")
	status.RunE = func(cmd *cobra.Command, _ []string) error {
		attemptID, _ := cmd.Flags().GetString("attempt-id")
		if attemptID == "" {
			return errors.New("repo create-local status requires --attempt-id")
		}
		client, err := d115Client(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		state, err := client.D115Status(cmd.Context(), attemptID)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(streams.Out, "attempt_id: %s\nstate: %s\n", state.AttemptID, state.State)
		return err
	}
	cancel := &cobra.Command{Use: "cancel", Args: cobra.NoArgs, Short: "cancel a never-submitted create-local attempt"}
	surface.Annotate(cancel, surface.Plumbing)
	cancel.Flags().String("attempt-id", "", "prepared create-local command ID")
	cancel.RunE = func(cmd *cobra.Command, _ []string) error {
		attemptID, _ := cmd.Flags().GetString("attempt-id")
		if attemptID == "" {
			return errors.New("repo create-local cancel requires --attempt-id")
		}
		client, err := d115Client(cmd)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		state, err := client.D115Cancel(cmd.Context(), attemptID)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(streams.Out, "attempt_id: %s\nstate: %s\n", state.AttemptID, state.State)
		return err
	}
	create.AddCommand(prepare, importProof, submit, status, cancel)
	return repo
}

func d115Client(cmd *cobra.Command) (*wipd.Client, error) {
	root := cmd.Root()
	profile, err := root.PersistentFlags().GetString("experimental-wipd-profile")
	if err != nil || profile == "" {
		return nil, errors.New("D115 requires --experimental-wipd-profile; no legacy store fallback was attempted")
	}
	return wipd.Activate(cmd.Context(), profile, "")
}

func encodeGrantArtifact(data []byte) string { return base64.RawURLEncoding.EncodeToString(data) }
