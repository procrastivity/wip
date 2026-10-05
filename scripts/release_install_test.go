// Package scripts tests binary bootstrap independently of harness projection
// and the store. The fake network follows toolsmith v0.0.3's installer tests.
package scripts

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseInstaller(t *testing.T) {
	tests := []struct {
		name       string
		goos       string
		goarch     string
		version    string
		asset      string
		existing   string
		checksum   string
		missing    string
		dir        string
		shasumOnly bool
		wantError  string
	}{
		{name: "latest linux amd64", goos: "Linux", goarch: "x86_64", asset: "wip-linux-amd64"},
		{name: "pinned darwin arm64 with shasum", goos: "Darwin", goarch: "arm64", version: "v1.2.3", asset: "wip-darwin-arm64", shasumOnly: true},
		{name: "linux amd64 uname alias", goos: "Linux", goarch: "amd64", asset: "wip-linux-amd64"},
		{name: "default HOME destination", goos: "Linux", goarch: "x86_64", asset: "wip-linux-amd64", dir: "default"},
		{name: "relative destination", goos: "Linux", goarch: "x86_64", asset: "wip-linux-amd64", dir: "relative"},
		{name: "replace regular binary", goos: "Linux", goarch: "x86_64", asset: "wip-linux-amd64", existing: "regular"},
		{name: "replace symlink without writing through it", goos: "Linux", goarch: "x86_64", asset: "wip-linux-amd64", existing: "symlink"},
		{name: "replace dangling symlink", goos: "Linux", goarch: "x86_64", asset: "wip-linux-amd64", existing: "dangling"},
		{name: "checksum mismatch leaves destination absent", goos: "Linux", goarch: "x86_64", asset: "wip-linux-amd64", checksum: "bad", wantError: "checksum verification failed"},
		{name: "checksum mismatch preserves regular binary", goos: "Linux", goarch: "x86_64", asset: "wip-linux-amd64", checksum: "bad", existing: "regular", wantError: "checksum verification failed"},
		{name: "checksum mismatch preserves symlink and target", goos: "Linux", goarch: "x86_64", asset: "wip-linux-amd64", checksum: "bad", existing: "symlink", wantError: "checksum verification failed"},
		{name: "missing checksum entry", goos: "Linux", goarch: "x86_64", asset: "wip-linux-amd64", checksum: "missing", existing: "symlink", wantError: "SHA256SUMS has no entry"},
		{name: "missing pinned release never falls back to latest", goos: "Linux", goarch: "x86_64", version: "v99.99.99", asset: "wip-linux-amd64", missing: "wip-linux-amd64", existing: "symlink", wantError: "download failed for wip-linux-amd64"},
		{name: "checksum download failure", goos: "Linux", goarch: "x86_64", asset: "wip-linux-amd64", missing: "SHA256SUMS", existing: "symlink", wantError: "download failed for SHA256SUMS"},
		{name: "directory destination refused", goos: "Linux", goarch: "x86_64", asset: "wip-linux-amd64", existing: "directory", wantError: "install destination is a directory"},
		{name: "unsupported platform before network", goos: "FreeBSD", goarch: "amd64", wantError: "unsupported platform"},
		{name: "unsupported Linux architecture before network", goos: "Linux", goarch: "aarch64", wantError: "unsupported Linux architecture"},
		{name: "unsupported macOS architecture before network", goos: "Darwin", goarch: "x86_64", wantError: "unsupported macOS architecture"},
		{name: "Windows before network", goos: "MINGW64_NT", goarch: "x86_64", wantError: "Windows is not supported"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			binDir := filepath.Join(root, "bin")
			fixtureDir := filepath.Join(root, "fixtures")
			tmpDir := filepath.Join(root, "tmp")
			home := filepath.Join(root, "home")
			installDir := filepath.Join(root, "install with spaces")
			installEnv := installDir
			switch tt.dir {
			case "default":
				installDir = filepath.Join(home, ".local", "bin")
				installEnv = ""
			case "relative":
				installEnv = "relative install"
				installDir = filepath.Join(root, installEnv)
			}
			for _, dir := range []string{binDir, fixtureDir, tmpDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			// Restrict PATH to known utilities so the macOS checksum fallback
			// is actually exercised even on a host with sha256sum installed.
			utilities := []string{"cp", "grep", "mktemp", "rm", "chmod", "mv", "mkdir"}
			if tt.shasumOnly {
				utilities = append(utilities, "shasum")
			} else {
				utilities = append(utilities, "sha256sum")
			}
			for _, name := range utilities {
				path, err := exec.LookPath(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path, filepath.Join(binDir, name)); err != nil {
					t.Fatal(err)
				}
			}

			binary := []byte("#!/bin/sh\nprintf '%s\\n' 'release fixture: " + tt.asset + "'\n")
			if tt.asset != "" {
				if tt.missing != tt.asset {
					writeFile(t, filepath.Join(fixtureDir, tt.asset), string(binary), 0o644)
				}
				sum := sha256.Sum256(binary)
				if tt.checksum == "bad" {
					sum[0] ^= 0xff
				}
				checksums := fmt.Sprintf("%x  %s\n", sum, tt.asset)
				if tt.checksum == "missing" {
					checksums = fmt.Sprintf("%x  different-asset\n", sum)
				}
				if tt.missing != "SHA256SUMS" {
					writeFile(t, filepath.Join(fixtureDir, "SHA256SUMS"), checksums, 0o644)
				}
			}

			logPath := filepath.Join(root, "curl.log")
			writeFile(t, filepath.Join(binDir, "curl"), `#!/bin/sh
set -eu
[ "$1" = -fsSL ] && [ "$3" = -o ]
url=$2
out=$4
printf '%s\n' "$url" >> "$CURL_LOG"
cp "$FIXTURE_DIR/${url##*/}" "$out"
`, 0o755)
			writeFile(t, filepath.Join(binDir, "uname"), `#!/bin/sh
case "${1:-}" in
-s) printf '%s\n' "$FAKE_UNAME_S" ;;
-m) printf '%s\n' "$FAKE_UNAME_M" ;;
*) exit 2 ;;
esac
`, 0o755)

			destination := filepath.Join(installDir, "wip")
			target := filepath.Join(root, "development-build")
			const oldBinary = "development binary must remain unchanged\n"
			if tt.existing != "" {
				if err := os.MkdirAll(installDir, 0o755); err != nil {
					t.Fatal(err)
				}
				switch tt.existing {
				case "regular":
					writeFile(t, destination, oldBinary, 0o755)
				case "directory":
					if err := os.Mkdir(destination, 0o755); err != nil {
						t.Fatal(err)
					}
				case "symlink", "dangling":
					if tt.existing == "symlink" {
						writeFile(t, target, oldBinary, 0o755)
					}
					if err := os.Symlink(target, destination); err != nil {
						t.Fatal(err)
					}
				}
			}

			_, file, _, _ := runtime.Caller(0)
			installer := filepath.Join(filepath.Dir(file), "install.sh")
			cmd := exec.Command("/bin/sh", installer)
			cmd.Dir = root
			cmd.Env = append(os.Environ(),
				"PATH="+binDir,
				"HOME="+home,
				"CURL_LOG="+logPath,
				"FIXTURE_DIR="+fixtureDir,
				"TMPDIR="+tmpDir,
				"WIP_INSTALL_DIR="+installEnv,
				"WIP_BASE_URL=https://github.com/example/wip///",
				"WIP_VERSION="+tt.version,
				"FAKE_UNAME_S="+tt.goos,
				"FAKE_UNAME_M="+tt.goarch,
			)
			output, err := cmd.CombinedOutput()
			if tt.wantError != "" {
				if err == nil || !strings.Contains(string(output), tt.wantError) {
					t.Fatalf("wanted failure containing %q, err=%v output=%s", tt.wantError, err, output)
				}
				if strings.Contains(string(output), "Installed wip") {
					t.Fatalf("failed installation reported success: %s", output)
				}
			} else if err != nil {
				t.Fatalf("installer failed: %v\n%s", err, output)
			}

			var wantURLs []string
			if tt.asset != "" {
				prefix := "https://github.com/example/wip/releases/latest/download"
				if tt.version != "" {
					prefix = "https://github.com/example/wip/releases/download/" + tt.version
				}
				wantURLs = append(wantURLs, prefix+"/"+tt.asset)
				if tt.missing != tt.asset {
					wantURLs = append(wantURLs, prefix+"/SHA256SUMS")
				}
			}
			gotLog, err := os.ReadFile(logPath)
			if err != nil && (!os.IsNotExist(err) || len(wantURLs) > 0) {
				t.Fatal(err)
			}
			if strings.Join(strings.Fields(string(gotLog)), "\n") != strings.Join(wantURLs, "\n") {
				t.Fatalf("curl URLs = %q, want ordered %v", gotLog, wantURLs)
			}

			if tt.wantError == "" {
				assertFile(t, destination, string(binary))
				info, err := os.Lstat(destination)
				if err != nil {
					t.Fatal(err)
				}
				if !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
					t.Fatalf("installed binary must be a regular executable, got %v", info.Mode())
				}
				out, err := exec.Command(destination).CombinedOutput()
				if err != nil || string(out) != "release fixture: "+tt.asset+"\n" {
					t.Fatalf("installed executable: output=%q err=%v", out, err)
				}
			} else {
				switch tt.existing {
				case "regular":
					assertFile(t, destination, oldBinary)
				case "symlink", "dangling":
					link, err := os.Readlink(destination)
					if err != nil || link != target {
						t.Fatalf("failure changed destination symlink: link=%q err=%v", link, err)
					}
				case "directory":
					entries, err := os.ReadDir(destination)
					if err != nil || len(entries) != 0 {
						t.Fatalf("failure changed destination directory: entries=%v err=%v", entries, err)
					}
				default:
					if _, err := os.Lstat(destination); !os.IsNotExist(err) {
						t.Fatalf("failed install created destination: %v", err)
					}
				}
			}
			switch tt.existing {
			case "symlink":
				assertFile(t, target, oldBinary)
			case "dangling":
				if _, err := os.Lstat(target); !os.IsNotExist(err) {
					t.Fatalf("installation wrote through dangling symlink: %v", err)
				}
			}
			for _, dir := range []string{tmpDir, installDir} {
				entries, err := os.ReadDir(dir)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), "wip-install.") || strings.HasPrefix(entry.Name(), ".wip.") {
						t.Errorf("temporary installer file remains: %s/%s", dir, entry.Name())
					}
				}
			}
		})
	}
}

