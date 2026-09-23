package outbox

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/felipecristiano/desafio/internal/application/port"
)

// SQSProducerAPI define a interface do SDK SQS necessária para publicar eventos.
type SQSProducerAPI interface {
	SendMessage(ctx context.Context, params *awssqs.SendMessageInput, optFns ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error)
}

type WorkerConfig struct {
	EventsQueueURL string
	PollInterval   time.Duration
	BatchSize      int
	MaxAttempts    int
}

type Worker struct {
	sqsProducer SQSProducerAPI
	uow         port.UnitOfWork
	outboxRepo  port.OutboxRepository
	cfg         WorkerConfig
	stopChan    chan struct{}
	wg          sync.WaitGroup
	running     bool
	mu          sync.Mutex
}

func NewWorker(
	sqsProducer SQSProducerAPI,
	uow port.UnitOfWork,
	outboxRepo port.OutboxRepository,
	cfg WorkerConfig,
) *Worker {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 50
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 10
	}

	return &Worker{
		sqsProducer: sqsProducer,
		uow:         uow,
		outboxRepo:  outboxRepo,
		cfg:         cfg,
		stopChan:    make(chan struct{}),
	}
}

// Start inicia o loop de publicação da outbox em background.
func (w *Worker) Start(ctx context.Context) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	w.mu.Unlock()

	w.wg.Add(1)
	go w.runLoop(ctx)
}

// Stop finaliza o worker de forma graciosa aguardando o processamento do lote atual.
func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return nil
	}
	w.running = false
	close(w.stopChan)
	w.mu.Unlock()

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("outbox worker stopped cleanly")
		return nil
	case <-ctx.Done():
		slog.Warn("outbox worker stop timed out")
		return ctx.Err()
	}
}

func (w *Worker) runLoop(ctx context.Context) {
	defer w.wg.Done()

	slog.Info("outbox worker started", "queue", w.cfg.EventsQueueURL, "interval", w.cfg.PollInterval)
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopChan:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.ProcessBatch(ctx)
		}
	}
}

// ProcessBatch busca e publica um lote de eventos pendentes usando FOR UPDATE SKIP LOCKED.
func (w *Worker) ProcessBatch(ctx context.Context) int {
	var publishedCount int

	err := w.uow.WithTx(ctx, func(tx port.DBTX) error {
		// Busca eventos pendentes com lock exclusivo na linha (outros workers pulam as mesmas linhas)
		events, err := w.outboxRepo.FetchPendingForPublish(ctx, tx, w.cfg.BatchSize)
		if err != nil {
			return err
		}

		for _, evt := range events {
			// Se excedeu o número máximo de tentativas
			if evt.Attempts() >= w.cfg.MaxAttempts {
				slog.Error("outbox event exceeded max attempts, skipping",
					"eventId", evt.ID().String(),
					"eventType", evt.EventType(),
					"attempts", evt.Attempts(),
				)
				continue
			}

			// Publica no SQS FIFO (MessageDeduplicationId = eventId imutável, MessageGroupId = aggregateId)
			bodyStr := string(evt.Payload())
			_, sendErr := w.sqsProducer.SendMessage(ctx, &awssqs.SendMessageInput{
				QueueUrl:               aws.String(w.cfg.EventsQueueURL),
				MessageBody:            aws.String(bodyStr),
				MessageGroupId:         aws.String(evt.AggregateID().String()),
				MessageDeduplicationId: aws.String(evt.ID().String()),
			})

			if sendErr != nil {
				slog.Warn("failed to publish outbox event to sqs, recording failure with backoff",
					"eventId", evt.ID().String(),
					"error", sendErr,
				)
				// Backoff exponencial simples: 2, 4, 8, 16... até max 60s
				delaySecs := 1 << evt.Attempts()
				if delaySecs > 60 {
					delaySecs = 60
				}
				_ = w.outboxRepo.RecordFailure(ctx, tx, evt.ID(), sendErr.Error(), delaySecs)
				continue
			}

			// Sucesso na publicação: marca published_at
			if err := w.outboxRepo.MarkPublished(ctx, tx, evt.ID()); err != nil {
				slog.Error("failed to mark outbox event as published", "eventId", evt.ID().String(), "error", err)
				return err
			}

			publishedCount++
		}
		return nil
	})

	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("error processing outbox batch", "error", err)
	}

	return publishedCount
}
