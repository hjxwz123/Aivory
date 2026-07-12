package api

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/store"
)

// ===== Async user deletion (§ async user delete) =====
//
// Deleting an account with years of history (hundreds of thousands of
// messages, thousands of files, vectors per chunk) used to run entirely
// inside the DELETE request: one huge SQL transaction plus one Qdrant call
// per document/conversation/KB plus one disk unlink per file. This module
// moves the heavy lifting to a background job:
//
//   1. The request only flips the user to status='deleting' (which bumps the
//      token version and revokes refresh tokens — the account is locked out
//      instantly, exactly like a ban) and spawns the job. It returns 202.
//   2. The job drains the big tables in short per-batch transactions (SQLite's
//      single writer never stalls behind one giant delete), cleans vectors and
//      physical storage best-effort, then runs the final store.DeleteUser
//      sweep, which is small by the time it executes.
//   3. Jobs that die with the process are resumed on startup: any user still
//      in status='deleting' is re-enqueued (every step is idempotent).
//
// status='deleting' is terminal: the auth stack rejects every non-"active"
// account, so no new sessions or requests can appear mid-deletion.

var (
	userDeleteJobRuntime    = envcfg.Dur("AIVORY_API_USER_DELETE_JOB_RUNTIME", 2*time.Hour)
	// Physical cleanup gets its own budget so an exhausted job context can
	// never silently skip the disk/object-storage phase.
	userDeleteFinalCleanupTimeout = envcfg.Dur("AIVORY_API_USER_DELETE_FINAL_CLEANUP_TIMEOUT", 1*time.Hour)
	userDeleteConvBatch     = envcfg.Int("AIVORY_API_USER_DELETE_CONV_BATCH", 200)
	userDeleteUsageBatch    = envcfg.Int("AIVORY_API_USER_DELETE_USAGE_BATCH", 10000)
	userDeleteJobRetention  = envcfg.Int("AIVORY_API_USER_DELETE_JOB_HISTORY_RETENTION", 20)
	userDeleteProgressEvery = envcfg.Int("AIVORY_API_USER_DELETE_PROGRESS_EVERY", 50)
)

type userDeletionJob struct {
	UserID      string `json:"user_id"`
	Email       string `json:"email"`
	Status      string `json:"status"` // running | completed | failed
	Progress    string `json:"progress"`
	Error       string `json:"error,omitempty"`
	StartedAt   int64  `json:"started_at"`
	CompletedAt int64  `json:"completed_at,omitempty"`
}

type userDeletionManager struct {
	mu    sync.Mutex
	jobs  map[string]*userDeletionJob // keyed by user id
	order []string
}

var userDeletions = &userDeletionManager{jobs: map[string]*userDeletionJob{}}

// start registers a job for the user. Single-flight per user id: a second
// delete request while a job is running returns started=false.
func (m *userDeletionManager) start(userID, email string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job := m.jobs[userID]; job != nil && job.Status == "running" {
		return false
	}
	if _, exists := m.jobs[userID]; !exists {
		m.order = append([]string{userID}, m.order...)
	}
	m.jobs[userID] = &userDeletionJob{
		UserID: userID, Email: email,
		Status: "running", Progress: "preparing",
		StartedAt: time.Now().Unix(),
	}
	for len(m.order) > userDeleteJobRetention {
		old := m.order[len(m.order)-1]
		if j := m.jobs[old]; j != nil && j.Status != "running" {
			m.order = m.order[:len(m.order)-1]
			delete(m.jobs, old)
		} else {
			break
		}
	}
	return true
}

func (m *userDeletionManager) progress(userID, text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job := m.jobs[userID]; job != nil {
		job.Progress = text
	}
}

func (m *userDeletionManager) finish(userID, status, errText string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job := m.jobs[userID]; job != nil {
		job.Status = status
		job.Progress = status
		job.Error = errText
		job.CompletedAt = time.Now().Unix()
	}
}

func (m *userDeletionManager) list() []userDeletionJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]userDeletionJob, 0, len(m.order))
	for _, id := range m.order {
		if job := m.jobs[id]; job != nil {
			out = append(out, *job)
		}
	}
	return out
}

