package cli

import (
	"encoding/csv"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/procrastivity/wip/internal/buildinfo"
	"github.com/procrastivity/wip/internal/iostreams"
	"github.com/procrastivity/wip/internal/manifest"
	"github.com/procrastivity/wip/internal/operation"
)

func TestM6OperationCensus(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	definitions := readCensusTSV(t, root, "docs/wipd/m6-operations.tsv", []string{
		"id", "status", "owner_step", "dependencies", "access", "delivery", "context", "claim",
		"guards", "reads", "writes", "blob_inputs", "external_effects", "current_owner",
		"current_behavior", "target_behavior", "current_tests", "target_tests",
	})
	extra := readCensusTSV(t, root, "docs/wipd/m6-engine-operations.tsv", []string{
		"id", "status", "owner_step", "dependencies", "access", "delivery", "context", "claim",
		"guards", "reads", "writes", "blob_inputs", "external_effects", "current_owner",
		"current_behavior", "target_behavior", "current_tests", "target_tests",
	})
	definitions = append(definitions, extra...)
	operationByID := checkCensusOperations(t, definitions)

	cliModes := readCensusTSV(t, root, "docs/wipd/m6-cli-modes.tsv", []string{
		"path", "mode", "disposition", "owner_step", "alias_of", "operation_ids",
		"current_owner", "current_behavior", "target_behavior", "test_owner",
	})
	checkCensusCLIModes(t, cliModes, operationByID)

	owners := readCensusTSV(t, root, "docs/wipd/m6-noncli-owners.tsv", []string{
		"id", "kind", "source", "line", "operation_ids", "owner_step", "dependencies",
		"current_effect", "target_behavior", "test_owner",
	})
	checkCensusOwners(t, root, owners, operationByID)

	owned := make(map[string]bool)
	for _, rows := range [][]map[string]string{cliModes, owners} {
		for _, row := range rows {
			for _, id := range censusList(row["operation_ids"]) {
				if id != "none" {
					owned[id] = true
				}
			}
		}
	}
	for id := range operationByID {
		if !owned[id] {
			t.Errorf("operation %s has no CLI mode or non-CLI owner", id)
		}
	}
}

// These expectations are intentionally independent of the census TSV. They
// pin the current multi-mode dispatch branches in next, init, finding,
// lifecycle, and outbox CLI sources to their semantic operation identities,
// so a relabelled mode or valid-but-wrong operation cannot make the table
// self-consistent while losing the source behavior it describes.
var m6PinnedCLIModes = map[string]map[string]string{
	"next": {
		"frontier-read": "next.read@v1",
		"cursor-set":    "cursor.set@v1",
		"cursor-clear":  "cursor.clear@v1",
	},
	"init": {
		"resolve-route":      "route.resolve@v1",
		"remote-less-create": "repo.create-local@v1",
		"attach-once":        "environment.attach-once@v1",
		"tier-enrollment":    "tier.enroll@v1",
	},
	"plumbing finding add": {
		"Matter-or-Step-subject": "finding.append@v1",
		"BacklogEntry-subject":   "backlog.finding.append@v1",
	},
	"plumbing finish": {
		"finish-Matter": "matter.finish@v1;render.sealed-node@v1",
		"finish-Stage":  "stage.finish@v1",
		"finish-Step":   "step.finish@v1",
	},
	"plumbing next": {
		"frontier-read": "next.read@v1",
		"cursor-set":    "cursor.set@v1",
		"cursor-clear":  "cursor.clear@v1",
	},
	"plumbing outbox level": {
		"read-level": "tracker.config.read@v1",
		"set-level":  "tracker.config.set@v1",
	},
	"plumbing outbox backlog-push": {
		"read-backlog-push": "tracker.config.read@v1",
		"set-backlog-push":  "tracker.config.set@v1",
	},
	"plumbing outbox backend": {
		"read-backend": "tracker.config.read@v1",
		"set-backend":  "tracker.config.set@v1",
	},
	"plumbing outbox target": {
		"read-target": "tracker.config.read@v1",
		"set-target":  "tracker.config.set@v1",
	},
	"plumbing outbox project": {
		"read-project": "tracker.config.read@v1",
		"set-project":  "tracker.config.set@v1",
	},
	"plumbing outbox canceled-label": {
		"read-canceled-label": "tracker.config.read@v1",
		"set-canceled-label":  "tracker.config.set@v1",
	},
}

