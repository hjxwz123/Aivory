// Package queue offers the lightweight in-process job runner used to drive
// document parsing/embedding pipelines, async memory updates and title
// generation. The interface is shaped so a future asynq/Kafka-based
// implementation drops in with no business-layer changes.
package queue

import (
	"context"
	"log"
	"sync"
	"time"
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

// NewInProcess starts the worker pool (4 workers).
func NewInProcess(logger *log.Logger) *InProcess {
	q := &InProcess{
		jobs:   make(chan job, 256),
		closed: make(chan struct{}),
		logger: logger,
	}
	for i := 0; i < 4; i++ {
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
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			if err := fn(ctx); err != nil {
				q.logger.Printf("queue(%s): %v", name, err)
			}
		}()
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
			// large scan can never finish. There are 4 workers, so one long ingest
			// doesn't starve title/memory jobs.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			if err := j.fn(ctx); err != nil {
				q.logger.Printf("queue(%s): %v", j.name, err)
			}
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
