package wipdauthority

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// step10MinimumTestTime is the least remaining test deadline the Step 10
// process acceptance accepts before it starts. A full local run takes about
// twelve minutes, so Go's default ten-minute package timeout always kills it.
const step10MinimumTestTime = 20 * time.Minute

const step10SkipHint = `To leave this test out instead, as CI does:
  GOFLAGS='-skip=^TestStep10PinnedPlanThroughAuthenticatedEnvironmentAndAuthorityProcesses$' make check`

// step10ProcessPreflight checks the host conditions the Step 10 process
// acceptance needs before it starts any process work. Without it, a missing
// condition shows up minutes into the run as an unrelated-looking failure:
// a package timeout, a missing input directory, or a sandbox EPERM.
func step10ProcessPreflight(t *testing.T, policyMode string) {
	t.Helper()
	// Inside a Verifier clean-state check, this test stops before the
	// Verifier pass: it needs no pinned inputs and no nested sandbox, and
	// it fits the check's default deadline.
	if _, nested := step10NestedVerifierCheck(); nested {
		return
	}
	var problems []string
	if deadline, ok := t.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < step10MinimumTestTime {
			problems = append(problems, fmt.Sprintf(
				"The test deadline is %s away, and this test takes about 12 minutes.\n"+
					"  Fix: run with GOFLAGS='-timeout=45m -count=1'.", remaining.Round(time.Second)))
		}
	}
	// The failed-* and blocked-* policy modes return before the Verifier
	// pass, so they need neither the pinned inputs nor the sandbox.
	if !strings.HasPrefix(policyMode, "failed-") && !strings.HasPrefix(policyMode, "blocked-") {
		if problem := step10VerifierInputsProblem(); problem != "" {
			problems = append(problems, problem)
		}
		if problem := step10SandboxProblem(); problem != "" {
			problems = append(problems, problem)
		}
	}
	if len(problems) > 0 {
		t.Fatalf("Step 10 process acceptance cannot run on this host:\n\n%s\n\n%s\n\nSee docs/step10-verifier-test-inputs.md.",
			strings.Join(problems, "\n\n"), step10SkipHint)
	}
}

// step10NestedVerifierCheck reports whether this test runs inside one of the
// Verifier's own clean-state checks, where it must not recurse into D106.
func step10NestedVerifierCheck() (string, bool) {
	checkID := os.Getenv("WIPD_STEP10_VERIFIER_CHECK")
	return checkID, checkID == "focused-step10" ||
		checkID == "changed-package-regressions" || checkID == "repository-check"
}

// step10VerifierInputsProblem reports only a missing input directory.
// step10VerifierExternalInputs still validates the directory contents.
func step10VerifierInputsProblem() string {
	inputDirectory := os.Getenv("WIPD_STEP10_VERIFIER_INPUTS")
	if filepath.IsAbs(inputDirectory) {
		if info, err := os.Stat(inputDirectory); err == nil && info.IsDir() {
			return ""
		}
	}
	return fmt.Sprintf(`WIPD_STEP10_VERIFIER_INPUTS=%q does not name a prepared input directory.
  The Verifier pass reads MODEL.md and one workplan, pinned to exact
  commits of github.com/simensen/wip-reboot (%s and %s).
  Fix: prepare them from a local wip-reboot checkout (default ../wip-reboot):
    make step10-verifier-inputs-local [WIP_REBOOT_DIR=<wip-reboot>]
  then set WIPD_STEP10_VERIFIER_INPUTS to the directory it prints.`,
		inputDirectory, step10VerifierModelCommit, step10VerifierWorkplanCommit)
}

// step10SandboxProblem runs the smallest bubblewrap command that needs the
// same privilege as the Verifier network setup: CAP_NET_ADMIN inside a new
// user and network namespace, used to set loopback state. Hosts that confine
// unprivileged user namespaces (Ubuntu 23.10+ AppArmor, GitHub-hosted
// runners) deny this, and the Verifier then fails with EPERM.
func step10SandboxProblem() string {
	tools := map[string]string{}
	for _, name := range []string{"bwrap", "ip"} {
		found, err := exec.LookPath(name)
		if err == nil {
			found, err = filepath.EvalSymlinks(found)
		}
		if err != nil || !strings.HasPrefix(found, "/nix/store/") {
			return fmt.Sprintf("%s is not on PATH from the Nix store (found %q, error %v).\n"+
				"  Fix: run inside the dev shell (nix develop, or direnv with this repo's .envrc).", name, found, err)
		}
		tools[name] = found
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, tools["bwrap"],
		"--die-with-parent", "--unshare-user", "--unshare-net",
		"--uid", "0", "--gid", "0", "--cap-add", "CAP_NET_ADMIN",
		"--ro-bind", "/nix/store", "/nix/store", "--proc", "/proc", "--dev", "/dev",
		"--", tools["ip"], "link", "set", "dev", "lo", "up")
	probe.Env = []string{"PATH=" + filepath.Dir(tools["bwrap"]), "HOME=/nonexistent"}
	var output bytes.Buffer
	probe.Stdout, probe.Stderr = &output, &output
	err := probe.Run()
	if err == nil {
		return ""
	}
	return fmt.Sprintf(`The bubblewrap sandbox cannot set loopback state in its own network namespace.
  Probe: bwrap --unshare-user --unshare-net --cap-add CAP_NET_ADMIN -- ip link set dev lo up
  Result: %v: %s
  kernel.apparmor_restrict_unprivileged_userns = %s
  Bubblewrap: %s
  Fix: allow user namespaces for Nix bubblewrap. On Ubuntu 23.10+, write
  /etc/apparmor.d/nix-bwrap with:
    abi <abi/4.0>,
    include <tunables/global>
    profile nix-bwrap /nix/store/*-bubblewrap-*/bin/bwrap flags=(unconfined) {
      userns,
    }
  then run: sudo apparmor_parser -r /etc/apparmor.d/nix-bwrap
  Or set kernel.apparmor_restrict_unprivileged_userns=0 (this affects the whole system).`,
		err, strings.TrimSpace(output.String()), step10AppArmorUserNSRestriction(), tools["bwrap"])
}

func step10AppArmorUserNSRestriction() string {
	value, err := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns")
	if err != nil {
		return "unknown (not readable)"
	}
	return strings.TrimSpace(string(value))
}