var m6PinnedAliases = map[string]string{
	"next": "plumbing next",
}

func TestM6PinnedCLIModeExpectationsRejectMutations(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	rows := readCensusTSV(t, root, "docs/wipd/m6-cli-modes.tsv", []string{
		"path", "mode", "disposition", "owner_step", "alias_of", "operation_ids",
		"current_owner", "current_behavior", "target_behavior", "test_owner",
	})
	if problems := m6PinnedCLIModeErrors(rows); len(problems) != 0 {
		t.Fatalf("checked-in multi-mode branches do not match pinned expectations: %s", strings.Join(problems, "; "))
	}

	tests := []struct {
		name       string
		mutate     func(*testing.T, []map[string]string) []map[string]string
		wantErrors []string
	}{
		{
			name: "relabel cursor clear",
			mutate: func(t *testing.T, rows []map[string]string) []map[string]string {
				findCensusModeRow(t, rows, "next", "cursor-clear")["mode"] = "cursor-reset"
				return rows
			},
			wantErrors: []string{"missing pinned mode next/cursor-clear", "unexpected mode next/cursor-reset"},
		},
		{
			name: "map cursor clear to another defined operation",
			mutate: func(t *testing.T, rows []map[string]string) []map[string]string {
				findCensusModeRow(t, rows, "next", "cursor-clear")["operation_ids"] = "cursor.set@v1"
				return rows
			},
			wantErrors: []string{"next/cursor-clear operation_ids"},
		},
		{
			name: "omit cursor clear",
			mutate: func(t *testing.T, rows []map[string]string) []map[string]string {
				index := findCensusModeIndex(t, rows, "next", "cursor-clear")
				return append(rows[:index], rows[index+1:]...)
			},
			wantErrors: []string{"missing pinned mode next/cursor-clear"},
		},
		{
			name: "break alias semantic parity",
			mutate: func(t *testing.T, rows []map[string]string) []map[string]string {
				findCensusModeRow(t, rows, "next", "cursor-clear")["operation_ids"] = "cursor.set@v1"
				return rows
			},
			wantErrors: []string{"alias next -> plumbing next differs for mode cursor-clear"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutated := cloneCensusRows(rows)
			mutated = tt.mutate(t, mutated)
			problems := m6PinnedCLIModeErrors(mutated)
			for _, want := range tt.wantErrors {
				if !containsCensusProblem(problems, want) {
					t.Errorf("validation errors %q do not include %q", problems, want)
				}
			}
		})
	}
}

func m6PinnedCLIModeErrors(rows []map[string]string) []string {
	actual := make(map[string]map[string]string)
	var problems []string
	for _, row := range rows {
		path, mode := row["path"], row["mode"]
		if actual[path] == nil {
			actual[path] = make(map[string]string)
		}
		if _, exists := actual[path][mode]; exists {
			problems = append(problems, fmt.Sprintf("duplicate mode %s/%s", path, mode))
		}
		actual[path][mode] = row["operation_ids"]
	}

	for path, expectedModes := range m6PinnedCLIModes {
		for mode, wantOperation := range expectedModes {
			gotOperation, exists := actual[path][mode]
			if !exists {
				problems = append(problems, fmt.Sprintf("missing pinned mode %s/%s", path, mode))
				continue
			}
			if gotOperation != wantOperation {
				problems = append(problems, fmt.Sprintf("%s/%s operation_ids = %q, want %q", path, mode, gotOperation, wantOperation))
			}
		}
		for mode := range actual[path] {
			if _, exists := expectedModes[mode]; !exists {
				problems = append(problems, fmt.Sprintf("unexpected mode %s/%s", path, mode))
			}
		}
	}

	for alias, canonical := range m6PinnedAliases {
		aliasModes, canonicalModes := actual[alias], actual[canonical]
		for mode, aliasOperation := range aliasModes {
			canonicalOperation, exists := canonicalModes[mode]
			if !exists || aliasOperation != canonicalOperation {
				problems = append(problems, fmt.Sprintf("alias %s -> %s differs for mode %s", alias, canonical, mode))
			}
		}
		for mode := range canonicalModes {
			if _, exists := aliasModes[mode]; !exists {
				problems = append(problems, fmt.Sprintf("alias %s -> %s is missing mode %s", alias, canonical, mode))
			}
		}
	}

	sort.Strings(problems)
	return problems
}