// startUserDeletion locks the account out and spawns the background job.
// Callers must have run their own permission guards (self-delete, last-admin,
// password confirmation) first. Returns false when a job is already running.
func startUserDeletion(d Deps, userID, email string) (bool, error) {
	if !userDeletions.start(userID, email) {
		return false, nil
	}
	// MarkUserDeleting flips the status atomically with the last-admin guard
	// folded into the UPDATE (closes the two-admins-delete-each-other TOCTOU)
	// and performs the same instant lockout a ban does: token_ver bump plus
	// refresh-token revocation. Already-deleting rows are a no-op so resume
	// and admin-retry paths stay idempotent.
	if _, err := store.MarkUserDeleting(context.Background(), d.DB, userID); err != nil {
		userDeletions.finish(userID, "failed", err.Error())
		return false, err
	}
	invalidateAuthUser(d, userID)
	d.Cache.Publish("user:"+userID+":kill", "1") // drop live streams immediately
	go func() {
		defer func() {
			// Same guard as queue.runJob: a panic here would otherwise kill the
			// process-wide goroutine silently and strand the user in 'deleting'.
			if rec := recover(); rec != nil {
				logStorageCleanup(d, "user delete %s: panic: %v\n%s", userID, rec, debug.Stack())
				userDeletions.finish(userID, "failed", fmt.Sprintf("panic: %v", rec))
			}
		}()
		runUserDeletionJob(d, userID)
	}()
	return true, nil
}

// runUserDeletionJob performs the actual deletion. Synchronous — tests call it
// directly; production wraps it in a goroutine via startUserDeletion.
func runUserDeletionJob(d Deps, userID string) {
	ctx, cancel := context.WithTimeout(context.Background(), userDeleteJobRuntime)
	defer cancel()
	label := "async delete user " + userID

	// Snapshot vector scopes and storage paths before rows start disappearing.
	plan, err := store.BuildUserCleanupPlan(ctx, d.DB, userID)
	if err != nil {
		userDeletions.finish(userID, "failed", err.Error())
		return
	}

	// Persist the storage paths BEFORE any destructive delete: a crash from
	// here on can always finish the physical cleanup from this ledger, even
	// after the users row (and with it the 'deleting' marker) is gone.
	if err := store.InsertPendingStorageCleanup(ctx, d.DB, userID, plan.StoragePaths); err != nil {
		userDeletions.finish(userID, "failed", err.Error())
		return
	}

	// Vector cleanup FIRST, while every row still exists: if this pass dies,
	// the retry rebuilds an identical plan and repeats it — Qdrant deletes are
	// idempotent payload-filter calls. (Doing this after the row drain would
	// let a mid-drain crash orphan the drained conversations' vectors forever:
	// the retry's plan can no longer see them.)
	for i, docID := range plan.DocumentIDs {
		cleanupRAGDocument(ctx, d, docID, label)
		if (i+1)%userDeleteProgressEvery == 0 {
			userDeletions.progress(userID, fmt.Sprintf("vectors: documents %d/%d", i+1, len(plan.DocumentIDs)))
		}
	}
	for i, convID := range plan.ConversationIDs {
		cleanupRAGConversation(ctx, d, convID, label)
		if (i+1)%userDeleteProgressEvery == 0 {
			userDeletions.progress(userID, fmt.Sprintf("vectors: conversations %d/%d", i+1, len(plan.ConversationIDs)))
		}
	}
	for _, kbID := range plan.KBIDs {
		cleanupRAGKB(ctx, d, kbID, label)
	}

	// Drain conversations (and their messages) in short transactions.
	deletedConvs := 0
	for {
		ids, err := store.ConversationIDsByUser(ctx, d.DB, userID, userDeleteConvBatch)
		if err != nil {
			userDeletions.finish(userID, "failed", err.Error())
			return
		}
		if len(ids) == 0 {
			break
		}
		if err := store.DeleteConversationRows(ctx, d.DB, ids); err != nil {
			userDeletions.finish(userID, "failed", err.Error())
			return
		}
		deletedConvs += len(ids)
		userDeletions.progress(userID, fmt.Sprintf("conversations: %d deleted", deletedConvs))
	}

	// Drain usage logs.
	deletedUsage := int64(0)
	for {
		n, err := store.DeleteUsageLogsBatch(ctx, d.DB, userID, userDeleteUsageBatch)
		if err != nil {
			userDeletions.finish(userID, "failed", err.Error())
			return
		}
		if n == 0 {
			break
		}
		deletedUsage += n
		userDeletions.progress(userID, fmt.Sprintf("usage logs: %d deleted", deletedUsage))
	}

	// Final sweep: files, documents, memories, tokens, the users row and its
	// cascades. The big tables are already empty, so this transaction is small.
	userDeletions.progress(userID, "finalizing")
	if err := store.DeleteUser(ctx, d.DB, userID); err != nil {
		userDeletions.finish(userID, "failed", err.Error())
		return
	}
	invalidateAuthUser(d, userID)

	// Physical bytes, driven by the durable ledger — NOT the request-scoped
	// context: if the 2h job budget is nearly spent by now, the cleanup must
	// still run to completion rather than silently skipping paths. Each path
	// is forgotten only once it is actually handled; failures stay in the
	// table for the startup sweep.
	fctx, fcancel := context.WithTimeout(context.Background(), userDeleteFinalCleanupTimeout)
	defer fcancel()
	obj := objectStorageClient(d)
	cleaned := 0
	for _, p := range plan.StoragePaths {
		if p == "" {
			continue
		}
		if _, err := cleanupOneStoragePath(fctx, d, obj, p); err != nil {
			logStorageCleanup(d, "%s: cleanup %q: %v", label, p, err)
			continue // keep the ledger row; the startup sweep retries it
		}
		if err := store.DeletePendingStorageCleanup(fctx, d.DB, p); err != nil {
			logStorageCleanup(d, "%s: forget ledger row %q: %v", label, p, err)
		}
		cleaned++
		if cleaned%userDeleteProgressEvery == 0 {
			userDeletions.progress(userID, fmt.Sprintf("storage: %d/%d", cleaned, len(plan.StoragePaths)))
		}
	}

	userDeletions.finish(userID, "completed", "")
}

