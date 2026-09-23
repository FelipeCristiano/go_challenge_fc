package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/felipecristiano/desafio/internal/application/usecase"
	"github.com/felipecristiano/desafio/internal/infra/config"
	"go.uber.org/fx"
)

type PendingReferenceWorker struct {
	useCase      *usecase.RetryPendingReferencesUseCase
	pollInterval time.Duration
	stopChan     chan struct{}
}

func NewPendingReferenceWorker(
	useCase *usecase.RetryPendingReferencesUseCase,
	cfg *config.Config,
) *PendingReferenceWorker {
	interval := cfg.PendingRef.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &PendingReferenceWorker{
		useCase:      useCase,
		pollInterval: interval,
		stopChan:     make(chan struct{}),
	}
}

func (w *PendingReferenceWorker) Start(ctx context.Context) {
	slog.Info("pending references worker started", "interval", w.pollInterval)
	go func() {
		ticker := time.NewTicker(w.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-w.stopChan:
				return
			case <-ticker.C:
				resolved, err := w.useCase.ExecuteBatch(context.Background(), 20)
				if err != nil {
					slog.Error("error processing pending references batch", "error", err)
				} else if resolved > 0 {
					slog.Info("resolved pending references", "count", resolved)
				}
			}
		}
	}()
}

func (w *PendingReferenceWorker) Stop(ctx context.Context) error {
	close(w.stopChan)
	slog.Info("pending references worker stopped")
	return nil
}

// Module Pending References Worker para Fx
var Module = fx.Module("pending_ref_worker",
	fx.Provide(NewPendingReferenceWorker),
)
