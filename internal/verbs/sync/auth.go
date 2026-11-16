// Package sync implements explicitly invoked, experimental one-pass exchange.
package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

type credentials struct{ Server, Account, Token string }

func credentialPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir, err = filepath.Abs(filepath.Join(dir, "wip", "sync"))
	if err != nil {
		return "", err
	}
	// Resolve every existing ancestor even before the private directory exists.
	// Keep the file leaf unresolved so readPrivate still refuses file symlinks.
	var missing string
	for {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			dir = filepath.Join(resolved, missing)
			break
		}
		if !os.IsNotExist(err) || filepath.Dir(dir) == dir {
			return "", err
		}
		missing = filepath.Join(filepath.Base(dir), missing)
		dir = filepath.Dir(dir)
	}
	path := filepath.Join(dir, "credentials.json")
	if root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		checkout, err := filepath.EvalSymlinks(strings.TrimSpace(string(root)))
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(checkout, path)
		if err != nil {
			return "", err
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("sync: credential storage must be outside the checkout")
		}
	}
	return path, nil
}

// Private input and credentials must be ordinary owner-private files, not
// symlinks. Comparing the opened file closes replacement races at the leaf.
func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !ok || owner.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("sync: credential input must be a private regular file (0600)")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("sync: credential file changed while opening")
	}
	return io.ReadAll(f)
}

func credentialDir() (string, error) {
	path, err := credentialPath()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	checked, err := credentialPath()
	if err != nil {
		return "", err
	}
	if checked != path {
		return "", fmt.Errorf("sync: credential destination changed while creating directory")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ok || owner.Uid != uint32(os.Geteuid()) {
		return "", fmt.Errorf("sync: credentials directory must be private (0700)")
	}
	return path, nil
}

func serverURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("sync: --server must be an explicit HTTP(S) origin")
	}
	return strings.TrimRight(raw, "/"), nil
}

type apiClient struct {
	server, token string
	http          *http.Client
}

type httpStatusError struct {
	status int
	path   string
}

func (e *httpStatusError) Error() string { return fmt.Sprintf("sync: HTTP %d on %s", e.status, e.path) }

func newAPI(server, token string) apiClient {
	return apiClient{server: server, token: token, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c apiClient) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.server+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.http.Jar != nil {
		for _, cookie := range c.http.Jar.Cookies(req.URL) {
			if cookie.Name == "XSRF-TOKEN" {
				token, err := url.QueryUnescape(cookie.Value)
				if err != nil {
					return err
				}
				req.Header.Set("X-XSRF-TOKEN", token)
			}
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sync: server request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpStatusError{status: resp.StatusCode, path: path}
	}
	if output != nil {
		return json.NewDecoder(resp.Body).Decode(output)
	}
	return nil
}

func login(ctx context.Context, name, server, email, passwordFile string, prompt io.Writer) error {
	if _, err := credentialPath(); err != nil {
		return err
	}
	registry, err := loadRegistry()
	if err != nil {
		return err
	}
	before := registry.Connections[name]
	if before.Server != "" && before.Server != server {
		return fmt.Errorf("sync: connection origin is immutable")
	}
	if before.Token != "" {
		return fmt.Errorf("sync: log out before replacing existing credentials")
	}
	var password []byte
	if passwordFile != "" {
		password, err = readPrivate(passwordFile)
	} else {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("sync: use a terminal or --password-file with protected input")
		}
		_, _ = fmt.Fprint(prompt, "Password: ")
		password, err = term.ReadPassword(int(os.Stdin.Fd()))
		_, _ = fmt.Fprintln(prompt)
	}
	if err != nil {
		return err
	}
	defer func() {
		for i := range password {
			password[i] = 0
		}
	}()
	c := newAPI(server, "")
	c.http.Jar, _ = cookiejar.New(nil)
	if err := c.request(ctx, "GET", "/sanctum/csrf-cookie", nil, nil); err != nil {
		return err
	}
	secret := strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r")
	if err := c.request(ctx, "POST", "/login", map[string]string{"email": email, "password": secret}, nil); err != nil {
		return err
	}
	defer func() { _ = c.request(ctx, "POST", "/logout", nil, nil) }()
	if err := c.request(ctx, "POST", "/user/confirm-password", map[string]string{"password": secret}, nil); err != nil {
		return err
	}
	var issued struct {
		Token string `json:"token"`
	}
	if err := c.request(ctx, "POST", "/tokens", map[string]string{"name": "wip explicit sync"}, &issued); err != nil {
		return err
	}
	if issued.Token == "" {
		return fmt.Errorf("sync: token issuance returned no token")
	}
	next := credentials{Server: server, Token: issued.Token}
	if err := next.identify(ctx); err != nil {
		return cleanupLogin(ctx, next, err)
	}
	if before.Account != "" && before.Account != next.Account {
		return cleanupLogin(ctx, next, fmt.Errorf("sync: connection account is immutable"))
	}
	if err := replaceConnection(name, before, next); err != nil {
		return cleanupLogin(ctx, next, err)
	}
	return nil
}

func cleanupLogin(ctx context.Context, c credentials, cause error) error {
	if err := newAPI(c.Server, c.Token).request(ctx, "DELETE", "/api/token", nil, nil); err != nil {
		return fmt.Errorf("%w; newly issued token cleanup failed; revoke it through the account", cause)
	}
	return cause
}
