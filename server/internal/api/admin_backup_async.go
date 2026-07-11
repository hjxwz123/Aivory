package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"aivory/server/internal/envcfg"

	"github.com/google/uuid"
)

const backupArchivePrefix = "aivory-docker-backup-"

// Env-overridable backup-export knobs. Defaults match the historical hardcoded
// values (see docs/config-reference.md).
var (
	backupExportJobHistoryRetention = envcfg.Int("AIVORY_API_BACKUP_EXPORT_JOB_HISTORY_RETENTION", 20)
	backupExportJobRuntime          = envcfg.Dur("AIVORY_API_BACKUP_EXPORT_JOB_RUNTIME", 12*time.Hour)
)

type backupExportJob struct {
	ID            string `json:"id"`
	Status        string `json:"status"` // running | completed | failed
	Progress      string `json:"progress"`
	Filename      string `json:"filename,omitempty"`
	Error         string `json:"error,omitempty"`
	StartedAt     int64  `json:"started_at"`
	CompletedAt   int64  `json:"completed_at,omitempty"`
	SizeBytes     int64  `json:"size_bytes,omitempty"`
	IncludeFiles  bool   `json:"include_files"`
	IncludeQdrant bool   `json:"include_qdrant"`
	QdrantPoints  int64  `json:"qdrant_points,omitempty"`
}

type backupArchiveFile struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	CreatedAt int64  `json:"created_at"`
}

type backupExportState struct {
	Running  *backupExportJob    `json:"running"`
	Jobs     []backupExportJob   `json:"jobs"`
	Archives []backupArchiveFile `json:"archives"`
}

type backupExportManager struct {
	mu        sync.Mutex
	jobs      map[string]*backupExportJob
	order     []string
	runningID string
}

var adminBackupExports = &backupExportManager{jobs: map[string]*backupExportJob{}}

func startBackupExportAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	if running := adminVectorMaintenance.running(); running != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "vector maintenance already running",
			"running": running,
		})
		return
	}
	var req struct {
		IncludeFiles *bool `json:"include_files"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	includeFiles := true
	if req.IncludeFiles != nil {
		includeFiles = *req.IncludeFiles
	}
	includeQdrant := strings.TrimSpace(d.Config.QdrantURL) != ""
	job, ok := adminBackupExports.start(includeFiles, includeQdrant)
	if !ok {
		state := buildBackupExportState(d)
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "backup export already running",
			"running": state.Running,
		})
		return
	}
	go runBackupExportJob(d, job)
	writeJSON(w, http.StatusAccepted, buildBackupExportState(d))
}

func listBackupExportsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, buildBackupExportState(d))
}

func downloadBackupArchiveAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	name := pathParam(r, "name")
	if !safeBackupArchiveName(name) {
		writeError(w, http.StatusBadRequest, errors.New("invalid archive name"))
		return
	}
	path := filepath.Join(d.Config.BackupDir, name)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, errors.New("archive not found"))
		return
	}
	w.Header().Set("content-type", "application/zip")
	w.Header().Set("content-disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("x-content-type-options", "nosniff")
	http.ServeFile(w, r, path)
}

func buildBackupExportState(d Deps) backupExportState {
	return backupExportState{
		Running:  adminBackupExports.running(),
		Jobs:     adminBackupExports.recentJobs(),
		Archives: listBackupArchiveFiles(d.Config.BackupDir),
	}
}

func (m *backupExportManager) start(includeFiles, includeQdrant bool) (*backupExportJob, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runningID != "" {
		return nil, false
	}
	id := uuid.NewString()
	job := &backupExportJob{
		ID:            id,
		Status:        "running",
		Progress:      "preparing",
		StartedAt:     time.Now().Unix(),
		IncludeFiles:  includeFiles,
		IncludeQdrant: includeQdrant,
	}
	m.jobs[id] = job
	m.order = append([]string{id}, m.order...)
	m.runningID = id
	for len(m.order) > backupExportJobHistoryRetention {
		old := m.order[len(m.order)-1]
		m.order = m.order[:len(m.order)-1]
		delete(m.jobs, old)
	}
	return cloneBackupJob(job), true
}

func (m *backupExportManager) update(id string, fn func(*backupExportJob)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job := m.jobs[id]; job != nil {
		fn(job)
	}
}

func (m *backupExportManager) finish(id string, status, filename, errText string, size, qdrantPoints int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job := m.jobs[id]; job != nil {
		job.Status = status
		job.Progress = status
		job.Filename = filename
		job.Error = errText
		job.CompletedAt = time.Now().Unix()
		job.SizeBytes = size
		job.QdrantPoints = qdrantPoints
	}
	if m.runningID == id {
		m.runningID = ""
	}
}

func (m *backupExportManager) running() *backupExportJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runningID == "" {
		return nil
	}
	return cloneBackupJob(m.jobs[m.runningID])
}

func (m *backupExportManager) recentJobs() []backupExportJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]backupExportJob, 0, len(m.order))
	for _, id := range m.order {
		if job := m.jobs[id]; job != nil {
			out = append(out, *cloneBackupJob(job))
		}
	}
	return out
}

func cloneBackupJob(job *backupExportJob) *backupExportJob {
	if job == nil {
		return nil
	}
	cp := *job
	return &cp
}

func runBackupExportJob(d Deps, job *backupExportJob) {
	ctx, cancel := context.WithTimeout(context.Background(), backupExportJobRuntime)
	defer cancel()

	if err := os.MkdirAll(d.Config.BackupDir, 0o755); err != nil {
		adminBackupExports.finish(job.ID, "failed", "", err.Error(), 0, 0)
		return
	}
	stamp := time.Now().Format("20060102-150405")
	shortID := strings.ReplaceAll(job.ID, "-", "")
	if len(shortID) > 10 {
		shortID = shortID[:10]
	}
	filename := fmt.Sprintf("%s%s-%s.zip", backupArchivePrefix, stamp, shortID)
	finalPath := filepath.Join(d.Config.BackupDir, filename)
	tmpPath := finalPath + ".tmp"

	adminBackupExports.update(job.ID, func(j *backupExportJob) { j.Progress = "reading_database" })
	tx, err := d.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		adminBackupExports.finish(job.ID, "failed", "", err.Error(), 0, 0)
		return
	}
	defer func() { _ = tx.Rollback() }()

	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		adminBackupExports.finish(job.ID, "failed", "", err.Error(), 0, 0)
		return
	}
	adminBackupExports.update(job.ID, func(j *backupExportJob) { j.Progress = "writing_archive" })
	res, err := writeBackupArchive(ctx, d, tx, out, backupArchiveOptions{
		IncludeFiles:  job.IncludeFiles,
		IncludeQdrant: job.IncludeQdrant,
	})
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmpPath)
		adminBackupExports.finish(job.ID, "failed", "", err.Error(), 0, res.QdrantPoints)
		return
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		adminBackupExports.finish(job.ID, "failed", "", err.Error(), 0, res.QdrantPoints)
		return
	}
	info, err := os.Stat(finalPath)
	if err != nil {
		adminBackupExports.finish(job.ID, "failed", filename, err.Error(), 0, res.QdrantPoints)
		return
	}
	adminBackupExports.finish(job.ID, "completed", filename, "", info.Size(), res.QdrantPoints)
}

func listBackupArchiveFiles(dir string) []backupArchiveFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []backupArchiveFile{}
	}
	out := []backupArchiveFile{}
	for _, entry := range entries {
		if entry.IsDir() || !safeBackupArchiveName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, backupArchiveFile{
			Name:      entry.Name(),
			SizeBytes: info.Size(),
			CreatedAt: info.ModTime().Unix(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

func safeBackupArchiveName(name string) bool {
	if name == "" || filepath.Base(name) != name {
		return false
	}
	if !strings.HasPrefix(name, backupArchivePrefix) || !strings.HasSuffix(name, ".zip") {
		return false
	}
	return !strings.Contains(name, "..")
}
