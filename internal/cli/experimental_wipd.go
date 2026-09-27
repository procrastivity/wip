package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/procrastivity/wip/internal/cliflags"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wiperr"
)

func activateExperimentalWipd(cmd *cobra.Command, streams *iostreams.Streams, profileRoot string) error {
	client, err := wipd.Activate(cmd.Context(), profileRoot, "")
	if err != nil {
		return wiperr.New("transport.unavailable", "experimental local wipd activation is unavailable: "+err.Error())
	}
	defer func() { _ = client.Close() }()
	return renderExperimentalStatus(streams.Out, cliflags.FromContext(cmd.Context()).JSON, client.Status())
}

func renderExperimentalStatus(output interface{ Write([]byte) (int, error) }, jsonMode bool, status wipd.LocalStatus) error {
	if jsonMode {
		encoded, err := json.Marshal(status)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, string(encoded))
		return err
	}
	_, err := fmt.Fprintf(output, "wipd status: %s\nprofile_verified: %t\nprocess_ready: %t\nfixture_ready: %t\n",
		status.StatusCode, status.ProfileVerified, status.ProcessReady, status.FixtureReady)
	return err
}
