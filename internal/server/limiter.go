package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/devrail-dev/devrail-router/internal/config"
)

var (
	errQueueFull    = errors.New("model queue is full")
	errQueueTimeout = errors.New("timed out waiting for model queue")
)

type modelLimiter struct {
	modelID      string
	slots        chan struct{}
	maxQueueSize int
	queueTimeout time.Duration

	mu     sync.Mutex
	active int
	queued int
}

type limiterSnapshot struct {
	active int
	queued int
}

func newModelLimiter(model config.ModelConfig) (*modelLimiter, error) {
	queueTimeout, err := model.QueueTimeoutDuration()
	if err != nil {
		return nil, err
	}
	if model.MaxConcurrentRequests <= 0 {
		return nil, nil
	}

	return &modelLimiter{
		modelID:      model.ID,
		slots:        make(chan struct{}, model.MaxConcurrentRequests),
		maxQueueSize: model.MaxQueueSize,
		queueTimeout: queueTimeout,
	}, nil
}

func (limiter *modelLimiter) acquire(ctx context.Context) (time.Duration, limiterSnapshot, func(), error) {
	started := time.Now()

	select {
	case limiter.slots <- struct{}{}:
		snapshot := limiter.incrementActive()
		return 0, snapshot, limiter.release, nil
	default:
	}

	if !limiter.joinQueue() {
		return 0, limiter.snapshot(), nil, errQueueFull
	}
	defer limiter.leaveQueue()

	waitCtx := ctx
	cancel := func() {}
	if limiter.queueTimeout > 0 {
		waitCtx, cancel = context.WithTimeout(ctx, limiter.queueTimeout)
	}
	defer cancel()

	select {
	case limiter.slots <- struct{}{}:
		waited := time.Since(started)
		snapshot := limiter.incrementActive()
		return waited, snapshot, limiter.release, nil
	case <-waitCtx.Done():
		if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
			return time.Since(started), limiter.snapshot(), nil, errQueueTimeout
		}
		return time.Since(started), limiter.snapshot(), nil, waitCtx.Err()
	}
}

func (limiter *modelLimiter) joinQueue() bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	if limiter.queued >= limiter.maxQueueSize {
		return false
	}
	limiter.queued++
	return true
}

func (limiter *modelLimiter) leaveQueue() {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	limiter.queued--
}

func (limiter *modelLimiter) incrementActive() limiterSnapshot {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	limiter.active++
	return limiterSnapshot{active: limiter.active, queued: limiter.queued}
}

func (limiter *modelLimiter) release() {
	<-limiter.slots

	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	limiter.active--
}

func (limiter *modelLimiter) snapshot() limiterSnapshot {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	return limiterSnapshot{active: limiter.active, queued: limiter.queued}
}