func cloneCensusRows(rows []map[string]string) []map[string]string {
	cloned := make([]map[string]string, len(rows))
	for index, row := range rows {
		cloned[index] = make(map[string]string, len(row))
		for key, value := range row {
			cloned[index][key] = value
		}
	}
	return cloned
}

func findCensusModeRow(t *testing.T, rows []map[string]string, path, mode string) map[string]string {
	t.Helper()
	return rows[findCensusModeIndex(t, rows, path, mode)]
}

func findCensusModeIndex(t *testing.T, rows []map[string]string, path, mode string) int {
	t.Helper()
	for index, row := range rows {
		if row["path"] == path && row["mode"] == mode {
			return index
		}
	}
	t.Fatalf("missing test fixture mode %s/%s", path, mode)
	return -1
}

func containsCensusProblem(problems []string, fragment string) bool {
	for _, problem := range problems {
		if strings.Contains(problem, fragment) {
			return true
		}
	}
	return false
}

func readCensusTSV(t *testing.T, root, relative string, wantHeader []string) []map[string]string {
	t.Helper()
	file, err := os.Open(filepath.Join(root, relative))
	if err != nil {
		t.Fatal(err)
	}
	reader := csv.NewReader(file)
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	records, readErr := reader.ReadAll()
	closeErr := file.Close()
	if readErr != nil {
		t.Fatalf("read %s: %v", relative, readErr)
	}
	if closeErr != nil {
		t.Fatalf("close %s: %v", relative, closeErr)
	}
	if len(records) == 0 {
		t.Fatalf("%s is empty; want header %q", relative, wantHeader)
	}
	if strings.Join(records[0], "\x00") != strings.Join(wantHeader, "\x00") {
		t.Fatalf("%s header = %q, want %q", relative, records[0], wantHeader)
	}
	rows := make([]map[string]string, 0, len(records)-1)
	for index, record := range records[1:] {
		if len(record) == 0 {
			continue
		}
		if len(record) != len(wantHeader) {
			t.Errorf("%s:%d has %d fields, want %d", relative, index+2, len(record), len(wantHeader))
			continue
		}
		row := make(map[string]string, len(wantHeader))
		for column, name := range wantHeader {
			if record[column] != strings.TrimSpace(record[column]) {
				t.Errorf("%s:%d field %s has surrounding whitespace %q", relative, index+2, name, record[column])
			}
			row[name] = record[column]
		}
		rows = append(rows, row)
	}
	return rows
}

