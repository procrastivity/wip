package authoritystore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// All names are derived from validated IDs/digests, never from a supplied path.
// Staging files are not authority truth. A committed chunk row names exactly
// one durable file; a verified row names a separately fsynced full product.
func chunkName(domain, digest string, offset uint64, sum []byte) string {
	return "s_" + domain + "_" + digest[7:] + "_" + strconv.FormatUint(offset, 10) + "_" + hex.EncodeToString(sum)
}

func productName(digest string) string { return "v_" + digest[7:] }

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

func initBlobDir(root string) error {
	dir := filepath.Join(root, "blobs")
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := privateBlobDir(dir); err != nil {
		return err
	}
	return syncDir(root)
}

func privateBlobDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return ErrInvalidStore
	}
	return nil
}

func readChunk(dir, name string, length uint64, sum []byte) ([]byte, error) {
	path := filepath.Join(dir, name)
	if err := regularFile(path); err != nil {
		return nil, fmt.Errorf("%w: staged chunk: %v", ErrInvalidStore, err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Size() != int64(length) {
		return nil, ErrInvalidStore
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(b)
	if uint64(len(b)) != length || !bytes.Equal(h[:], sum) {
		return nil, ErrInvalidStore
	}
	return b, nil
}

// linkDurable writes a private temporary file, syncs it, publishes without
// replacing an existing immutable path, then syncs the containing directory.
// A response/DB failure may leave an orphan, which collection can remove.
func linkDurable(dir, name string, write func(*os.File) error, verifyExisting func() error) error {
	f, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err = write(f); err == nil {
		err = f.Chmod(0o400)
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if err = os.Link(f.Name(), filepath.Join(dir, name)); errors.Is(err, os.ErrExist) {
		if err = verifyExisting(); err != nil {
			return err
		}
		return syncDir(dir)
	} else if err != nil {
		return err
	}
	return syncDir(dir)
}

func durableChunk(dir, domain, digest string, offset uint64, b []byte) ([]byte, error) {
	h := sha256.Sum256(b)
	name := chunkName(domain, digest, offset, h[:])
	err := linkDurable(dir, name, func(f *os.File) error {
		_, e := f.Write(b)
		return e
	}, func() error {
		_, e := readChunk(dir, name, uint64(len(b)), h[:])
		return e
	})
	return h[:], err
}

func hashChunks(ctx context.Context, q *sql.Tx, dir, domain, digest string, length uint64, h hash.Hash, dest io.Writer) error {
	rows, err := q.QueryContext(ctx, `SELECT offset,chunk_hash,byte_length FROM blob_chunks WHERE domain_id=? AND digest=? ORDER BY offset`, domain, digest)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var at uint64
	for rows.Next() {
		var off, size uint64
		var sum []byte
		if err = rows.Scan(&off, &sum, &size); err != nil {
			return err
		}
		if off != at || size == 0 || size > maxBlobChunk || at > length || size > length-at {
			return ErrInvalidStore
		}
		b, err := readChunk(dir, chunkName(domain, digest, off, sum), size, sum)
		if err != nil {
			return err
		}
		if _, err = h.Write(b); err != nil {
			return err
		}
		if dest != nil {
			if _, err = dest.Write(b); err != nil {
				return err
			}
		}
		at += size
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if at != length {
		return ErrInvalidStore
	}
	return nil
}

func verifyProduct(dir, digest string, length uint64) error {
	path := filepath.Join(dir, productName(digest))
	if err := regularFile(path); err != nil {
		return fmt.Errorf("%w: verified blob: %v", ErrInvalidStore, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.Size() != int64(length) || info.Mode().Perm() != 0o400 {
		return ErrInvalidStore
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return err
	}
	if digestRawBytes(h.Sum(nil)) != digest {
		return ErrInvalidStore
	}
	return nil
}

func durableProduct(ctx context.Context, tx *sql.Tx, dir, domain, digest string, length uint64) error {
	return linkDurable(dir, productName(digest), func(f *os.File) error {
		h := sha256.New()
		if err := hashChunks(ctx, tx, dir, domain, digest, length, h, f); err != nil {
			return err
		}
		if digestRawBytes(h.Sum(nil)) != digest {
			return ErrBlobDigest
		}
		return nil
	}, func() error { return verifyProduct(dir, digest, length) })
}

// Open validates every DB-named byte, including incomplete staged evidence;
// extra unreferenced files are left untouched for explicit collection.
func checkBlobFiles(db *sql.DB, dir string) error {
	if err := privateBlobDir(dir); err != nil {
		return err
	}
	rows, err := db.Query(`SELECT domain_id,digest,byte_length,verified_offset,verified FROM blob_products`)
	if err != nil {
		return err
	}
	type product struct {
		domain, digest string
		length, offset uint64
		verified       int
	}
	var products []product
	for rows.Next() {
		var p product
		if err = rows.Scan(&p.domain, &p.digest, &p.length, &p.offset, &p.verified); err != nil {
			break
		}
		products = append(products, p)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, p := range products {
		if !ulid.MatchString(p.domain) || !validDigest(p.digest) || p.offset > p.length {
			return ErrInvalidStore
		}
		if p.verified == 1 {
			if p.offset != p.length || verifyProduct(dir, p.digest, p.length) != nil {
				return ErrInvalidStore
			}
			var n int
			if err = db.QueryRow(`SELECT count(*) FROM blob_chunks WHERE domain_id=? AND digest=?`, p.domain, p.digest).Scan(&n); err != nil || n != 0 {
				return ErrInvalidStore
			}
		} else {
			tx, e := db.Begin()
			if e != nil {
				return e
			}
			e = hashChunks(context.Background(), tx, dir, p.domain, p.digest, p.offset, sha256.New(), nil)
			_ = tx.Rollback()
			if e != nil {
				return e
			}
		}
	}
	return nil
}

func collectBlobFiles(db *sql.DB, dir string) error {
	keep := make(map[string]bool)
	rows, err := db.Query(`SELECT domain_id,digest,offset,chunk_hash FROM blob_chunks`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var domain, digest string
		var offset uint64
		var sum []byte
		if err = rows.Scan(&domain, &digest, &offset, &sum); err != nil {
			break
		}
		keep[chunkName(domain, digest, offset, sum)] = true
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	rows, err = db.Query(`SELECT DISTINCT digest FROM blob_products WHERE verified=1`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var digest string
		if err = rows.Scan(&digest); err != nil {
			break
		}
		keep[productName(digest)] = true
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if keep[name] || (!strings.HasPrefix(name, ".tmp-") && !strings.HasPrefix(name, "s_") && !strings.HasPrefix(name, "v_")) {
			continue
		}
		if !entry.Type().IsRegular() {
			return ErrInvalidStore
		}
		if err = os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return syncDir(dir)
}
