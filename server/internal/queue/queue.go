// Package queue offers the lightweight in-process job runner used to drive
// document parsing/embedding pipelines, async memory updates and title
// generation. The interface is shaped so a future asynq/Kafka-based
// implementation drops in with no business-layer changes.
package queue

import (
	"context"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"aivory/server/internal/envcfg"
)

// Job is a function that the queue runs in the background.
type Job func(ctx context.Context) error

// Queue is the public surface.
type Queue interface {
	// Enqueue schedules the job to be run. Jobs are FIFO; concurrency is
	// bounded by the implementation. The optional `name` is logged.
	Enqueue(name string, job Job)
	// Close drains any pending jobs (best-effort) and stops the workers.
	Close()
}

// InProcess is a simple worker pool. Jobs are dispatched to a small fixed
// number of workers so a tight document-parsing burst doesn't pile up.
type InProcess struct {
	jobs   chan job
	wg     sync.WaitGroup
	closed chan struct{}
	once   sync.Once
	logger *log.Logger
}

type job struct {
	name string
	fn   Job
}

// Env-overridable defaults (see docs/config-reference.md); each falls back to
// the original hardcoded value when its AIVORY_* variable is unset.
var (
	inProcessWorkers            = envcfg.Int("AIVORY_QUEUE_IN_PROCESS_WORKERS", 8)
	processJobBuffer            = envcfg.Int("AIVORY_QUEUE_PROCESS_JOB_BUFFER", 256)
	queueBackpressureJobTimeout = envcfg.Dur("AIVORY_QUEUE_QUEUE_BACKPRESSURE_JOB_TIMEOUT", 30*time.Minute)
	queueWorkerJobTimeout       = envcfg.Dur("AIVORY_QUEUE_QUEUE_WORKER_JOB_TIMEOUT", 30*time.Minute)
)

// NewInProcess starts the worker pool. Production RAG work uses dedicated
// Redis lanes; the larger development pool prevents one parsing burst from
// starving lightweight title and memory jobs.
func NewInProcess(logger *log.Logger) *InProcess {
	q := &InProcess{
		jobs:   make(chan job, processJobBuffer),
		closed: make(chan struct{}),
		logger: logger,
	}
	for i := 0; i < inProcessWorkers; i++ {
		q.wg.Add(1)
		go q.worker()
	}
	return q
}

// Enqueue schedules a job. If the buffer is full, runs synchronously to keep
// memory bounded (dev only).
func (q *InProcess) Enqueue(name string, fn Job) {
	select {
	case q.jobs <- job{name: name, fn: fn}:
	case <-q.closed:
		// Server is shutting down — skip silently.
	default:
		// Backpressure fallback.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), queueBackpressureJobTimeout)
			defer cancel()
			q.runJob(ctx, name, fn)
		}()
	}
}

// runJob executes one job with panic recovery. Background jobs (memory extract,
// compaction, title, RAG ingest) parse untrusted upstream/model output and can
// panic (e.g. the PDF reader on a malformed file, see parser.go). A panic here
// is NOT caught by recoverMiddleware — that only wraps the request goroutine — so
// without this guard one bad job permanently kills a worker; after 4 the pool is
// dead and all async work silently stops. Recover, log, and keep the worker alive.
func (q *InProcess) runJob(ctx context.Context, name string, fn Job) {
	defer func() {
		if r := recover(); r != nil {
			q.logger.Printf("queue(%s) panic recovered: %v\n%s", name, r, debug.Stack())
		}
	}()
	if err := fn(ctx); err != nil {
		q.logger.Printf("queue(%s): %v", name, err)
	}
}

func (q *InProcess) worker() {
	defer q.wg.Done()
	for {
		select {
		case <-q.closed:
			return
		case j := <-q.jobs:
			// 30 min ceiling: document ingest can run a MinerU OCR job that polls
			// up to 20 min (parser.go), so the job context must outlast it or a
			// large scan can never finish. Multiple workers ensure one long ingest
			// doesn't starve title/memory jobs.
			ctx, cancel := context.WithTimeout(context.Background(), queueWorkerJobTimeout)
			q.runJob(ctx, j.name, j.fn)
			cancel()
		}
	}
}

// Close stops the workers.
func (q *InProcess) Close() {
	q.once.Do(func() {
		close(q.closed)
		// Drain channel to unblock pending sends.
		go func() {
			for range q.jobs {
			}
		}()
		q.wg.Wait()
		close(q.jobs)
	})
}
