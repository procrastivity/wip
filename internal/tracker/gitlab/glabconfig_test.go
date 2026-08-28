package gitlab

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// glabFixture mirrors a real glab CLI config.yml: a comment, two hosts, one
// with an empty token (as gitlab.com ships) and one with a token padded with
// whitespace and mixed-case host name.
const glabFixture = `# comment
hosts:
    gitlab.com:
        api_protocol: https
        token:
    GitLab.Example.COM:
        token: "  glpat-example  "
        api_host: gitlab.example.com
        user: someone
`

// isolateGlabEnv resets all three environment variables glabConfigPath
// consults so a test never reads the real $HOME or ~/.config/glab-cli.
// HOME is pointed at a fresh temp directory; callers set whichever variable
// they want to test on top of this.
func isolateGlabEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GLAB_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", t.TempDir())
}

// writeGlabConfig writes content to <dir>/config.yml, creating dir if
// needed, and returns dir.
func writeGlabConfig(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return dir
}

// TestGlabTokenPrefersGlabConfigDir checks that GLAB_CONFIG_DIR wins over
// XDG_CONFIG_HOME when both are set.
func TestGlabTokenPrefersGlabConfigDir(t *testing.T) {
	isolateGlabEnv(t)
	a := writeGlabConfig(t, t.TempDir(), glabFixture)
	t.Setenv("GLAB_CONFIG_DIR", a)

	xdg := t.TempDir()
	writeGlabConfig(t, filepath.Join(xdg, "glab-cli"), `hosts:
    gitlab.example.com:
        token: should-not-be-used
`)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	token, err := glabToken("gitlab.example.com")
	if err != nil {
		t.Fatalf("glabToken: %v", err)
	}
	if token != "glpat-example" {
		t.Errorf("token = %q, want %q", token, "glpat-example")
	}
}

// TestGlabTokenUsesXDGWhenGlabConfigDirUnset checks that XDG_CONFIG_HOME is
// used when GLAB_CONFIG_DIR is not set.
func TestGlabTokenUsesXDGWhenGlabConfigDirUnset(t *testing.T) {
	isolateGlabEnv(t)
	x := t.TempDir()
	writeGlabConfig(t, filepath.Join(x, "glab-cli"), glabFixture)
	t.Setenv("XDG_CONFIG_HOME", x)

	token, err := glabToken("gitlab.example.com")
	if err != nil {
		t.Fatalf("glabToken: %v", err)
	}
	if token != "glpat-example" {
		t.Errorf("token = %q, want %q", token, "glpat-example")
	}
}

// TestGlabTokenFallsBackToHome checks that $HOME/.config/glab-cli is used
// when neither GLAB_CONFIG_DIR nor XDG_CONFIG_HOME is set.
func TestGlabTokenFallsBackToHome(t *testing.T) {
	isolateGlabEnv(t)
	h := t.TempDir()
	writeGlabConfig(t, filepath.Join(h, ".config", "glab-cli"), glabFixture)
	t.Setenv("HOME", h)

	token, err := glabToken("gitlab.example.com")
	if err != nil {
		t.Fatalf("glabToken: %v", err)
	}
	if token != "glpat-example" {
		t.Errorf("token = %q, want %q", token, "glpat-example")
	}
}

// TestGlabTokenSetDirDoesNotFallThrough checks that a set GLAB_CONFIG_DIR
// with no config.yml under it does not fall through to XDG_CONFIG_HOME.
func TestGlabTokenSetDirDoesNotFallThrough(t *testing.T) {
	isolateGlabEnv(t)
	empty := t.TempDir()
	t.Setenv("GLAB_CONFIG_DIR", empty)

	xdg := t.TempDir()
	writeGlabConfig(t, filepath.Join(xdg, "glab-cli"), glabFixture)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	token, err := glabToken("gitlab.example.com")
	if err != nil {
		t.Fatalf("glabToken: %v", err)
	}
	if token != "" {
		t.Errorf("token = %q, want empty", token)
	}
}

// TestGlabTokenMissingFileIsEmpty checks that a missing config.yml under an
// otherwise valid directory returns "", nil rather than an error.
func TestGlabTokenMissingFileIsEmpty(t *testing.T) {
	isolateGlabEnv(t)
	t.Setenv("GLAB_CONFIG_DIR", t.TempDir())

	token, err := glabToken("gitlab.example.com")
	if err != nil {
		t.Fatalf("glabToken: %v", err)
	}
	if token != "" {
		t.Errorf("token = %q, want empty", token)
	}
}

