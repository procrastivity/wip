package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

var connectionName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

type connectionRegistry struct {
	Version     int
	Connections map[string]credentials
}

// Read-only operations interpret the old file without migrating or doing HTTP.
func loadRegistry() (connectionRegistry, error) {
	r := connectionRegistry{Version: 1, Connections: map[string]credentials{}}
	path, err := credentialPath()
	if err != nil {
		return r, err
	}
	raw, err := readPrivate(path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	var old credentials
	if err := json.Unmarshal(raw, &old); err != nil {
		return r, fmt.Errorf("sync: invalid credential registry")
	}
	if old.Server != "" {
		r.Connections["default"] = old
	} else if json.Unmarshal(raw, &r) != nil || r.Version != 1 || r.Connections == nil {
		return r, fmt.Errorf("sync: unsupported credential registry")
	}
	for name, c := range r.Connections {
		origin, err := serverURL(c.Server)
		if !connectionName.MatchString(name) || err != nil || origin != c.Server {
			return r, fmt.Errorf("sync: invalid saved connection")
		}
	}
	return r, nil
}

// Hold an OS-local lock only over the read/compare/atomic-replace operation,
// never while prompting or making HTTP requests. Other connections are retained.
func replaceConnection(name string, before, next credentials) error {
	if !connectionName.MatchString(name) {
		return fmt.Errorf("sync: invalid connection name")
	}
	return withRegistryLock(func(path string) error {
		if err := checkResetPending(name); err != nil {
			return err
		}
		r, err := loadRegistry()
		if err != nil {
			return err
		}
		if r.Connections[name] != before {
			return fmt.Errorf("sync: connection changed concurrently; retry with current credentials")
		}
		if before.Server != "" && (before.Server != next.Server || (before.Account != "" && before.Account != next.Account)) {
			return fmt.Errorf("sync: connection origin/account is immutable")
		}
		r.Connections[name] = next
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		return atomicPrivate(path, raw)
	})
}

func withRegistryLock(fn func(string) error) error {
	path, err := credentialDir()
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	info, err := lock.Stat()
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !ok || owner.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("sync: credential lock must be owner-private")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	return fn(path)
}

func atomicPrivate(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".credentials-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

func (c *credentials) identify(ctx context.Context) error {
	var me struct {
		ID json.Number `json:"id"`
	}
	if err := newAPI(c.Server, c.Token).request(ctx, "GET", "/api/me", nil, &me); err != nil {
		return err
	}
	if me.ID == "" {
		return fmt.Errorf("sync: server did not supply account identity")
	}
	if c.Account != "" && c.Account != me.ID.String() {
		return fmt.Errorf("sync: authenticated account differs from saved identity")
	}
	c.Account = me.ID.String()
	return nil
}

func loadConnection(name, server string) (credentials, error) {
	r, err := loadRegistry()
	if err != nil {
		return credentials{}, err
	}
	c, ok := r.Connections[name]
	if !ok || c.Token == "" {
		return c, fmt.Errorf("sync: login required for connection %q", name)
	}
	if server != "" {
		origin, err := serverURL(server)
		if err != nil {
			return c, err
		}
		if origin != c.Server {
			return c, fmt.Errorf("sync: credentials belong to another server")
		}
	}
	return c, nil
}
