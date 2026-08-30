package api

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"aivory/server/internal/fileguard"
)

type backupFileRestore struct {
	trees     []*stagedBackupTree
	files     int
	applied   bool
	committed bool
}

type stagedBackupTree struct {
	target    string
	stage     string
	previous  string
	hadTarget bool
	applied   bool
}

func prepareBackupFileRestore(d Deps, zr *zip.Reader, man backupManifest) (*backupFileRestore, error) {
	if !man.IncludesFiles {
		return nil, nil
	}
	uploads, err := newStagedBackupTree(d.Config.UploadDir)
	if err != nil {
		return nil, fmt.Errorf("prepare upload restore: %w", err)
	}
	artifacts, err := newStagedBackupTree(d.Config.ArtifactDir)
	if err != nil {
		_ = uploads.cleanup()
		return nil, fmt.Errorf("prepare artifact restore: %w", err)
	}
	if uploads.target == artifacts.target {
		_ = uploads.cleanup()
		_ = artifacts.cleanup()
		return nil, errors.New("upload and artifact directories must be distinct")
	}

	restore := &backupFileRestore{trees: []*stagedBackupTree{uploads, artifacts}}
	ok := false
	defer func() {
		if !ok {
			_ = restore.Rollback()
		}
	}()
	for _, entry := range zr.File {
		var tree *stagedBackupTree
		var rel string
		switch {
		case strings.HasPrefix(entry.Name, backupZipUploads):
			tree = uploads
			rel = strings.TrimPrefix(entry.Name, backupZipUploads)
		case strings.HasPrefix(entry.Name, backupZipArtifacts):
			tree = artifacts
			rel = strings.TrimPrefix(entry.Name, backupZipArtifacts)
		default:
			continue
		}
		if rel == "" || strings.HasSuffix(entry.Name, "/") {
			continue
		}
		if err := extractBackupEntry(tree.stage, filepath.FromSlash(rel), entry); err != nil {
			return nil, fmt.Errorf("restore %s: %w", entry.Name, err)
		}
		restore.files++
	}
	ok = true
	return restore, nil
}

func newStagedBackupTree(target string) (*stagedBackupTree, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("storage directory is empty")
	}
	abs, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return nil, errors.New("refusing to replace filesystem root")
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(parent, ".aivory-restore-stage-")
	if err != nil {
		return nil, err
	}
	return &stagedBackupTree{target: abs, stage: stage}, nil
}

func extractBackupEntry(root, rel string, entry *zip.File) error {
	if strings.TrimSpace(rel) == "" || filepath.IsAbs(rel) {
		return errors.New("invalid relative path")
	}
	dest, err := fileguard.PrepareWrite(root, filepath.Join(root, rel))
	if err != nil {
		return err
	}
	rc, err := entry.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".aivory-restore-file-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, rc); err != nil {
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return err
	}
	keep = true
	return nil
}

func (r *backupFileRestore) Apply() error {
	if r == nil || r.applied {
		return nil
	}
	for _, tree := range r.trees {
		if err := tree.apply(); err != nil {
			_ = r.Rollback()
			return err
		}
	}
	r.applied = true
	return nil
}

func (r *backupFileRestore) Rollback() error {
	if r == nil || r.committed {
		return nil
	}
	var errs []error
	for i := len(r.trees) - 1; i >= 0; i-- {
		if err := r.trees[i].rollback(); err != nil {
			errs = append(errs, err)
		}
	}
	r.applied = false
	return errors.Join(errs...)
}

func (r *backupFileRestore) Commit() error {
	if r == nil || r.committed {
		return nil
	}
	r.committed = true
	var errs []error
	for _, tree := range r.trees {
		if err := tree.cleanup(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (t *stagedBackupTree) apply() error {
	info, err := os.Lstat(t.target)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("restore target is not a regular directory: %s", t.target)
		}
		t.hadTarget = true
		placeholder, err := os.MkdirTemp(filepath.Dir(t.target), ".aivory-restore-previous-")
		if err != nil {
			return err
		}
		if err := os.Remove(placeholder); err != nil {
			return err
		}
		t.previous = placeholder
		if err := os.Rename(t.target, t.previous); err != nil {
			return err
		}
	case os.IsNotExist(err):
		t.hadTarget = false
	default:
		return err
	}

	if err := os.Rename(t.stage, t.target); err != nil {
		if t.hadTarget {
			_ = os.Rename(t.previous, t.target)
		}
		return err
	}
	t.applied = true
	return nil
}

func (t *stagedBackupTree) rollback() error {
	var errs []error
	if t.applied {
		if err := os.RemoveAll(t.target); err != nil {
			errs = append(errs, err)
		}
		if t.hadTarget {
			if err := os.Rename(t.previous, t.target); err != nil {
				errs = append(errs, err)
			}
		}
		t.applied = false
	}
	if t.stage != "" {
		if err := os.RemoveAll(t.stage); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
		t.stage = ""
	}
	return errors.Join(errs...)
}

func (t *stagedBackupTree) cleanup() error {
	var errs []error
	if t.previous != "" {
		if err := os.RemoveAll(t.previous); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
		t.previous = ""
	}
	if t.stage != "" {
		if err := os.RemoveAll(t.stage); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
		t.stage = ""
	}
	return errors.Join(errs...)
}