// TestGlabTokenUnknownHostIsEmpty checks that a host absent from the config
// returns "", nil.
func TestGlabTokenUnknownHostIsEmpty(t *testing.T) {
	isolateGlabEnv(t)
	dir := writeGlabConfig(t, t.TempDir(), glabFixture)
	t.Setenv("GLAB_CONFIG_DIR", dir)

	token, err := glabToken("other.example.com")
	if err != nil {
		t.Fatalf("glabToken: %v", err)
	}
	if token != "" {
		t.Errorf("token = %q, want empty", token)
	}
}

// TestGlabTokenEmptyTokenIsEmpty checks that a host with an explicitly empty
// token (as gitlab.com ships) returns "", nil.
func TestGlabTokenEmptyTokenIsEmpty(t *testing.T) {
	isolateGlabEnv(t)
	dir := writeGlabConfig(t, t.TempDir(), glabFixture)
	t.Setenv("GLAB_CONFIG_DIR", dir)

	token, err := glabToken("gitlab.com")
	if err != nil {
		t.Fatalf("glabToken: %v", err)
	}
	if token != "" {
		t.Errorf("token = %q, want empty", token)
	}
}

// TestGlabTokenMatchesHostCaseInsensitively checks that a lookup host is
// matched against a differently-cased host key in the config.
func TestGlabTokenMatchesHostCaseInsensitively(t *testing.T) {
	isolateGlabEnv(t)
	dir := writeGlabConfig(t, t.TempDir(), glabFixture)
	t.Setenv("GLAB_CONFIG_DIR", dir)

	token, err := glabToken("gitlab.example.com")
	if err != nil {
		t.Fatalf("glabToken: %v", err)
	}
	if token != "glpat-example" {
		t.Errorf("token = %q, want %q", token, "glpat-example")
	}
}

// TestGlabTokenTrimsWhitespace checks that surrounding whitespace in the
// YAML token value is trimmed from the returned token.
func TestGlabTokenTrimsWhitespace(t *testing.T) {
	isolateGlabEnv(t)
	dir := writeGlabConfig(t, t.TempDir(), glabFixture)
	t.Setenv("GLAB_CONFIG_DIR", dir)

	token, err := glabToken("GitLab.Example.COM")
	if err != nil {
		t.Fatalf("glabToken: %v", err)
	}
	if strings.TrimSpace(token) != token {
		t.Errorf("token = %q, want no surrounding whitespace", token)
	}
	if token != "glpat-example" {
		t.Errorf("token = %q, want %q", token, "glpat-example")
	}
}

// TestGlabTokenMalformedYAMLIsAnError checks that malformed YAML surfaces as
// an error carrying the "gitlab tracker: glab config:" prefix.
func TestGlabTokenMalformedYAMLIsAnError(t *testing.T) {
	isolateGlabEnv(t)
	dir := writeGlabConfig(t, t.TempDir(), "hosts: [unclosed")
	t.Setenv("GLAB_CONFIG_DIR", dir)

	_, err := glabToken("gitlab.example.com")
	if err == nil {
		t.Fatal("glabToken: want error, got nil")
	}
	if !strings.Contains(err.Error(), "gitlab tracker: glab config:") {
		t.Errorf("err = %q, want it to contain %q", err.Error(), "gitlab tracker: glab config:")
	}
}

// TestGlabTokenUnreadableFileIsAnError checks that a config.yml the process
// cannot read surfaces as an error other than os.ErrNotExist.
func TestGlabTokenUnreadableFileIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file permissions do not block reads")
	}
	isolateGlabEnv(t)
	dir := writeGlabConfig(t, t.TempDir(), glabFixture)
	t.Setenv("GLAB_CONFIG_DIR", dir)

	path := filepath.Join(dir, "config.yml")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatalf("restore chmod %s: %v", path, err)
		}
	})

	_, err := glabToken("gitlab.example.com")
	if err == nil {
		t.Fatal("glabToken: want error, got nil")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want not os.ErrNotExist", err)
	}
}
