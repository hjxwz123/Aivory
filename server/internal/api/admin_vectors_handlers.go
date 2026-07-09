package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"aurelia/server/internal/rag"

	"github.com/google/uuid"
)

type vectorMaintenanceJob struct {
	ID          string                 `json:"id"`
	Type        string                 `json:"type"`   // check | rebuild
	Status      string                 `json:"status"` // running | completed | failed
	Progress    string                 `json:"progress"`
	Error       string                 `json:"error,omitempty"`
	StartedAt   int64                  `json:"started_at"`
	CompletedAt int64                  `json:"completed_at,omitempty"`
	Report      *rag.VectorAuditReport `json:"report,omitempty"`
	Rebuilt     int                    `json:"rebuilt,omitempty"`
	Failed      int                    `json:"failed,omitempty"`
}

type vectorMaintenanceState struct {
	Running *vectorMaintenanceJob  `json:"running"`
	Jobs    []vectorMaintenanceJob `json:"jobs"`
}

type vectorMaintenanceManager struct {
	mu        sync.Mutex
	jobs      map[string]*vectorMaintenanceJob
	order     []string
	runningID string
}

var adminVectorMaintenance = &vectorMaintenanceManager{jobs: map[string]*vectorMaintenanceJob{}}

func listVectorMaintenanceAdmin(_ Deps, w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, adminVectorMaintenance.state())
}

func startVectorCheckAdmin(d Deps, w http.ResponseWriter, _ *http.Request) {
	startVectorMaintenanceAdmin(d, w, "check")
}

func startVectorRebuildAdmin(d Deps, w http.ResponseWriter, _ *http.Request) {
	startVectorMaintenanceAdmin(d, w, "rebuild")
}

func startVectorMaintenanceAdmin(d Deps, w http.ResponseWriter, typ string) {
	if running := adminBackupExports.running(); running != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "backup export already running",
			"running": running,
		})
		return
	}
	if d.RAG == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("rag service is not configured"))
		return
	}
	if !d.RAG.VectorStoreEnabled() {
		writeError(w, http.StatusServiceUnavailable, errors.New("vector backend is not configured"))
		return
	}
	job, ok := adminVectorMaintenance.start(typ)
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "vector maintenance already running",
			"running": adminVectorMaintenance.running(),
		})
		return
	}
	go runVectorMaintenanceJob(d, job.ID, typ)
	writeJSON(w, http.StatusAccepted, adminVectorMaintenance.state())
}

func (m *vectorMaintenanceManager) start(typ string) (*vectorMaintenanceJob, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runningID != "" {
		return nil, false
	}
	id := uuid.NewString()
	job := &vectorMaintenanceJob{
		ID:        id,
		Type:      typ,
		Status:    "running",
		Progress:  "preparing",
		StartedAt: time.Now().Unix(),
	}
	m.jobs[id] = job
	m.order = append([]string{id}, m.order...)
	m.runningID = id
	for len(m.order) > 20 {
		old := m.order[len(m.order)-1]
		m.order = m.order[:len(m.order)-1]
		delete(m.jobs, old)
	}
	return cloneVectorJob(job), true
}

func (m *vectorMaintenanceManager) update(id string, fn func(*vectorMaintenanceJob)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job := m.jobs[id]; job != nil {
		fn(job)
	}
}

func (m *vectorMaintenanceManager) finish(id, status, errText string, report *rag.VectorAuditReport, rebuilt, failed int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job := m.jobs[id]; job != nil {
		job.Status = status
		job.Progress = status
		job.Error = errText
		job.Report = report
		job.Rebuilt = rebuilt
		job.Failed = failed
		job.CompletedAt = time.Now().Unix()
	}
	if m.runningID == id {
		m.runningID = ""
	}
}

func (m *vectorMaintenanceManager) running() *vectorMaintenanceJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runningID == "" {
		return nil
	}
	return cloneVectorJob(m.jobs[m.runningID])
}

func (m *vectorMaintenanceManager) state() vectorMaintenanceState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := vectorMaintenanceState{Jobs: make([]vectorMaintenanceJob, 0, len(m.order))}
	if m.runningID != "" {
		out.Running = cloneVectorJob(m.jobs[m.runningID])
	}
	for _, id := range m.order {
		if job := m.jobs[id]; job != nil {
			out.Jobs = append(out.Jobs, *cloneVectorJob(job))
		}
	}
	return out
}

func cloneVectorJob(job *vectorMaintenanceJob) *vectorMaintenanceJob {
	if job == nil {
		return nil
	}
	cp := *job
	if job.Report != nil {
		report := *job.Report
		report.Models = append([]rag.VectorAuditModel{}, job.Report.Models...)
		report.Issues = append([]rag.VectorIssue{}, job.Report.Issues...)
		cp.Report = &report
	}
	return &cp
}

func runVectorMaintenanceJob(d Deps, id, typ string) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
	defer cancel()

	switch typ {
	case "check":
		adminVectorMaintenance.update(id, func(j *vectorMaintenanceJob) { j.Progress = "checking" })
		report, err := d.RAG.AuditVectorIndex(ctx)
		if err != nil {
			adminVectorMaintenance.finish(id, "failed", err.Error(), nil, 0, 0)
			return
		}
		adminVectorMaintenance.finish(id, "completed", "", &report, 0, 0)
	case "rebuild":
		adminVectorMaintenance.update(id, func(j *vectorMaintenanceJob) { j.Progress = "checking" })
		res, err := d.RAG.RebuildMissingVectors(ctx, func(p rag.VectorRebuildProgress) {
			adminVectorMaintenance.update(id, func(j *vectorMaintenanceJob) {
				j.Progress = "rebuilding"
				j.Rebuilt = p.Rebuilt
				j.Failed = p.Failed
			})
		})
		if err != nil {
			adminVectorMaintenance.finish(id, "failed", err.Error(), nil, 0, 0)
			return
		}
		report := res.Before
		if res.After != nil {
			report = *res.After
		}
		if len(res.Issues) > 0 {
			report.Issues = append(report.Issues, res.Issues...)
			if len(report.Issues) > 100 {
				report.Issues = report.Issues[:100]
			}
		}
		adminVectorMaintenance.finish(id, "completed", "", &report, res.Rebuilt, res.Failed)
	default:
		adminVectorMaintenance.finish(id, "failed", "invalid vector maintenance job type", nil, 0, 0)
	}
}
