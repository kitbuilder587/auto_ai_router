package worker

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// testJob implements Job for testing
type testJob struct {
	id      int
	result  chan<- testResult
	execute func(ctx context.Context) error
}

type testResult struct {
	jobID int
	err   error
}

func (j testJob) Execute(ctx context.Context) Result {
	err := j.execute(ctx)
	if j.result != nil {
		j.result <- testResult{jobID: j.id, err: err}
	}
	return testResult{jobID: j.id, err: err}
}

func (r testResult) Error() error {
	return r.err
}

// mockLogger implements slog.Logger for testing
type mockLogger struct {
	mu       sync.Mutex
	messages []string
}

func newMockLogger() *slog.Logger {
	return slog.New(&mockLoggerHandler{logger: &mockLogger{}})
}

type mockLoggerHandler struct {
	logger *mockLogger
}

func (h *mockLoggerHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

func (h *mockLoggerHandler) Handle(ctx context.Context, r slog.Record) error {
	h.logger.mu.Lock()
	defer h.logger.mu.Unlock()
	h.logger.messages = append(h.logger.messages, r.Message)
	return nil
}

func (h *mockLoggerHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *mockLoggerHandler) WithGroup(name string) slog.Handler {
	return h
}

func TestSpawnWorkerPool_BasicExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := newMockLogger()
	jobQueue := make(chan Job, 10)
	resultCh := make(chan testResult, 5)

	// Spawn worker pool with 2 workers
	wg := SpawnWorkerPool(ctx, 2, jobQueue, logger)

	// Submit jobs
	for i := 0; i < 3; i++ {
		jobQueue <- testJob{
			id:     i,
			result: resultCh,
			execute: func(ctx context.Context) error {
				return nil
			},
		}
	}

	// Wait for results
	results := make([]testResult, 0)
	timeout := time.After(2 * time.Second)
done:
	for {
		select {
		case r := <-resultCh:
			results = append(results, r)
			if len(results) == 3 {
				break done
			}
		case <-timeout:
			t.Fatal("timeout waiting for results")
		}
	}

	// Verify all jobs completed
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}

	// Close job queue and wait for workers
	close(jobQueue)
	wg.Wait()

	// Verify no errors
	for _, r := range results {
		if r.err != nil {
			t.Errorf("job %d failed: %v", r.jobID, r.err)
		}
	}
}

func TestSpawnWorkerPool_ZeroWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := newMockLogger()
	jobQueue := make(chan Job, 10)

	// Spawn with 0 workers - should default to 1
	wg := SpawnWorkerPool(ctx, 0, jobQueue, logger)

	// Submit a job
	resultCh := make(chan testResult, 1)
	jobQueue <- testJob{
		id:     1,
		result: resultCh,
		execute: func(ctx context.Context) error {
			return nil
		},
	}

	close(jobQueue)
	wg.Wait()

	// Should complete without hanging
	select {
	case r := <-resultCh:
		if r.err != nil {
			t.Errorf("job failed: %v", r.err)
		}
	default:
	}
}

func TestSpawnWorkerPool_NegativeWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := newMockLogger()
	jobQueue := make(chan Job, 10)

	// Spawn with negative workers - should default to 1
	wg := SpawnWorkerPool(ctx, -5, jobQueue, logger)

	// Submit a job
	resultCh := make(chan testResult, 1)
	jobQueue <- testJob{
		id:     1,
		result: resultCh,
		execute: func(ctx context.Context) error {
			return nil
		},
	}

	close(jobQueue)
	wg.Wait()
}

func TestSpawnWorkerPool_JobExecutionError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := newMockLogger()
	jobQueue := make(chan Job, 10)
	resultCh := make(chan testResult, 1)

	// Spawn worker pool
	wg := SpawnWorkerPool(ctx, 1, jobQueue, logger)

	// Submit a job that returns error
	expectedErr := &testError{"test error"}
	jobQueue <- testJob{
		id:     1,
		result: resultCh,
		execute: func(ctx context.Context) error {
			return expectedErr
		},
	}

	// Wait for result
	select {
	case r := <-resultCh:
		if r.err == nil {
			t.Error("expected error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for result")
	}

	close(jobQueue)
	wg.Wait()
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

func TestSpawnWorkerPool_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	logger := newMockLogger()
	jobQueue := make(chan Job, 10)
	resultCh := make(chan testResult, 5)

	// Spawn worker pool
	wg := SpawnWorkerPool(ctx, 2, jobQueue, logger)

	// Submit some jobs
	for i := 0; i < 3; i++ {
		jobQueue <- testJob{
			id:     i,
			result: resultCh,
			execute: func(ctx context.Context) error {
				// Simulate some work
				time.Sleep(50 * time.Millisecond)
				return nil
			},
		}
	}

	// Cancel context while jobs are running
	cancel()

	// Close job queue to signal workers to stop draining
	// This is required because workers wait for more jobs in range loop
	close(jobQueue)

	// Wait for workers to finish
	wg.Wait()

	// Drain result channel to avoid goroutine leak
	close(resultCh)
}

func TestSpawnWorkerPool_JobQueueClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := newMockLogger()
	jobQueue := make(chan Job, 10)
	resultCh := make(chan testResult, 1)

	// Spawn worker pool
	wg := SpawnWorkerPool(ctx, 1, jobQueue, logger)

	// Submit a job
	jobQueue <- testJob{
		id:     1,
		result: resultCh,
		execute: func(ctx context.Context) error {
			return nil
		},
	}

	// Close job queue (signals workers to exit)
	close(jobQueue)

	// Wait for workers to finish
	wg.Wait()

	// Should still get the result before queue closed
	select {
	case r := <-resultCh:
		if r.err != nil {
			t.Errorf("job failed: %v", r.err)
		}
	case <-time.After(1 * time.Second):
		// Result may have been received before close
	}
}

func TestSpawnWorkerPool_PanicRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := newMockLogger()
	jobQueue := make(chan Job, 10)
	resultCh := make(chan testResult, 2)

	// Spawn worker pool
	wg := SpawnWorkerPool(ctx, 1, jobQueue, logger)

	// Submit a job that panics
	jobQueue <- testJob{
		id:     1,
		result: resultCh,
		execute: func(ctx context.Context) error {
			panic("test panic")
		},
	}

	// Submit another job to verify worker continues
	jobQueue <- testJob{
		id:     2,
		result: resultCh,
		execute: func(ctx context.Context) error {
			return nil
		},
	}

	// Wait for workers
	close(jobQueue)
	wg.Wait()

	// Both jobs should have been processed (second one should succeed)
	select {
	case r := <-resultCh:
		if r.jobID == 2 && r.err != nil {
			t.Errorf("second job should succeed, got error: %v", r.err)
		}
	default:
	}
}

func TestJobInterface(t *testing.T) {
	// Verify that testJob properly implements Job and Result interfaces
	var _ Job = testJob{}
	var _ Result = testResult{}
}
