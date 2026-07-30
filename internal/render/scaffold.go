package render

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// excludeLine is the entry appended to `.git/info/exclude` — D41 verbatim:
// "excluded per-clone via `.git/info/exclude` … never a tracked `.gitignore`".
const excludeLine = ".wip/"

// EnsureLayout implements step-01: on first render (or `wip init`) in a
// worktree lacking `.wip/`, both halves are created and `.wip/` is excluded
// per-clone — idempotent, so a repeat call never duplicates the exclude line
// or recreates existing directories destructively.
//
// gitCommonDir is the Clone's own natural key (D37) — `.git/info/exclude`
// lives there, shared by every worktree of this Clone, consistent with D41's
// "per-clone" framing; `generated/` and `work/` are created under root, the
// worktree's own checkout, since that is where a working tree actually reads
// and writes files.
func EnsureLayout(root, gitCommonDir string) error {
	if err := os.MkdirAll(GeneratedDir(root), 0o755); err != nil {
		return fmt.Errorf("render: preparing %s: %w", GeneratedDir(root), err)
	}
	if err := os.MkdirAll(WorkDir(root), 0o755); err != nil {
		return fmt.Errorf("render: preparing %s: %w", WorkDir(root), err)
	}
	return ensureExcluded(gitCommonDir)
}

func ensureExcluded(gitCommonDir string) error {
	infoDir := filepath.Join(gitCommonDir, "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return fmt.Errorf("render: preparing %s: %w", infoDir, err)
	}
	path := filepath.Join(infoDir, "exclude")

	present, endsWithNewline, err := excludeLinePresent(path)
	if err != nil {
		return err
	}
	if present {
		return nil
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("render: opening %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	line := excludeLine + "\n"
	if !endsWithNewline {
		line = "\n" + line
	}
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("render: appending %s to %s: %w", excludeLine, path, err)
	}
	return nil
}

// excludeLinePresent reports whether excludeLine already appears as its own
// line in path (line-exact, whitespace-trimmed — never a substring match, so
// e.g. `foo/.wip/` would not falsely satisfy it), and whether the file's
// existing content ends in a newline (so the appended line never gets fused
// onto a trailing one).
func excludeLinePresent(path string) (present, endsWithNewline bool, err error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return false, true, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("render: reading %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == excludeLine {
			present = true
		}
	}
	if err := scanner.Err(); err != nil {
		return false, false, fmt.Errorf("render: reading %s: %w", path, err)
	}

	info, err := os.Stat(path)
	if err != nil {
		return false, false, fmt.Errorf("render: stat %s: %w", path, err)
	}
	endsWithNewline = info.Size() == 0 || lastByteIsNewline(path)
	return present, endsWithNewline, nil
}

// lastByteIsNewline reads path's final byte directly — bufio.Scanner strips
// line terminators, so it cannot itself answer whether the file ended in one.
func lastByteIsNewline(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return true
	}
	buf := make([]byte, 1)
	if _, err := f.ReadAt(buf, info.Size()-1); err != nil {
		return true
	}
	return buf[0] == '\n'
}