func checkCensusOperations(t *testing.T, rows []map[string]string) map[string]map[string]string {
	t.Helper()
	definitions := make(map[string]map[string]string, len(rows))
	registered := make(map[string]map[string]string)
	for index, row := range rows {
		id := row["id"]
		if _, exists := definitions[id]; exists {
			t.Errorf("operation identity %q has multiple definition rows", id)
			continue
		}
		definitions[id] = row
		if !validCensusOperationID(id) {
			t.Errorf("operation row %d has invalid versioned ID %q", index+1, id)
		}
		if !oneOf(row["status"], "catalogued-m5", "proposed-not-registered-not-implemented", "authenticated-control-existing") {
			t.Errorf("%s has unsupported status %q", id, row["status"])
		}
		step, err := strconv.Atoi(row["owner_step"])
		if err != nil || step < 2 || step > 20 {
			t.Errorf("%s owner_step = %q, want its single primary owner in Steps 2–20", id, row["owner_step"])
		}
		checkStepList(t, id+" dependencies", row["dependencies"], 2, 23)
		if !oneOf(row["access"], "read", "mutation") {
			t.Errorf("%s has invalid access %q", id, row["access"])
		}
		if !oneOf(row["delivery"], "none", "authority", "claim", "provisional", "capture", "environment") {
			t.Errorf("%s has invalid delivery %q", id, row["delivery"])
		}
		if row["access"] == "read" && row["delivery"] != "none" {
			t.Errorf("read %s must have delivery=none, got %q", id, row["delivery"])
		}
		if row["access"] == "mutation" && (row["delivery"] == "none" || row["writes"] == "none") {
			t.Errorf("mutation %s must declare delivery and a write footprint", id)
		}
		if row["delivery"] == "claim" && row["claim"] != "exact" {
			t.Errorf("claim-delivered %s must require an exact claim, got %q", id, row["claim"])
		}
		if !oneOf(row["claim"], "none", "exact", "implicit-birth") {
			t.Errorf("%s has invalid claim requirement %q", id, row["claim"])
		}
		for _, field := range []string{"context", "guards", "reads", "writes", "blob_inputs", "external_effects", "current_owner", "current_behavior", "target_behavior", "current_tests", "target_tests"} {
			if strings.TrimSpace(row[field]) == "" {
				t.Errorf("%s omits %s (use explicit none for empty sets)", id, field)
			}
		}
		for _, field := range []string{"context", "guards", "reads", "writes", "blob_inputs", "external_effects"} {
			if hasDuplicate(censusList(row[field])) {
				t.Errorf("%s has duplicate entries in %s: %q", id, field, row[field])
			}
		}
		if row["status"] == "catalogued-m5" {
			registered[id] = row
		}
	}

	actual := make(map[string]operation.Metadata)
	for _, definition := range operation.Catalogue() {
		metadata := definition.Metadata()
		actual[metadata.Operation.String()] = metadata
	}
	if len(actual) != len(registered) {
		t.Errorf("catalogued-m5 row count = %d, runtime Catalogue has %d entries", len(registered), len(actual))
	}
	for id, metadata := range actual {
		row, ok := registered[id]
		if !ok {
			t.Errorf("runtime catalogue operation %s has no catalogued-m5 row", id)
			continue
		}
		compareCensusMetadata(t, id, row, metadata)
	}
	for id, row := range definitions {
		if row["status"] == "catalogued-m5" {
			if _, ok := actual[id]; !ok {
				t.Errorf("%s is marked catalogued-m5 but is absent from operation.Catalogue()", id)
			}
		}
		if row["status"] == "proposed-not-registered-not-implemented" || row["status"] == "authenticated-control-existing" {
			if _, ok := actual[id]; ok {
				t.Errorf("%s operation %s must remain outside operation.Catalogue()", row["status"], id)
			}
		}
	}
	return definitions
}

func compareCensusMetadata(t *testing.T, id string, row map[string]string, metadata operation.Metadata) {
	t.Helper()
	checks := map[string]string{
		"access":           string(metadata.Access),
		"delivery":         string(metadata.Delivery),
		"context":          censusJoin(metadata.RequiredContext),
		"claim":            string(metadata.Claim),
		"guards":           censusJoin(metadata.Guards),
		"writes":           censusJoin(metadata.Writes),
		"blob_inputs":      censusBlobInputs(metadata.BlobInputs),
		"external_effects": censusJoin(metadata.ExternalEffects),
	}
	for field, want := range checks {
		if want == "" {
			want = "none"
		}
		if got := row[field]; got != want {
			t.Errorf("%s %s = %q, runtime metadata is %q", id, field, got, want)
		}
	}
}

