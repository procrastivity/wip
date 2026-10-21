// Package main implements the offline D115 operator trust-bundle commands.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/procrastivity/wip/internal/authoritystore"
	"github.com/procrastivity/wip/internal/d115"
	"github.com/procrastivity/wip/internal/wipdseed"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: wip-d115-trust export|install [flags]")
	}
	switch os.Args[1] {
	case "export":
		return exportBundle(os.Args[2:])
	case "install":
		return installBundle(os.Args[2:])
	default:
		return errors.New("unknown wip-d115-trust command")
	}
}

func exportBundle(args []string) error {
	flags := flag.NewFlagSet("export", flag.ContinueOnError)
	root := flags.String("authority-root", "", "existing private authority root")
	out := flags.String("out", "", "new bundle path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *root == "" || *out == "" {
		return errors.New("export requires --authority-root and --out")
	}
	tty, err := openControllingTTY()
	if err != nil {
		return err
	}
	defer func() { _ = tty.Close() }()
	store, err := authoritystore.OpenExisting(*root)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	data, err := store.ExportD115OperatorTrustBundle(context.Background())
	if err != nil {
		return err
	}
	defer clear(data)
	bundle, err := d115.DecodeOperatorTrustBundle(data)
	if err != nil {
		return err
	}
	printBundle(tty, bundle, wipdseed.D115BundleDigest(data))
	if _, err = fmt.Fprintln(tty, "Confirm public trust snapshot export? Type EXPORT:"); err != nil {
		return err
	}
	answer, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil || strings.TrimSpace(answer) != "EXPORT" {
		return errors.New("trust-bundle.export-refused")
	}
	if err = wipdseed.WriteD115OwnerFile(*out, data); err != nil {
		return err
	}
	return nil
}

func installBundle(args []string) error {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	directory := flags.String("bundle-store", "", "existing owner-private bundle-store directory")
	file := flags.String("bundle-file", "", "owner-transferred bundle file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *directory == "" || *file == "" {
		return errors.New("install requires --bundle-store and --bundle-file")
	}
	tty, err := openControllingTTY()
	if err != nil {
		return err
	}
	defer func() { _ = tty.Close() }()
	data, err := wipdseed.ReadD115OwnerFile(*file, 1<<20)
	if err != nil {
		return err
	}
	defer clear(data)
	bundle, err := d115.DecodeOperatorTrustBundle(data)
	if err != nil {
		return err
	}
	printBundleDetails(tty, bundle)
	reader := bufio.NewReader(tty)
	if _, err = fmt.Fprintf(tty, "Type the exact realm %s:\n", bundle.RealmID); err != nil {
		return err
	}
	realm, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(realm) != bundle.RealmID {
		return errors.New("trust-bundle.confirmation-mismatch")
	}
	if _, err = fmt.Fprintln(tty, "Type the SHA-256 received with the bundle from the independently confirmed export:"); err != nil {
		return err
	}
	typed, err := reader.ReadString('\n')
	if err != nil {
		return errors.New("trust-bundle.confirmation-mismatch")
	}
	return wipdseed.InstallD115OperatorTrustBundle(*directory, data, bundle.RealmID, strings.TrimSpace(typed))
}

func printBundle(out *os.File, bundle d115.OperatorTrustBundle, digest string) {
	_, _ = fmt.Fprintf(out, "bundle_sha256: %s\nrealm: %s\ngeneration: %d\nenabled: %t\n", digest, bundle.RealmID, bundle.Generation, bundle.Enabled)
	printBundleKeys(out, bundle)
}

func printBundleDetails(out *os.File, bundle d115.OperatorTrustBundle) {
	_, _ = fmt.Fprintf(out, "realm: %s\ngeneration: %d\nenabled: %t\n", bundle.RealmID, bundle.Generation, bundle.Enabled)
	printBundleKeys(out, bundle)
}

func printBundleKeys(out *os.File, bundle d115.OperatorTrustBundle) {
	for _, key := range bundle.Keys {
		_, _ = fmt.Fprintf(out, "key_id: %s fingerprint: %s state: %s scope: %s audience: %s validity: %s..%s\n", key.KeyID, key.KeyID, key.State, key.Scope, key.Audience, key.NotBefore, key.NotAfter)
	}
}

func openControllingTTY() (*os.File, error) {
	file, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, errors.New("controlling TTY required")
	}
	return file, nil
}