// Exercise the actual workflow's packaging shell commands in a fixture tree.
// A fake gh records the publish arguments; no release or network write occurs.
func TestReleaseInstallerPackaging(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	repo := filepath.Dir(filepath.Dir(file))
	data, err := os.ReadFile(filepath.Join(repo, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name             string `yaml:"name"`
				Run              string `yaml:"run"`
				WorkingDirectory string `yaml:"working-directory"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, dir := range []string{"scripts", "dist", "bin"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	installer, err := os.ReadFile(filepath.Join(repo, "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "scripts", "install.sh"), string(installer), 0o755)
	writeFile(t, filepath.Join(root, "dist", "wip-darwin-arm64"), "darwin binary fixture", 0o755)
	writeFile(t, filepath.Join(root, "dist", "wip-linux-amd64"), "linux binary fixture", 0o755)
	writeFile(t, filepath.Join(root, "dist", "CHANGELOG.md"), "changelog fixture", 0o644)
	writeFile(t, filepath.Join(root, "dist", "RELEASE_NOTES.md"), "release body, not an asset", 0o644)
	logPath := filepath.Join(root, "gh.log")
	writeFile(t, filepath.Join(root, "bin", "gh"), "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GH_LOG\"\n", 0o755)
	var ran []string
	for _, step := range workflow.Jobs["release"].Steps {
		switch step.Name {
		case "installer", "checksums", "publish":
			ran = append(ran, step.Name)
		default:
			continue
		}
		cmd := exec.Command("/bin/sh", "-eu", "-c", strings.ReplaceAll(step.Run, "${{ github.ref_name }}", "v9.8.7"))
		cmd.Dir = filepath.Join(root, step.WorkingDirectory)
		cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"), "GH_LOG="+logPath, "GH_TOKEN=")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("workflow step %s: %v\n%s", step.Name, err, out)
		}
	}
	if strings.Join(ran, ",") != "installer,checksums,publish" {
		t.Fatalf("packaging step order = %v", ran)
	}
	assertFile(t, filepath.Join(root, "dist", "wip-install.sh"), string(installer))
	darwinSum := sha256.Sum256([]byte("darwin binary fixture"))
	linuxSum := sha256.Sum256([]byte("linux binary fixture"))
	assertFile(t, filepath.Join(root, "dist", "SHA256SUMS"), fmt.Sprintf("%x  wip-darwin-arm64\n%x  wip-linux-amd64\n", darwinSum, linuxSum))
	assertFile(t, logPath, strings.Join([]string{
		"release", "create", "v9.8.7", "--title", "v9.8.7", "--notes-file", "dist/RELEASE_NOTES.md",
		"dist/wip-darwin-arm64", "dist/wip-linux-amd64", "dist/wip-install.sh", "dist/SHA256SUMS", "dist/CHANGELOG.md", "",
	}, "\n"))
}

func writeFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s: bytes=%q err=%v, want %q", path, got, err, want)
	}
}
