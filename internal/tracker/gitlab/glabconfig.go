package gitlab

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// glabConfigPath returns the config.yml path the glab CLI would read. The
// first environment variable that is set decides the directory; a set
// variable never falls through to the next one, so a missing file under an
// explicit GLAB_CONFIG_DIR means "no token", not "try XDG".
func glabConfigPath() string {
	if dir := strings.TrimSpace(os.Getenv("GLAB_CONFIG_DIR")); dir != "" {
		return filepath.Join(dir, "config.yml")
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "glab-cli", "config.yml")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "glab-cli", "config.yml")
}

// glabToken reads hosts.<host>.token from the glab CLI config. A missing
// file, an unknown host, or an empty token all return "" with a nil error;
// only an unreadable file or malformed YAML is an error.
func glabToken(host string) (string, error) {
	path := glabConfigPath()
	if path == "" {
		return "", nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("gitlab tracker: glab config: %w", err)
	}
	var config struct {
		Hosts map[string]struct {
			Token string `yaml:"token"`
		} `yaml:"hosts"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return "", fmt.Errorf("gitlab tracker: glab config: parse %s: %w", path, err)
	}
	want := strings.ToLower(strings.TrimSpace(host))
	for name, entry := range config.Hosts {
		if strings.ToLower(strings.TrimSpace(name)) == want {
			return strings.TrimSpace(entry.Token), nil
		}
	}
	return "", nil
}
