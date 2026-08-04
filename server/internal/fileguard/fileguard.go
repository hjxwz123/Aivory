// Package fileguard centralizes containment checks for database-backed local
// storage paths. Database values are metadata, not trusted filesystem paths.
package fileguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrOutsideRoot = errors.New("path is outside the configured storage root")

// Relative returns path relative to root when path is strictly contained by
// root. It is lexical by design, so it can validate backup paths from another
// host even when those source paths do not exist on this machine.
func Relative(root, path string) (string, error) {
	root = strings.TrimSpace(root)
	path = strings.TrimSpace(path)
	if root == "" || path == "" {
		return "", ErrOutsideRoot
	}
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve storage root: %w", err)
	}
	pathAbs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve storage path: %w", err)
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == "" || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrOutsideRoot
	}
	return rel, nil
}

// Remap validates path against sourceRoot and returns the corresponding path
// beneath targetRoot. No source path is ever retained on validation failure.
func Remap(sourceRoot, targetRoot, path string) (string, error) {
	rel, err := Relative(sourceRoot, path)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(targetRoot) == "" {
		return "", ErrOutsideRoot
	}
	targetAbs, err := filepath.Abs(filepath.Clean(targetRoot))
	if err != nil {
		return "", fmt.Errorf("resolve target storage root: %w", err)
	}
	return filepath.Join(targetAbs, rel), nil
}

// ResolveExisting returns an existing path only when both its lexical path and
// its symlink-resolved path remain inside one of roots. Callers should open or
// delete the returned path, not the original database value.
func ResolveExisting(path string, roots ...string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", ErrOutsideRoot
	}
	pathAbs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve storage path: %w", err)
	}
	realPath, err := filepath.EvalSymlinks(pathAbs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return "", err
	}
	// Callers use the returned path as a regular file (read, unlink, or
	// replace).  Do not let database-controlled paths reach FIFOs, sockets,
	// devices, or other special files even when they happen to be below a
	// configured root.
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("storage path is not a regular file: %w", ErrOutsideRoot)
	}

	for _, root := range roots {
		if _, err := Relative(root, pathAbs); err != nil {
			continue
		}
		rootAbs, err := filepath.Abs(filepath.Clean(strings.TrimSpace(root)))
		if err != nil {
			continue
		}
		realRoot, err := filepath.EvalSymlinks(rootAbs)
		if err != nil {
			continue
		}
		if _, err := Relative(realRoot, realPath); err == nil {
			return realPath, nil
		}
	}
	return "", ErrOutsideRoot
}

// PrepareWrite creates missing directories beneath root without traversing a
// symlink below the root and returns a destination that is safe for a local
// write. Existing symlink destinations are rejected instead of followed.
func PrepareWrite(root, path string) (string, error) {
	rel, err := Relative(root, path)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(filepath.Clean(strings.TrimSpace(root)))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(rootAbs, 0o755); err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}

	parts := strings.Split(filepath.Dir(rel), string(filepath.Separator))
	current := rootAbs
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o755); err != nil && !os.IsExist(err) {
				return "", err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", ErrOutsideRoot
		}
	}
	realParent, err := filepath.EvalSymlinks(filepath.Dir(filepath.Join(rootAbs, rel)))
	if err != nil {
		return "", err
	}
	if filepath.Clean(realParent) != filepath.Clean(realRoot) {
		if _, err := Relative(realRoot, filepath.Join(realParent, filepath.Base(rel))); err != nil {
			return "", ErrOutsideRoot
		}
	}
	dest := filepath.Join(rootAbs, rel)
	if info, err := os.Lstat(dest); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", ErrOutsideRoot
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return dest, nil
}