func checkCensusCLIModes(t *testing.T, rows []map[string]string, operations map[string]map[string]string) {
	t.Helper()
	t.Setenv("WIP_SELFTEST", "0")
	root := NewRootCommand(&iostreams.Streams{Out: io.Discard, Err: io.Discard}, buildinfo.Info{})
	result, err := manifest.Build(root, buildinfo.Info{})
	if err != nil {
		t.Fatalf("build Cobra manifest in process: %v", err)
	}
	verbs := make(map[string]manifest.Verb, len(result.Verbs))
	for _, verb := range result.Verbs {
		if _, exists := verbs[verb.Name]; exists {
			t.Errorf("manifest contains duplicate path %q", verb.Name)
		}
		verbs[verb.Name] = verb
	}
	if len(verbs) != 79 {
		t.Errorf("current manifest has %d runnable paths, want census baseline 79", len(verbs))
	}
	if len(rows) != 95 {
		t.Errorf("CLI mode table has %d rows, want census baseline 95", len(rows))
	}
	for _, problem := range m6PinnedCLIModeErrors(rows) {
		t.Errorf("CLI mode expectations: %s", problem)
	}
	seenModes := make(map[string]bool, len(rows))
	paths := make(map[string]bool)
	pathModes := make(map[string][]map[string]string)
	local := make(map[string]bool)
	for _, row := range rows {
		path, mode := row["path"], row["mode"]
		key := path + "\x00" + mode
		if seenModes[key] {
			t.Errorf("CLI path/mode %q / %q is multiply owned", path, mode)
		}
		seenModes[key] = true
		paths[path] = true
		pathModes[path] = append(pathModes[path], row)
		verb, ok := verbs[path]
		if !ok {
			t.Errorf("CLI mode row %q / %q is absent from current manifest", path, mode)
		}
		if !oneOf(row["disposition"], "operation", "cli-local") {
			t.Errorf("%q / %q has invalid disposition %q", path, mode, row["disposition"])
		}
		if strings.TrimSpace(row["current_owner"]) == "" || strings.TrimSpace(row["current_behavior"]) == "" ||
			strings.TrimSpace(row["target_behavior"]) == "" || strings.TrimSpace(row["test_owner"]) == "" {
			t.Errorf("CLI mode %q / %q has incomplete ownership/behavior/test metadata", path, mode)
		}
		if row["alias_of"] != verb.AliasOf {
			t.Errorf("CLI path %q alias_of = %q, manifest says %q", path, row["alias_of"], verb.AliasOf)
		}
		ids := censusList(row["operation_ids"])
		if hasDuplicate(ids) {
			t.Errorf("CLI mode %q / %q repeats an operation identity: %q", path, mode, row["operation_ids"])
		}
		if row["disposition"] == "cli-local" {
			if len(ids) != 0 || row["owner_step"] != "20" {
				t.Errorf("CLI-local mode %q / %q must have no operation and owner Step 20", path, mode)
			}
			local[path] = true
		} else {
			if len(ids) == 0 {
				t.Errorf("domain mode %q / %q has no exact operation identity", path, mode)
			}
			if row["owner_step"] != "-" {
				t.Errorf("domain mode %q / %q must derive semantic owner Step from its operation rows, got %q", path, mode, row["owner_step"])
			}
		}
		for _, id := range ids {
			if _, exists := operations[id]; !exists {
				t.Errorf("CLI mode %q / %q references undefined operation %q", path, mode, id)
			}
		}
	}
	if len(paths) != len(verbs) {
		t.Errorf("CLI table covers %d distinct paths, manifest has %d", len(paths), len(verbs))
	}
	for path, verb := range verbs {
		if !paths[path] {
			t.Errorf("manifest path %q has no census mode", path)
		}
		for _, row := range pathModes[path] {
			if row["alias_of"] != verb.AliasOf {
				t.Errorf("path %q modes disagree with manifest alias identity", path)
			}
		}
	}
	actualAliases := make(map[string]string)
	for path, verb := range verbs {
		if verb.AliasOf != "" {
			actualAliases[path] = verb.AliasOf
		}
	}
	if len(actualAliases) != len(m6PinnedAliases) {
		t.Errorf("manifest aliases = %v, want exactly %v", actualAliases, m6PinnedAliases)
	}
	for alias, canonical := range m6PinnedAliases {
		if got := actualAliases[alias]; got != canonical {
			t.Errorf("manifest alias %q targets %q, want %q", alias, got, canonical)
		}
	}
	wantLocal := []string{"install", "manifest", "uninstall", "version"}
	gotLocal := make([]string, 0, len(local))
	for path := range local {
		gotLocal = append(gotLocal, path)
	}
	sort.Strings(gotLocal)
	if fmt.Sprint(gotLocal) != fmt.Sprint(wantLocal) {
		t.Errorf("CLI-local paths = %v, want exactly %v", gotLocal, wantLocal)
	}
}

