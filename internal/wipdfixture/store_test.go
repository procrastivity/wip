package wipdfixture_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/procrastivity/wip/internal/operation"
	"github.com/procrastivity/wip/internal/wipdfixture"
	"github.com/procrastivity/wip/internal/wipdprofile"
)

func TestTestOnlyRegistryHandlerPersistsTypedAsymmetricFixture(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "xdg-unrelated"))
	t.Setenv("WIP_DB_PATH", filepath.Join(base, "legacy-unrelated", "wip.db"))
	profile, err := wipdprofile.Resolve(filepath.Join(base, "fixture-root"))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	store, err := wipdfixture.Open(profile)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	rootInfo, err := os.Stat(profile.Root)
	if err != nil {
		t.Fatal(err)
	}
	if rootInfo.Mode().Perm() != 0o700 {
		t.Fatalf("fixture root permissions = %04o, want 0700", rootInfo.Mode().Perm())
	}
	databasePath := filepath.Join(profile.Root, "m4-test-fixture.sqlite")
	databaseInfo, err := os.Stat(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if databaseInfo.Mode().Perm() != 0o600 {
		t.Fatalf("fixture database permissions = %04o, want 0600", databaseInfo.Mode().Perm())
	}

	input := operation.MatterCreateInput{Title: "M4 fixture result", Locator: "unequal-fixture-key"}
	const fixtureID = "01M4FIXTURE0000000000000001"
	wantOutput := operation.MatterCreateOutput{
		ID:      fixtureID,
		Locator: "unequal-fixture-key",
		Title:   "M4 fixture result",
	}
	registry := operation.NewRegistry()
	if err := registry.Register(operation.MatterCreateV1, func(ctx context.Context, request operation.Request) operation.Result {
		semanticInput, ok := request.Input.(operation.MatterCreateInput)
		if !ok {
			return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{
				Code: operation.ProblemExecutionFailed, Message: "unexpected test fixture input type",
			}}
		}
		fixtureRecord := wipdfixture.Record{
			ID:    fixtureID,
			Key:   semanticInput.Locator,
			Value: semanticInput.Title,
		}
		if err := store.Put(ctx, fixtureRecord); err != nil {
			return operation.Result{Code: operation.ResultFailed, Problem: &operation.Problem{
				Code: operation.ProblemExecutionFailed, Message: err.Error(),
			}}
		}
		return operation.Result{Code: operation.ResultSucceeded, Output: operation.MatterCreateOutput{
			ID: fixtureRecord.ID, Locator: semanticInput.Locator, Title: semanticInput.Title,
		}}
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	result := registry.Dispatch(context.Background(), operation.Request{
		Operation: operation.MatterCreateV1.Metadata().Operation,
		Actor:     operation.Actor("human"),
		Context:   operation.Context{Repo: "fixture-repo"},
		Input:     input,
		Blobs:     []operation.BlobInput{},
	})
	if result.Code != operation.ResultSucceeded || result.Problem != nil {
		t.Fatalf("Dispatch() result = %+v, want typed success", result)
	}
	output, ok := result.Output.(operation.MatterCreateOutput)
	if !ok || output != wantOutput {
		t.Fatalf("Dispatch() output = %#v, want independently specified %#v", result.Output, wantOutput)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := wipdfixture.Open(profile)
	if err != nil {
		t.Fatalf("reopen fixture: %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened fixture: %v", err)
		}
	}()
	gotRecord, err := reopened.Get(context.Background(), "unequal-fixture-key")
	if err != nil {
		t.Fatalf("Get() after reopen: %v", err)
	}
	wantRecord := wipdfixture.Record{ID: fixtureID, Key: "unequal-fixture-key", Value: "M4 fixture result"}
	if gotRecord != wantRecord {
		t.Fatalf("persisted fixture record = %+v, want %+v", gotRecord, wantRecord)
	}
}
