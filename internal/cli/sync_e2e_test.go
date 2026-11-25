package cli_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/procrastivity/wip/internal/store"
	"github.com/procrastivity/wip/internal/writesurface"
)

// Isolate the native OS config location, not only Linux's XDG override.
func syncConfigEnv(home string) []string {
	return []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home}
}

func syncCredentialFile(home string) string {
	if runtime.GOOS == "darwin" {
		home = filepath.Join(home, "Library", "Application Support")
	}
	return filepath.Join(home, "wip", "sync", "credentials.json")
}

func TestSyncCredentialSafetyAndOfflinePreview(t *testing.T) {
	dir := newGitRepo(t, "sync-safety")
	db := filepath.Join(t.TempDir(), "ordinary.db")
	config := t.TempDir()
	env := append(syncConfigEnv(config), "WIP_DB_PATH="+db)
	if r := runInDir(t, dir, env, "init"); r.exitCode != 0 {
		t.Fatal(r.stderr)
	}
	for _, args := range [][]string{{"status"}, {"run", "--dry-run"}, {"pair", "--dry-run", "--server", "https://not-contacted.example", "--org", "explicit-org", "--server-repo", "explicit-repo"}} {
		r := runInDir(t, dir, env, append([]string{"--json", "plumbing", "sync"}, args...)...)
		if r.exitCode != 0 || strings.Contains(r.stdout, `"Paired":true`) {
			t.Fatal(r)
		}
	}
	read, err := sql.Open("sqlite", "file:"+db+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	var n int
	if err := read.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND (name LIKE 'experiment_%' OR name LIKE 'exchange_%')`).Scan(&n); err != nil || n != 0 {
		t.Fatal("offline preview initialized optional tables", n, err)
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()
	password := filepath.Join(t.TempDir(), "password")
	const secret = "protected test-only secret"
	if err := os.WriteFile(password, []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"--json", "plumbing", "sync", "login", "--server", server.URL, "--email", "test@example.test", "--password-file", password}
	r := runInDir(t, dir, env, args...)
	if r.exitCode != 1 || !strings.Contains(r.stderr, "private regular file") || strings.Contains(r.stderr+r.stdout, secret) || requests.Load() != 0 {
		t.Fatal("insecure password file was not refused before network", r)
	}
	if err := os.Chmod(password, 0o600); err != nil {
		t.Fatal(err)
	}
	r = runInDir(t, dir, env, args...)
	if r.exitCode != 1 || !strings.Contains(r.stderr, "HTTP 302") || redirected.Load() != 0 || requests.Load() != 1 {
		t.Fatal("login followed redirect", r)
	}
	if _, err := os.Stat(syncCredentialFile(config)); !os.IsNotExist(err) {
		t.Fatal("failed login saved credentials", err)
	}
	inside := append(syncConfigEnv(filepath.Join(dir, "config")), "WIP_DB_PATH="+db)
	r = runInDir(t, dir, inside, args...)
	if r.exitCode != 1 || !strings.Contains(r.stderr, "outside the checkout") || requests.Load() != 1 {
		t.Fatal("credentials permitted inside checkout", r)
	}
	for _, intermediate := range []bool{false, true} {
		t.Run(fmt.Sprintf("config-ancestor-symlink=%t", intermediate), func(t *testing.T) {
			outside := t.TempDir()
			target := filepath.Join(dir, "config")
			link := filepath.Join(outside, "alias")
			home := link
			if intermediate {
				link = filepath.Join(outside, "wip")
				target = filepath.Join(target, "wip")
				if runtime.GOOS == "darwin" {
					link = filepath.Join(outside, "Library")
					target = filepath.Join(dir, "config", "Library")
				}
				home = outside
			}
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			env := append(syncConfigEnv(home), "WIP_DB_PATH="+db)
			result := runInDir(t, dir, env, args...)
			if result.exitCode != 1 || !strings.Contains(result.stderr, "outside the checkout") || requests.Load() != 1 {
				t.Fatal("symlinked credential destination was not refused before HTTP", result)
			}
			if _, err := os.Stat(filepath.Dir(syncCredentialFile(filepath.Join(dir, "config")))); !os.IsNotExist(err) {
				t.Fatal("refused login created credential directories inside checkout", err)
			}
		})
	}
}

func TestSyncScopedOfflineRecheck(t *testing.T) {
	ctx := context.Background()
	dir := newGitRepo(t, "source")
	db := filepath.Join(t.TempDir(), "source.db")
	for _, args := range [][]string{{"init"}, {"plumbing", "matter", "create", "--title", "Shared"}} {
		if r := runInDir(t, dir, []string{"WIP_DB_PATH=" + db}, args...); r.exitCode != 0 {
			t.Fatal(r)
		}
	}
	source, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	events, err := source.Events(ctx)
	if err != nil || len(events) == 0 {
		t.Fatal("seed", err)
	}
	repo := events[0].Repo
	stage, err := writesurface.CreateStage(ctx, source, store.ActorHuman, repo, "shared", "Stage")
	if err != nil {
		t.Fatal(err)
	}
	step, err := writesurface.CreateStep(ctx, source, store.ActorHuman, repo, "shared/"+stage.Locator, "Step")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	copyDB := filepath.Join(t.TempDir(), "destination.db")
	if err := os.WriteFile(copyDB, data, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err = store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.EnableSharedStoreExperiment(ctx); err != nil {
		t.Fatal(err)
	}
	var sources []store.ExperimentSource
	for _, locator := range []string{"shared/" + stage.Locator, "shared/" + step.Locator} {
		started, err := writesurface.Start(ctx, source, store.ActorHuman, repo, locator)
		if err != nil {
			t.Fatal(err)
		}
		original, err := source.ExperimentExport(ctx, "portable-source", started[0].Correlation)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, original)
	}
	destination, err := store.Open(copyDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = destination.Close() })
	if _, err := destination.PairExchange(ctx, repo, "https://offline.example/api/organizations/old/repos/old"); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{1, 0} {
		d, err := destination.ExperimentImportBound(ctx, sources[i], repo)
		if err != nil || (i == 1 && d.Disposition != "pending") || (i == 0 && d.Disposition != "integrated") {
			t.Fatal("dependency fixture", d, err)
		}
	}
	if err := destination.UnpairExchange(ctx, repo); err != nil {
		t.Fatal(err)
	}
	before, err := destination.ExchangeStatus(ctx, repo)
	if err != nil || before.Waiting != 1 || before.Active {
		t.Fatal("unpaired A needs one waiting child", before, err)
	}
	// The retained pending child waits on its unarrived parent — a dependency
	// wait, never a byte wait — and the categories mirror the retained
	// counters exactly, queued included.
	if before.Categories.Waiting != 1 || before.Categories.WaitingDependencies != 1 || before.Categories.WaitingBytes != 0 {
		t.Fatal("pending child must surface as one dependency wait, never a byte wait", before.Categories)
	}
	if before.Categories.Queued != len(before.PendingUpload) || before.Categories.Acknowledged != len(before.Retained) ||
		before.Categories.Received != int(before.Downloaded) || before.Categories.Applied != before.Integrated ||
		before.Categories.Conflicts != before.Conflicts || before.Categories.Excluded != len(before.Unsupported) {
		t.Fatal("categories must mirror the retained counters", before.Categories)
	}
	prior, err := destination.EventsOfSubject(ctx, step.ID)
	if err != nil {
		t.Fatal(err)
	}
	other := newGitRepo(t, "unrelated")
	home := t.TempDir()
	env := append(syncConfigEnv(home), "WIP_DB_PATH="+copyDB)
	if r := runInDir(t, other, env, "init"); r.exitCode != 0 {
		t.Fatal(r)
	}
	command := func(ok bool, args ...string) string {
		t.Helper()
		r := runInDir(t, other, env, append([]string{"--json", "plumbing", "sync"}, args...)...)
		if (r.exitCode == 0) != ok {
			t.Fatal(args, r)
		}
		return r.stdout
	}
	var selected store.ExchangeStatus
	if err := json.Unmarshal([]byte(command(true, "status")), &selected); err != nil || selected.Binding.LocalRepo == repo {
		t.Fatal("B must be independently initialized in A's database", err)
	}
	// Only the transport neighbor is a fake here. Actual Laravel acceptance is
	// separate; this fast control exercises the real CLI's Repo selection.
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer portable-dummy-token" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/api/me" {
			_, _ = w.Write([]byte(`{"id":1}`))
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/origins") {
			w.WriteHeader(201)
			return
		}
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/changes") {
			downloads.Add(1)
			_, _ = w.Write([]byte(`{"changes":[],"cursor":0}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer server.Close()
	if _, err := destination.PairExchange(ctx, selected.Binding.LocalRepo, server.URL+"/api/organizations/portable/repos/portable"); err != nil {
		t.Fatal(err)
	}
	credential := syncCredentialFile(home)
	if err := os.MkdirAll(filepath.Dir(credential), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"Server": server.URL, "Token": "portable-dummy-token"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credential, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// `run` applies retained decisions with no preview, acknowledgement or
	// activity inspection first — inspection is optional, never a gate.
	for _, args := range [][]string{{"recheck"}, {"run"}, {"recheck", sources[1].Actions[0].Event.ID}} {
		command(len(args) == 1, args...)
		after, err := destination.ExchangeStatus(ctx, repo)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("B changed A's retained decisions", after, err)
		}
		afterEvents, err := destination.EventsOfSubject(ctx, step.ID)
		if err != nil || !reflect.DeepEqual(prior, afterEvents) {
			t.Fatal("B applied A's business effects", err)
		}
	}
	if downloads.Load() != 1 {
		t.Fatal("CLI run never reached transport")
	}
	server.Close()
	if err := os.Remove(credential); err != nil {
		t.Fatal(err)
	}
	command(true, "recheck", "--repo", repo)
	after, err := destination.ExchangeStatus(ctx, repo)
	if err != nil || after.Waiting != 0 || after.Integrated != before.Integrated+1 {
		t.Fatal("explicit A did not recover offline without credentials", after, err)
	}
	if after.Categories.Waiting != 0 || after.Categories.WaitingBytes != 0 || after.Categories.WaitingDependencies != 0 ||
		after.Categories.Applied != after.Integrated {
		t.Fatal("applied child must leave every waiting bucket and land in applied", after.Categories)
	}
	node, err := destination.Node(ctx, step.ID)
	if err != nil || node.Lifecycle != store.InProgress {
		t.Fatal("offline recovery did not apply child", node, err)
	}
	beforeRetry, err := destination.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	command(true, "recheck", "--repo", repo)
	retry, err := destination.ExchangeStatus(ctx, repo)
	if err != nil || !reflect.DeepEqual(after, retry) {
		t.Fatal("offline retry was not inert", retry, err)
	}
	retryEvents, err := destination.Events(ctx)
	if err != nil || !reflect.DeepEqual(beforeRetry, retryEvents) {
		t.Fatal("offline retry changed native event history", err)
	}
}

// Sync help must state the release contract plainly: pairing is the only
// opt-in, old/new/imported work follows one policy, status names its
// categories, and inspection is never a mandatory preview/apply gate.
func TestSyncHelpStatesSingleOptIn(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		contains []string
	}{
		{[]string{"plumbing", "sync", "--help"}, []string{"only sync opt-in", "one policy"}},
		{[]string{"plumbing", "sync", "pair", "--help"}, []string{"only sync opt-in", "one policy", "inspection never gates"}},
		{[]string{"plumbing", "sync", "status", "--help"}, []string{
			"Queued", "Acknowledged", "Received", "Waiting", "WaitingBytes", "WaitingDependencies",
			"Applied", "Conflicts", "Excluded", "only opt-in", "one policy",
		}},
		{[]string{"plumbing", "sync", "run", "--help"}, []string{"explicit exchange pass", "no mandatory preview", "only opt-in"}},
		{[]string{"plumbing", "sync", "activity", "--help"}, []string{"Inspection only", "only opt-in"}},
	} {
		r := run(t, nil, tc.args...)
		if r.exitCode != 0 {
			t.Fatalf("%v exit %d: %s", tc.args, r.exitCode, r.stderr)
		}
		for _, want := range tc.contains {
			if !strings.Contains(r.stdout, want) {
				t.Fatalf("%v help missing %q:\n%s", tc.args, want, r.stdout)
			}
		}
	}
}