func checkCensusOwners(t *testing.T, root string, rows []map[string]string, operations map[string]map[string]string) {
	t.Helper()
	ownerIDs := make(map[string]bool)
	commitRows := make(map[string]bool)
	commitCount := 0
	kinds := make(map[string]bool)
	kindOperations := make(map[string]map[string]bool)
	for _, row := range rows {
		id := row["id"]
		if ownerIDs[id] {
			t.Errorf("non-CLI owner row %q is multiply owned", id)
		}
		ownerIDs[id] = true
		kinds[row["kind"]] = true
		if kindOperations[row["kind"]] == nil {
			kindOperations[row["kind"]] = make(map[string]bool)
		}
		if row["source"] == "" || row["current_effect"] == "" || row["target_behavior"] == "" || row["test_owner"] == "" {
			t.Errorf("non-CLI owner row %q has incomplete source/effect/target/test metadata", id)
		}
		for _, opID := range censusList(row["operation_ids"]) {
			if opID != "none" {
				kindOperations[row["kind"]][opID] = true
				if _, ok := operations[opID]; !ok {
					t.Errorf("non-CLI owner %q references undefined operation %q", id, opID)
				}
			}
		}
		if hasDuplicate(censusList(row["operation_ids"])) {
			t.Errorf("non-CLI owner %q repeats an operation identity: %q", id, row["operation_ids"])
		}
		if row["kind"] != "store-commit" {
			continue
		}
		commitCount++
		commitRows[id] = true
		line, err := strconv.Atoi(row["line"])
		if err != nil || line < 1 || id != row["source"]+":"+row["line"] {
			t.Errorf("Store.Commit owner %q must identify an exact source:line", id)
		}
		if len(censusList(row["operation_ids"])) == 0 {
			t.Errorf("Store.Commit owner %q has no semantic operation identity", id)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(row["source"]))); err != nil {
			t.Errorf("Store.Commit owner %q source does not exist: %v", id, err)
		}
	}
	if commitCount != 58 {
		t.Errorf("owner table has %d Store.Commit rows, want baseline 58", commitCount)
	}
	for _, kind := range []string{
		"store-open", "migration", "blob-store", "blob-retention", "filesystem", "advisory-lock",
		"cli-files", "git-read", "cli-input", "provider-http", "external-hook",
		"authenticated-control", "environment-identity", "authority-read", "daemon-diagnostics",
	} {
		if !kinds[kind] {
			t.Errorf("non-CLI owner inventory is missing effect category %q", kind)
		}
	}
	requiredOwnerOperations := map[string][]string{
		"store-open":            {"status.read@v1", "backlog.read@v1", "next.read@v1"},
		"blob-store":            {"content.write-once@v1", "finding.append@v1", "content.read@v1", "backlog.content.read@v1"},
		"blob-retention":        {"blob.reap@v1", "store.clean@v1"},
		"filesystem":            {"render.refresh@v1", "render.sealed-node@v1", "dispatch.open@v1", "dispatch.close@v1", "dispatch.reap@v1", "dispatch.scratch.remove@v1", "store.clean@v1"},
		"advisory-lock":         {"run-lock.inspect@v1", "run.stand-down@v1", "scheduler.resume@v1"},
		"git-read":              {"route.resolve@v1", "repo.create-local@v1", "environment.attach-once@v1", "repo.adopt-remote@v1", "clone.relink@v1"},
		"provider-http":         {"tracker.capabilities.read@v1", "tracker.read@v1", "outbox.flush@v1", "tracker.item.confirm@v1", "tracker.state.pushed@v1", "tracker.state.observed@v1"},
		"authenticated-control": {"claim.acquire@v1", "claim.release@v1", "claim.stand-down@v1", "claim-journal.repair@v1", "claim-journal.close@v1"},
		"environment-identity":  {"environment.certificate.renew@v1", "environment.certificate.rotate@v1", "environment.identity.recover@v1", "environment.identity.revoke@v1"},
		"authority-read":        {"batch.read@v1"},
		"daemon-diagnostics":    {"diagnostics.inspect@v1"},
	}
	for kind, operationIDs := range requiredOwnerOperations {
		for _, id := range operationIDs {
			if !kindOperations[kind][id] {
				t.Errorf("non-CLI effect owner %q is missing operation %s", kind, id)
			}
		}
	}

	actualSites := productionDomainCommitSites(t, root)
	if len(actualSites) != 58 {
		t.Errorf("production Store.Commit AST scan found %d sites, want baseline 58", len(actualSites))
	}
	for site := range actualSites {
		if !commitRows[site] {
			t.Errorf("production Store.Commit site %q has no unique owner row", site)
		}
	}
	for site := range commitRows {
		if !actualSites[site] {
			t.Errorf("owner row %q does not match a production Store.Commit site", site)
		}
	}
}

