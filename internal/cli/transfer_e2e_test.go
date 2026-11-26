package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/procrastivity/wip/internal/localtransfer"
	"github.com/procrastivity/wip/internal/store"
)

type transferFixture struct {
	Path, Repo, Matter, Step, Entry string
	Findings                        []byte
}

func makeTransferFixture(t *testing.T, title string, planned bool) transferFixture {
	t.Helper()
	f := transferFixture{Path: filepath.Join(t.TempDir(), "wip.db")}
	s, err := store.Open(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	f.Repo, f.Matter, f.Step, f.Entry = s.NewID(), s.NewID(), s.NewID(), s.NewID()
	req := store.Request{Actor: store.ActorHuman, Env: store.Env{Repo: f.Repo}}
	_, err = s.Commit(context.Background(), req, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		drafts := []store.Draft{
			{Type: store.TypeRepoAttached, Subject: f.Repo, Payload: store.RepoAttached{Label: title + " repo"}},
			{Type: store.TypeMatterCreated, Subject: f.Matter, Payload: store.NodeBirth{Title: title, Locator: "recover-me"}},
			{Type: store.TypeStepCreated, Subject: f.Step, Payload: store.NodeBirth{Title: "second-level work", Locator: "step-07", Parent: f.Matter, SortKey: 70}},
		}
		if planned {
			drafts = append(drafts,
				store.Draft{Type: store.TypeBacklogEntered, Subject: f.Entry, Payload: store.BacklogEntered{Title: "planned intake", Provenance: store.Provenance("intake")}},
				store.Draft{Type: store.TypeBacklogPlanned, Subject: f.Entry, Payload: store.BacklogPlanned{Matter: f.Matter}},
			)
			f.Findings = bytes.Repeat([]byte("recovered café\n"), 90000)
			content, err := tx.ContentDraft(f.Entry, store.KindFindings, f.Findings)
			if err != nil {
				return nil, err
			}
			drafts = append(drafts, content)
		}
		return drafts, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func transferJSON(t *testing.T, env []string, into any, args ...string) {
	t.Helper()
	r := run(t, env, append([]string{"--json"}, args...)...)
	if r.exitCode != 0 {
		t.Fatalf("%v: exit=%d stderr=%s", args, r.exitCode, r.stderr)
	}
	if err := json.Unmarshal([]byte(r.stdout), into); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, r.stdout)
	}
}

func TestTransferCLIPlannedBacklogOfflineRoundTrip(t *testing.T) {
	source := makeTransferFixture(t, "old host work", true)
	target := makeTransferFixture(t, "unrelated new host work", false)
	root := t.TempDir()
	// Deliberately point the normal store/config somewhere else. All transfer
	// commands must honor their explicit paths without consulting this DB.
	env := []string{"WIP_DB_PATH=" + filepath.Join(root, "never-open.db"), "HOME=" + root, "XDG_CONFIG_HOME=" + root}
	sourceBefore, err := localtransfer.Fingerprint(source.Path)
	if err != nil {
		t.Fatal(err)
	}
	targetBefore, err := localtransfer.Fingerprint(target.Path)
	if err != nil {
		t.Fatal(err)
	}
	targetBytes, err := os.ReadFile(target.Path)
	if err != nil {
		t.Fatal(err)
	}
	var inventory map[string]any
	transferJSON(t, env, &inventory, "export", "list", "--source-db", source.Path, "--repo", source.Repo)
	exportPlan := filepath.Join(root, "export.json")
	var exported localtransfer.ExportPlan
	transferJSON(t, env, &exported, "plumbing", "export", "plan", "--source-db", source.Path, "--repo", source.Repo, "--backlog", source.Entry, "--out", exportPlan)
	if len(exported.Snapshot.Nodes) != 2 || len(exported.Snapshot.Backlog) != 1 || exported.Snapshot.Backlog[0].Matter != source.Matter {
		t.Fatalf("planned entry did not include its Matter closure: %+v", exported.Snapshot)
	}
	archive := filepath.Join(root, "old-work.wiparchive")
	var written map[string]any
	transferJSON(t, env, &written, "export", "write", "--plan", exportPlan, "--out", archive)
	segment := run(t, env, "import", "inspect", archive, "--content", exported.Snapshot.Contents[0].ID)
	if segment.exitCode != 0 || !bytes.Equal([]byte(segment.stdout), source.Findings) {
		t.Fatalf("archive inspection changed finding bytes: exit=%d stderr=%s", segment.exitCode, segment.stderr)
	}
	var inspected map[string]any
	transferJSON(t, env, &inspected, "import", "inspect", archive)
	importPlan := filepath.Join(root, "import.json")
	var preview localtransfer.ImportPlan
	transferJSON(t, env, &preview, "import", "plan", archive, "--target-db", target.Path, "--repo", target.Repo, "--out", importPlan, "--rename", source.Matter+"=recovered-old")
	if preview.Sync.State != "unpaired" {
		t.Fatalf("unexpected target pairing: %+v", preview.Sync)
	}
	targetAfterPreview, err := localtransfer.Fingerprint(target.Path)
	if err != nil || targetBefore != targetAfterPreview {
		t.Fatalf("preview modified target: %v", err)
	}
	var applied localtransfer.ImportResult
	transferJSON(t, env, &applied, "plumbing", "import", "apply", "--plan", importPlan)
	if applied.Backup == "" {
		t.Fatal("import did not retain a destination backup")
	}
	// Reading the raw capture may create empty SQLite WAL/SHM sidecars. The
	// archived database bytes, rather than that incidental file set, must match.
	backupBytes, err := os.ReadFile(filepath.Join(applied.Backup, "capture.db"))
	if err != nil || !bytes.Equal(backupBytes, targetBytes) {
		t.Fatalf("backup did not preserve destination database bytes: %v", err)
	}
	s, err := store.Open(target.Path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	entries, err := s.Backlog(ctx, target.Repo)
	if err != nil || len(entries) != 1 {
		t.Fatalf("backlog round trip: %+v %v", entries, err)
	}
	entry := entries[0]
	if entry.State != "planned" || entry.Matter == "" || entry.Matter == source.Matter {
		t.Fatalf("entry lost planned state or mapped Matter: %+v", entry)
	}
	importedMatter, err := s.Node(ctx, entry.Matter)
	if err != nil || importedMatter.Title != "old host work" || importedMatter.Locator != "recovered-old" {
		t.Fatalf("entry links to wrong Matter: %+v %v", importedMatter, err)
	}
	children, err := s.Children(ctx, entry.Matter)
	if err != nil || len(children) != 1 || children[0].SortKey != 70 || children[0].Locator != "step-07" {
		t.Fatalf("Matter closure changed: %+v %v", children, err)
	}
	got, err := s.Content(ctx, entry.ID, store.KindFindings)
	if err != nil || !bytes.Equal(got, source.Findings) {
		t.Fatalf("spilled backlog findings changed: len=%d err=%v", len(got), err)
	}
	if _, err := s.Node(ctx, target.Matter); err != nil {
		t.Fatalf("unrelated target work lost: %v", err)
	}
	if err := s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = s.Content(ctx, entry.ID, store.KindFindings)
	if err != nil || !bytes.Equal(got, source.Findings) {
		t.Fatalf("findings changed after rebuild: %v", err)
	}
	_ = s.Close()
	beforeRepeat, err := localtransfer.Fingerprint(target.Path)
	if err != nil {
		t.Fatal(err)
	}
	var repeated localtransfer.ImportResult
	transferJSON(t, env, &repeated, "import", "apply", "--plan", importPlan)
	afterRepeat, err := localtransfer.Fingerprint(target.Path)
	if err != nil || beforeRepeat != afterRepeat || repeated.Backup != "" || repeated.Receipt.Operation != applied.Receipt.Operation {
		t.Fatal("repeat import changed destination instead of returning receipt")
	}
	sourceAfter, err := localtransfer.Fingerprint(source.Path)
	if err != nil || sourceBefore != sourceAfter {
		t.Fatalf("source changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "never-open.db")); !os.IsNotExist(err) {
		t.Fatalf("transfer opened ambient host DB: %v", err)
	}
}

func TestTransferCLIStalePlanAndMissingBlobRefuseWithoutWorkMutation(t *testing.T) {
	source := makeTransferFixture(t, "old", true)
	target := makeTransferFixture(t, "new", false)
	root := t.TempDir()
	exportPlan, archive := filepath.Join(root, "export.json"), filepath.Join(root, "work.wiparchive")
	var exported localtransfer.ExportPlan
	transferJSON(t, nil, &exported, "export", "plan", "--source-db", source.Path, "--repo", source.Repo, "--backlog", source.Entry, "--out", exportPlan)
	var ignored map[string]any
	transferJSON(t, nil, &ignored, "export", "write", "--plan", exportPlan, "--out", archive)
	importPlan := filepath.Join(root, "import.json")
	var preview localtransfer.ImportPlan
	transferJSON(t, nil, &preview, "import", "plan", archive, "--target-db", target.Path, "--repo", target.Repo, "--rename", source.Matter+"=recovered", "--out", importPlan)
	s, err := store.Open(target.Path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Commit(context.Background(), store.Request{Actor: store.ActorHuman, Env: store.Env{Repo: target.Repo}}, func(_ context.Context, tx *store.Tx) ([]store.Draft, error) {
		return []store.Draft{{Type: store.TypeMatterCreated, Subject: tx.NewID(), Payload: store.NodeBirth{Title: "arrived after preview", Locator: "new-arrival"}}}, nil
	})
	_ = s.Close()
	if err != nil {
		t.Fatal(err)
	}
	before, err := localtransfer.Fingerprint(target.Path)
	if err != nil {
		t.Fatal(err)
	}
	r := run(t, nil, "--json", "import", "apply", "--plan", importPlan)
	if r.exitCode == 0 || !strings.Contains(r.stderr, "destination changed") || r.stdout != "" {
		t.Fatalf("stale plan applied: %+v", r)
	}
	after, err := localtransfer.Fingerprint(target.Path)
	if err != nil || before != after {
		t.Fatalf("stale apply changed target: %v", err)
	}
	blobs, err := os.ReadDir(filepath.Join(filepath.Dir(source.Path), "blobs"))
	if err != nil || len(blobs) != 1 {
		t.Fatalf("fixture must have one spilled blob: %v", err)
	}
	if err := os.Remove(filepath.Join(filepath.Dir(source.Path), "blobs", blobs[0].Name())); err != nil {
		t.Fatal(err)
	}
	missingPlan := filepath.Join(root, "missing.json")
	r = run(t, nil, "--json", "export", "plan", "--source-db", source.Path, "--repo", source.Repo, "--backlog", source.Entry, "--out", missingPlan)
	if r.exitCode == 0 || r.stdout != "" {
		t.Fatalf("export silently accepted missing blob: %+v", r)
	}
	if _, err := os.Stat(missingPlan); !os.IsNotExist(err) {
		t.Fatalf("missing-blob export left successful plan: %v", err)
	}
}

func TestTransferCLIExportWriteRejectsEditedPreview(t *testing.T) {
	source := makeTransferFixture(t, "original title", false)
	root := t.TempDir()
	planPath, archive := filepath.Join(root, "export.json"), filepath.Join(root, "work.wiparchive")
	var plan localtransfer.ExportPlan
	transferJSON(t, nil, &plan, "export", "plan", "--source-db", source.Path, "--repo", source.Repo, "--matter", source.Matter, "--out", planPath)
	plan.Snapshot.Nodes[0].Title = "reviewed replacement title"
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(planPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	r := run(t, nil, "--json", "export", "write", "--plan", planPath, "--out", archive)
	if r.exitCode == 0 || !strings.Contains(r.stderr, "preview changed") || r.stdout != "" {
		t.Fatalf("edited review was silently ignored: %+v", r)
	}
	if _, err = os.Stat(archive); !os.IsNotExist(err) {
		t.Fatalf("edited review emitted an archive: %v", err)
	}
}

func TestTransferCLIImportUsesInvocationRole(t *testing.T) {
	source := makeTransferFixture(t, "recoverable", true)
	root := t.TempDir()
	exportPlan, archive := filepath.Join(root, "export.json"), filepath.Join(root, "work.wiparchive")
	var ignored map[string]any
	transferJSON(t, nil, &ignored, "export", "plan", "--source-db", source.Path, "--repo", source.Repo, "--backlog", source.Entry, "--out", exportPlan)
	transferJSON(t, nil, &ignored, "export", "write", "--plan", exportPlan, "--out", archive)
	for _, spelling := range []string{"flag", "environment"} {
		t.Run(spelling, func(t *testing.T) {
			dir, dbEnv := setupRepo(t)
			refreshRepo(t, dir, dbEnv)
			if r := runIn(t, dir, dbEnv, "plumbing", "role", "spawn", "researcher"); r.exitCode != 0 {
				t.Fatalf("spawn: %+v", r)
			}
			db := strings.TrimPrefix(dbEnv[0], "WIP_DB_PATH=")
			s := openTestStore(t, db)
			repos, err := s.Repos(context.Background())
			if err != nil || len(repos) != 1 {
				t.Fatalf("target Repos: %+v %v", repos, err)
			}
			_ = s.Close()
			planPath := filepath.Join(t.TempDir(), "import.json")
			var preview localtransfer.ImportPlan
			transferJSON(t, dbEnv, &preview, "import", "plan", archive, "--target-db", db, "--repo", repos[0].ID, "--out", planPath)
			before, err := localtransfer.Fingerprint(db)
			if err != nil {
				t.Fatal(err)
			}
			for _, role := range []string{"unspawned", "researcher"} {
				env := append([]string(nil), dbEnv...)
				args := []string{"--json", "import", "apply", "--plan", planPath}
				if spelling == "flag" {
					args = append(args, "--as-role", role)
				} else {
					env = append(env, "WIP_AS_ROLE="+role)
				}
				r := run(t, env, args...)
				if role == "unspawned" {
					if r.exitCode == 0 || !strings.Contains(r.stderr, "no open spawn") || r.stdout != "" {
						t.Fatalf("unbacked role imported work: %+v", r)
					}
					after, err := localtransfer.Fingerprint(db)
					if err != nil || before != after {
						t.Fatalf("rejected role changed target: %v", err)
					}
					continue
				}
				if r.exitCode != 0 {
					t.Fatalf("backed role refused: %+v", r)
				}
				s = openTestStore(t, db)
				ev := wantOneEvent(t, s, preview.Work.Operation, store.TypeWorkImported)
				if ev.Actor != store.RoleActor(role) {
					t.Fatalf("import actor = %q, want role:%s", ev.Actor, role)
				}
				_ = s.Close()
				if r := runIn(t, dir, dbEnv, "plumbing", "role", "close", role); r.exitCode != 0 {
					t.Fatalf("close: %+v", r)
				}
				before, err = localtransfer.Fingerprint(db)
				if err != nil {
					t.Fatal(err)
				}
				retry := run(t, env, args...)
				var repeated localtransfer.ImportResult
				if retry.exitCode != 0 || json.Unmarshal([]byte(retry.stdout), &repeated) != nil || repeated.Receipt == nil || repeated.Receipt.Operation != preview.Work.Operation || repeated.Backup != "" {
					t.Fatalf("closed role could not reconcile its committed receipt: %+v", retry)
				}
				after, err := localtransfer.Fingerprint(db)
				if err != nil || before != after {
					t.Fatalf("receipt recovery changed destination: %v", err)
				}
			}
		})
	}
}

func TestTransferCLIPairedAndPausedImportsNeverStartTransport(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	source := makeTransferFixture(t, "old", true)
	root := t.TempDir()
	exportPlan, archive := filepath.Join(root, "export.json"), filepath.Join(root, "work.wiparchive")
	var ignored map[string]any
	transferJSON(t, nil, &ignored, "export", "plan", "--source-db", source.Path, "--repo", source.Repo, "--backlog", source.Entry, "--out", exportPlan)
	transferJSON(t, nil, &ignored, "export", "write", "--plan", exportPlan, "--out", archive)
	for _, state := range []string{"paired", "paused"} {
		t.Run(state, func(t *testing.T) {
			target := makeTransferFixture(t, "new", false)
			s, err := store.Open(target.Path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.PairExchange(context.Background(), target.Repo, server.URL); err != nil {
				t.Fatal(err)
			}
			if state == "paused" {
				if err := s.UnpairExchange(context.Background(), target.Repo); err != nil {
					t.Fatal(err)
				}
			}
			_ = s.Close()
			planPath := filepath.Join(t.TempDir(), "import.json")
			var preview localtransfer.ImportPlan
			transferJSON(t, nil, &preview, "import", "plan", archive, "--target-db", target.Path, "--repo", target.Repo, "--rename", source.Matter+"=recovered", "--out", planPath)
			if preview.Sync.State != state || len(preview.Sync.Bindings) != 1 || preview.Sync.Bindings[0].Endpoint != server.URL {
				t.Fatalf("misleading sync disclosure: %+v", preview.Sync)
			}
			var result localtransfer.ImportResult
			transferJSON(t, nil, &result, "import", "apply", "--plan", planPath)
			s, err = store.Open(target.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			bindings, err := s.ExchangeBindings(context.Background())
			if err != nil || len(bindings) != 1 || bindings[0].Active != (state == "paired") || bindings[0].Endpoint != server.URL {
				t.Fatalf("import changed pairing policy: %+v %v", bindings, err)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("offline import attempted %d HTTP requests", requests.Load())
	}
}
