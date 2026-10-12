package wipdauthority_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/procrastivity/wip/internal/wipdauthority"
	"github.com/procrastivity/wip/internal/wipdseed"
	"github.com/procrastivity/wip/internal/wipdwire"
)

type m5CrossContainerProjectionProfile struct {
	Schema                  string `json:"schema"`
	Origin                  string `json:"origin"`
	DomainID                string `json:"domain_id"`
	Epoch                   uint64 `json:"authority_epoch"`
	RepoID                  string `json:"repo_id"`
	OwnerRootSPKI           string `json:"owner_root_spki"`
	AuthoritySPKIPin        string `json:"authority_spki_pin"`
	AuthorityCertificateDER []byte `json:"authority_certificate_der"`
	OwnerRootPublicKey      []byte `json:"owner_root_public_key"`
	ArtifactKeyCertificate  []byte `json:"artifact_key_certificate"`
	ClientStateDirectory    string `json:"client_state_directory"`
}

type m5CrossContainerInstalledProjection struct {
	EnvironmentID      string                 `json:"environment_id"`
	Prefix             wipdwire.PrefixAnchor  `json:"prefix"`
	ManifestDigest     string                 `json:"manifest_digest"`
	EventRecords       []wipdwire.EventRecord `json:"event_records"`
	MatterProjections  []json.RawMessage      `json:"matter_projections"`
	StepProjections    []json.RawMessage      `json:"step_projections"`
	ContentProjections []json.RawMessage      `json:"content_projections"`
}

type m5CrossContainerInstalledProjections struct {
	Schema       string                              `json:"schema"`
	EnvironmentA m5CrossContainerInstalledProjection `json:"environment_a"`
	EnvironmentB m5CrossContainerInstalledProjection `json:"environment_b"`
}

// TestM5CrossContainerClientProjectionEvidence is launched as a subprocess by
// the M5 process acceptance test after both journals have been reopened. It
// exercises the real client pull/fold path from each installed identity.
func TestM5CrossContainerClientProjectionEvidence(t *testing.T) {
	profileA := os.Getenv("WIP_M5_CROSS_CONTAINER_PROJECTION_PROFILE_A")
	stateA := os.Getenv("WIP_M5_CROSS_CONTAINER_PROJECTION_STATE_A")
	profileB := os.Getenv("WIP_M5_CROSS_CONTAINER_PROJECTION_PROFILE_B")
	stateB := os.Getenv("WIP_M5_CROSS_CONTAINER_PROJECTION_STATE_B")
	output := os.Getenv("WIP_M5_CROSS_CONTAINER_PROJECTION_OUTPUT")
	if profileA == "" || stateA == "" || profileB == "" || stateB == "" || output == "" {
		t.Skip("cross-container projection evidence runs only inside the isolated M5 acceptance")
	}
	if filepath.Clean(stateA) == filepath.Clean(stateB) {
		t.Fatal("cross-container projection evidence requires separate Environment client-state directories")
	}
	evidence := m5CrossContainerInstalledProjections{
		Schema:       "wipd.m5-cross-container-installed-projections/1",
		EnvironmentA: m5PullCrossContainerProjections(t, profileA, stateA),
		EnvironmentB: m5PullCrossContainerProjections(t, profileB, stateB),
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(output, encoded, 0o600); err != nil {
		t.Fatalf("write cross-container client projection evidence: %v", err)
	}
}

func m5PullCrossContainerProjections(t *testing.T, profilePath, stateDirectory string) m5CrossContainerInstalledProjection {
	t.Helper()
	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read connected authority profile %s: %v", profilePath, err)
	}
	var config m5CrossContainerProjectionProfile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&config); err != nil || decoder.Decode(new(any)) != io.EOF ||
		config.Schema != "wipd.connected-authority-profile/2" || config.ClientStateDirectory != stateDirectory ||
		len(config.AuthorityCertificateDER) == 0 {
		t.Fatalf("invalid connected authority profile %s: %v", profilePath, err)
	}
	profile, err := wipdauthority.NewProfile(config.Origin, config.DomainID, config.Epoch,
		config.AuthoritySPKIPin, config.OwnerRootSPKI)
	if err != nil {
		t.Fatalf("construct trusted M5 client profile: %v", err)
	}
	profile, err = profile.WithM5LabRepoID(config.RepoID)
	if err != nil {
		t.Fatalf("bind M5 client Repo: %v", err)
	}
	rootCertificate, err := x509.ParseCertificate(config.AuthorityCertificateDER)
	if err != nil {
		t.Fatalf("parse authority trust root: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(rootCertificate)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := wipdseed.PullAndInstall(ctx, profile, roots, stateDirectory)
	if err != nil {
		t.Fatalf("authenticated client pull from %s: %v", config.Origin, err)
	}
	defer clear(state.PrivateKeyPKCS8)
	if state.Schema != "wipd.m5-client-state/1" || state.DomainID != config.DomainID || state.RepoID != config.RepoID ||
		state.Epoch != config.Epoch || state.EnvironmentID == "" {
		t.Fatal("authenticated client pull returned an identity-mismatched state")
	}
	return m5CrossContainerInstalledProjection{
		EnvironmentID: state.EnvironmentID, Prefix: state.Prefix, ManifestDigest: state.ManifestDigest,
		EventRecords: state.EventRecords, MatterProjections: state.Projections,
		StepProjections: state.StepProjections, ContentProjections: state.ContentProjections,
	}
}