// listUserDeletionsAdmin reports running and recent deletion jobs so the
// users page can poll while a purge is in flight.
func listUserDeletionsAdmin(_ Deps, w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"jobs": userDeletions.list()})
}

// resumeUserDeletions re-enqueues jobs for accounts stranded in
// status='deleting' by a previous process. Called once at startup.
func resumeUserDeletions(d Deps) {
	users, err := store.UsersMarkedDeleting(context.Background(), d.DB)
	if err != nil {
		logStorageCleanup(d, "resume user deletions: %v", err)
	}
	for _, u := range users {
		if started, err := startUserDeletion(d, u.ID, u.Email); err != nil {
			logStorageCleanup(d, "resume user deletion %s: %v", u.ID, err)
		} else if started {
			logStorageCleanup(d, "resumed user deletion %s (%s)", u.ID, u.Email)
		}
	}
	sweepPendingStorageCleanup(d)
}

// sweepPendingStorageCleanup finishes physical deletions a crashed process
// left in the ledger — including jobs whose users row is already gone, which
// UsersMarkedDeleting can no longer see. Paths still referenced by live rows
// belong to a deletion job that is running (or about to resume); they are
// left for that job and swept here only once orphaned.
func sweepPendingStorageCleanup(d Deps) {
	ctx, cancel := context.WithTimeout(context.Background(), userDeleteFinalCleanupTimeout)
	defer cancel()
	paths, err := store.ListPendingStorageCleanup(ctx, d.DB)
	if err != nil {
		logStorageCleanup(d, "storage cleanup sweep: %v", err)
		return
	}
	if len(paths) == 0 {
		return
	}
	obj := objectStorageClient(d)
	swept := 0
	for _, p := range paths {
		referenced, err := cleanupOneStoragePath(ctx, d, obj, p)
		if err != nil {
			logStorageCleanup(d, "storage cleanup sweep %q: %v", p, err)
			continue
		}
		if referenced {
			continue // its owner's job will handle (and forget) it
		}
		if err := store.DeletePendingStorageCleanup(ctx, d.DB, p); err != nil {
			logStorageCleanup(d, "storage cleanup sweep: forget %q: %v", p, err)
			continue
		}
		swept++
	}
	if swept > 0 {
		logStorageCleanup(d, "storage cleanup sweep: removed %d orphaned path(s)", swept)
	}
}
