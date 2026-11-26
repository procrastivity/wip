package transfer

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Write creates a private (0600) archive, refusing to overwrite any existing
// path, including symlinks. Any error after creation removes the partial file.
// It does not create parent directories or extract any archive paths.
func Write(path string, s *Snapshot) (err error) {
	if err = Validate(s); err != nil {
		return err
	}
	metadata := canonicalMetadata(s)
	manifest, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	z := zip.NewWriter(f)
	put := func(name string, data []byte) error {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o600)
		w, err := z.CreateHeader(h)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, bytes.NewReader(data))
		return err
	}
	if err = put("manifest.json", manifest); err != nil {
		return err
	}
	written := make(map[string]bool)
	for _, c := range metadata.Contents {
		if !written[c.SHA256] {
			if err = put("content/"+c.SHA256, c.Data); err != nil {
				return err
			}
			written[c.SHA256] = true
		}
	}
	if err = z.Close(); err != nil {
		return err
	}
	return f.Sync()
}

// Read verifies a ZIP without extracting files. Only manifest.json and the
// referenced digest-addressed regular content files are allowed. Content is
// held in memory by the Snapshot model; callers must provide sufficient memory
// for the full manifest and uncompressed unique blobs. There are no item/size
// quotas. Length-limited streaming checks prevent expanding beyond the declared
// content lengths; hashes and ZIP checksums are verified before accepting data.
func Read(path string) (*Snapshot, error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = z.Close() }()
	files := make(map[string]*zip.File)
	for _, f := range z.File {
		name := f.Name
		if files[name] != nil {
			return nil, fmt.Errorf("transfer: duplicate ZIP entry %q", name)
		}
		if !f.Mode().IsRegular() || f.Flags&1 != 0 || f.Flags&0x40 != 0 {
			return nil, fmt.Errorf("transfer: non-regular or encrypted ZIP entry %q", name)
		}
		if name != "manifest.json" && (!strings.HasPrefix(name, "content/") || !validHash(strings.TrimPrefix(name, "content/"))) {
			return nil, fmt.Errorf("transfer: unsupported ZIP entry %q", name)
		}
		files[name] = f
	}
	manifest := files["manifest.json"]
	if manifest == nil {
		return nil, fmt.Errorf("transfer: missing manifest.json")
	}
	data, err := readZipFile(manifest, nil)
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := DecodeJSON(data, &s); err != nil {
		return nil, err
	}
	if err := ValidateMetadata(&s); err != nil {
		return nil, err
	}
	delete(files, "manifest.json")
	blobs := make(map[string][]byte)
	for i := range s.Contents {
		c := &s.Contents[i]
		if data, ok := blobs[c.SHA256]; ok {
			c.Data = data
			continue
		}
		name := "content/" + c.SHA256
		f := files[name]
		if f == nil {
			return nil, fmt.Errorf("transfer: missing %s", name)
		}
		if f.UncompressedSize64 != uint64(c.Length) {
			return nil, fmt.Errorf("transfer: content length mismatch for %s", c.ID)
		}
		data, err := readZipFile(f, &c.Length)
		if err != nil {
			return nil, err
		}
		c.Data = data
		blobs[c.SHA256] = data
		delete(files, name)
	}
	if len(files) != 0 {
		return nil, fmt.Errorf("transfer: unreferenced content files")
	}
	if err := Validate(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

func readZipFile(f *zip.File, length *int64) ([]byte, error) {
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	var buf bytes.Buffer
	h := sha256.New()
	w := io.MultiWriter(&buf, h)
	if length == nil {
		_, err = io.Copy(w, r)
	} else {
		// Read at most the promised bytes, then probe EOF to detect expansion
		// and force archive/zip's CRC check, without length+1 overflow.
		var n int64
		n, err = io.CopyN(w, r, *length)
		if err == nil && n == *length {
			var one [1]byte
			var count int
			count, err = r.Read(one[:])
			if count != 0 || err != io.EOF {
				if err == nil {
					err = fmt.Errorf("transfer: content exceeds declared length")
				}
			} else {
				err = nil
			}
		}
		if err == nil && hex.EncodeToString(h.Sum(nil)) != strings.TrimPrefix(f.Name, "content/") {
			err = fmt.Errorf("transfer: corrupt content %s", f.Name)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("transfer: reading %s: %w", f.Name, err)
	}
	return buf.Bytes(), nil
}
