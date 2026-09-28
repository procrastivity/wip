package wipdseed

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const stateInstallLockHelperEnv = "WIPDSEED_STATE_INSTALL_LOCK_HELPER"

func TestReplaceInstalledStateSerializesCrossProcessCompareAndRename(t *testing.T) {
	directory := t.TempDir()
	previous := ClientState{
		Schema: "wipd.m5-client-state/1", RepoID: testRepoID, DomainID: testDomainID, Epoch: 1,
		EnvironmentID: "01KZ7XHAQT1S46NYPN1PW1DX3D", OwnerKeyID: "sha256:" + strings.Repeat("a", 64),
		SPKIDigest: "sha256:" + strings.Repeat("b", 64), Prefix: emptyWireAnchor(),
		ManifestDigest: emptyManifestDigest(), Projections: []json.RawMessage{}, StepProjections: []json.RawMessage{},
	}
	previousBytes, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, stateName), previousBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	previousPath := filepath.Join(directory, "previous.json")
	if err = os.WriteFile(previousPath, previousBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStateInstallLockSubprocessHelper$")
	command.Env = append(os.Environ(), stateInstallLockHelperEnv+"=1", "WIPDSEED_STATE_INSTALL_LOCK_DIR="+directory,
		"WIPDSEED_STATE_INSTALL_LOCK_PREVIOUS="+previousPath)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("lock helper readiness = %q, %v; want locked", line, err)
	}

	older := previous
	older.Prefix.EventCount = 1
	olderEventID := "01KZ7XHAQT1S46NYPN1PW1DX3E"
	older.Prefix.EventID = &olderEventID
	older.Prefix.Digest = testDigest([]byte("older prefix"))
	attempting := make(chan struct{})
	attemptResult := make(chan error, 1)
	go func() {
		close(attempting)
		attemptResult <- replaceInstalledState(directory, previousBytes, older)
	}()
	<-attempting
	var earlyErr error
	completedWhileLocked := false
	select {
	case earlyErr = <-attemptResult:
		completedWhileLocked = true
	case <-time.After(75 * time.Millisecond):
	}

	if _, err = fmt.Fprintln(stdin, "install"); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal(err)
	}
	line, readErr := reader.ReadString('\n')
	waitErr := command.Wait()
	if readErr != nil || strings.TrimSpace(line) != "installed" || waitErr != nil {
		t.Fatalf("newer lock helper result = %q, read %v, wait %v", line, readErr, waitErr)
	}
	if completedWhileLocked {
		t.Fatalf("older replacement completed while the cross-process lock was held: %v", earlyErr)
	}
	if err = <-attemptResult; !errors.Is(err, ErrStateExists) {
		t.Fatalf("stale older replacement = %v, want ErrStateExists", err)
	}
	installed, err := os.ReadFile(filepath.Join(directory, stateName))
	if err != nil {
		t.Fatal(err)
	}
	var got ClientState
	if err = json.Unmarshal(installed, &got); err != nil {
		t.Fatal(err)
	}
	if got.Prefix.EventCount != 2 || got.Prefix.EventID == nil || *got.Prefix.EventID != "01KZ7XHAQT1S46NYPN1PW1DX3F" ||
		got.Prefix.Digest != testDigest([]byte("newer prefix")) {
		t.Fatalf("installed state regressed from the newer prefix: %+v", got.Prefix)
	}
	lockInfo, err := os.Stat(filepath.Join(directory, ".client-state.lock"))
	if err != nil || lockInfo.Mode().Perm() != 0o600 {
		t.Fatalf("persistent state lock mode = %v, %v; want 0600", lockInfo, err)
	}
}

func TestStateInstallLockSubprocessHelper(t *testing.T) {
	if os.Getenv(stateInstallLockHelperEnv) != "1" {
		return
	}
	directory := os.Getenv("WIPDSEED_STATE_INSTALL_LOCK_DIR")
	lock, err := lockInstalledState(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			t.Errorf("close installed-state lock: %v", closeErr)
		}
	}()
	if _, err = fmt.Fprintln(os.Stdout, "locked"); err != nil {
		t.Fatal(err)
	}
	var command string
	if _, err = fmt.Fscan(os.Stdin, &command); err != nil || command != "install" {
		t.Fatalf("lock helper command = %q, %v", command, err)
	}
	previous, err := os.ReadFile(os.Getenv("WIPDSEED_STATE_INSTALL_LOCK_PREVIOUS"))
	if err != nil {
		t.Fatal(err)
	}
	var newer ClientState
	if err = json.Unmarshal(previous, &newer); err != nil {
		t.Fatal(err)
	}
	newer.Prefix.EventCount = 2
	newerEventID := "01KZ7XHAQT1S46NYPN1PW1DX3F"
	newer.Prefix.EventID = &newerEventID
	newer.Prefix.Digest = testDigest([]byte("newer prefix"))
	if err = replaceInstalledStateLocked(directory, previous, newer); err != nil {
		t.Fatal(err)
	}
	if _, err = fmt.Fprintln(os.Stdout, "installed"); err != nil {
		t.Fatal(err)
	}
}
