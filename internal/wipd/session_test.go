package wipd

import (
	"context"
	"errors"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
)

func TestCapabilitySelectionRequiresExactM2FeaturesAndVersionIntersection(t *testing.T) {
	registry := operation.NewRegistry()
	if err := registry.Register(operation.MatterCreateV1, func(context.Context, operation.Request) operation.Result {
		return operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{ID: "01M4F1XT4R3E00000000000001", Locator: "fixture-17", Title: "M4 Fixture"}}
	}); err != nil {
		t.Fatal(err)
	}

	client := capabilityHello{
		protocolMin:     protocolVersion{major: 1, minor: 0},
		protocolMax:     protocolVersion{major: 1, minor: 3},
		identitySchemas: []string{identitySchemaV1},
		operations: []operationCapability{{
			name:            "matter.create",
			versions:        []uint16{1},
			identitySchemas: []string{identitySchemaV1},
		}},
		storeSchemas: []string{storeSchemaV1},
		features:     []string{frameSchema},
	}

	selected, parameters, err := negotiateCapabilities(client, registry, false, false, false, false, false)
	if err != nil {
		t.Fatalf("negotiateCapabilities() error = %v", err)
	}
	if selected.selectedProtocol != (protocolVersion{major: 1, minor: 0}) {
		t.Fatalf("selected protocol = %+v, want greatest server-supported intersection 1.0", selected.selectedProtocol)
	}
	if len(selected.operations) != 1 || selected.operations[0].name != "matter.create" ||
		len(selected.operations[0].versions) != 1 || selected.operations[0].versions[0] != 1 ||
		len(selected.operations[0].identitySchemas) != 1 || selected.operations[0].identitySchemas[0] != identitySchemaV1 {
		t.Fatalf("selected operation capabilities = %+v, want exact matter.create@v1 / command/1", selected.operations)
	}
	if parameters != defaultSessionParameters(defaultExchanges) {
		t.Fatalf("session parameters = %+v, want protocol defaults %+v", parameters, defaultSessionParameters(defaultExchanges))
	}

	withoutFrame := client
	withoutFrame.features = nil
	if _, _, err := negotiateCapabilities(withoutFrame, registry, false, false, false, false, false); !errors.Is(err, errUnsupportedExtension) {
		t.Fatalf("missing required wipd.frame/1 error = %v, want unsupported extension", err)
	}

	withoutIdentity := client
	withoutIdentity.identitySchemas = nil
	if _, _, err := negotiateCapabilities(withoutIdentity, registry, false, false, false, false, false); !errors.Is(err, errUnsupportedExtension) {
		t.Fatalf("missing required wipd.command/1 error = %v, want unsupported extension", err)
	}

	noCommonMinor := client
	noCommonMinor.protocolMin = protocolVersion{major: 1, minor: 1}
	if _, _, err := negotiateCapabilities(noCommonMinor, registry, false, false, false, false, false); !errors.Is(err, errIncompatibleVersion) {
		t.Fatalf("no common protocol minor error = %v, want incompatible version", err)
	}

	wrongMajor := client
	wrongMajor.protocolMin = protocolVersion{major: 2, minor: 0}
	wrongMajor.protocolMax = protocolVersion{major: 2, minor: 2}
	if _, _, err := negotiateCapabilities(wrongMajor, registry, false, false, false, false, false); !errors.Is(err, errIncompatibleVersion) {
		t.Fatalf("incompatible protocol major error = %v, want incompatible version", err)
	}
}

func TestLifecycleFeaturesAreAdvertisedOnlyWhenConfigured(t *testing.T) {
	client := capabilityHello{
		protocolMin: protocolVersion{major: 1, minor: 0}, protocolMax: protocolVersion{major: 1, minor: 0},
		identitySchemas: []string{identitySchemaV1}, storeSchemas: []string{storeSchemaV1},
		features: []string{birthReleaseFeature, claimAcquireFeature, claimJournalCloseFeature, frameSchema},
	}
	withoutLifecycle, _, err := negotiateCapabilities(client, operation.NewRegistry(), false, false, false, false, false)
	if err != nil || !equalStrings(withoutLifecycle.features, []string{frameSchema}) {
		t.Fatalf("unconfigured lifecycle features = %v, %v; want frame-only", withoutLifecycle.features, err)
	}
	withRelease, _, err := negotiateCapabilities(client, operation.NewRegistry(), true, false, false, false, false)
	if err != nil || !equalStrings(withRelease.features, []string{birthReleaseFeature, frameSchema}) {
		t.Fatalf("configured release feature = %v, %v; want release only", withRelease.features, err)
	}
	withAcquire, _, err := negotiateCapabilities(client, operation.NewRegistry(), false, true, false, false, false)
	if err != nil || !equalStrings(withAcquire.features, []string{claimAcquireFeature, frameSchema}) {
		t.Fatalf("configured acquisition feature = %v, %v; want acquisition only", withAcquire.features, err)
	}
	withClose, _, err := negotiateCapabilities(client, operation.NewRegistry(), false, false, true, false, false)
	if err != nil || !equalStrings(withClose.features, []string{claimJournalCloseFeature, frameSchema}) {
		t.Fatalf("configured claim-journal close feature = %v, %v; want close only", withClose.features, err)
	}
	withBoth, _, err := negotiateCapabilities(client, operation.NewRegistry(), true, true, true, false, false)
	if err != nil || !equalStrings(withBoth.features, []string{birthReleaseFeature, claimAcquireFeature, claimJournalCloseFeature, frameSchema}) {
		t.Fatalf("configured lifecycle features = %v, %v; want all advertised", withBoth.features, err)
	}
}

func TestProductionServerAdvertisesNoUnregisteredLegacyHandler(t *testing.T) {
	server := NewServer()
	if got := registeredOperationCapabilities(server.registry); len(got) != 0 {
		t.Fatalf("production registered operation capabilities = %+v, want none", got)
	}
}