func productionDomainCommitSites(t *testing.T, root string) map[string]bool {
	t.Helper()
	sites := make(map[string]bool)
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(filepath.Base(path), "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Commit" || !isDomainStoreReceiver(selector.X) {
				return true
			}
			site := filepath.ToSlash(rel) + ":" + strconv.Itoa(fset.Position(call.Pos()).Line)
			if sites[site] {
				t.Errorf("AST scan found duplicate Store.Commit site %q", site)
			}
			sites[site] = true
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sites
}

func isDomainStoreReceiver(expr ast.Expr) bool {
	switch receiver := expr.(type) {
	case *ast.Ident:
		return receiver.Name == "s"
	case *ast.SelectorExpr:
		if receiver.Sel.Name != "s" {
			return false
		}
		base, ok := receiver.X.(*ast.Ident)
		return ok && base.Name == "p"
	default:
		return false
	}
}

func validCensusOperationID(id string) bool {
	name, version, ok := strings.Cut(id, "@v")
	if !ok || name == "" || version == "" || strings.Contains(version, "@") || len(strings.Split(name, ".")) < 2 {
		return false
	}
	for _, segment := range strings.Split(name, ".") {
		if segment == "" || segment[0] < 'a' || segment[0] > 'z' {
			return false
		}
		for _, r := range segment {
			letter := r >= 'a' && r <= 'z'
			digit := r >= '0' && r <= '9'
			if !letter && !digit && r != '-' {
				return false
			}
		}
	}
	n, err := strconv.Atoi(version)
	return err == nil && n > 0
}

func censusList(value string) []string {
	if value == "" || value == "none" || value == "-" {
		return nil
	}
	return strings.Split(value, ";")
}

func censusJoin[T ~string](values []T) string {
	if len(values) == 0 {
		return "none"
	}
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = string(value)
	}
	return strings.Join(parts, ";")
}

func censusBlobInputs(values []operation.BlobSpec) string {
	if len(values) == 0 {
		return "none"
	}
	parts := make([]string, len(values))
	for index, value := range values {
		requirement := "optional"
		if value.Required {
			requirement = "required"
		}
		parts[index] = value.Name + "(" + requirement + ")"
	}
	return strings.Join(parts, ";")
}

func checkStepList(t *testing.T, label, value string, minimum, maximum int) {
	t.Helper()
	for _, item := range censusList(value) {
		step, err := strconv.Atoi(item)
		if err != nil || step < minimum || step > maximum {
			t.Errorf("%s contains invalid Step %q", label, item)
		}
	}
}

func hasDuplicate(values []string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
