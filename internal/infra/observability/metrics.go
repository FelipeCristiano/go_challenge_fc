package observability

import (
	"log/slog"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// WagerTransactionsTotal conta as transações de apostas processadas por status, tipo (kind) e origem (source: HTTP ou SQS).
	WagerTransactionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "wager_transactions_total",
			Help: "Total number of wager transactions partitioned by status, kind, and source.",
		},
		[]string{"status", "kind", "source"},
	)

	// WagerProcessingDurationSeconds mede o histograma de latência de processamento das transações.
	WagerProcessingDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "wager_processing_duration_seconds",
			Help:    "Latency of wager transaction processing in seconds.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"kind", "source", "status"},
	)

	// WagerDuplicatesTotal conta as requisições duplicadas identificadas (idempotência ou inbox).
	WagerDuplicatesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "wager_transactions_duplicates_total",
			Help: "Total number of duplicate transaction attempts detected.",
		},
		[]string{"source", "type"}, // type: "idempotent_replay", "inbox_duplicate"
	)

	// WagerRetriesTotal registra as retentativas efetuadas pelos workers.
	WagerRetriesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "wager_retries_total",
			Help: "Total number of worker retries performed.",
		},
		[]string{"worker", "result"}, // worker: "pending_ref", "outbox", "sqs"; result: "success", "failure", "retry", "max_reached"
	)

	// WagerDLQMessagesTotal conta as mensagens enviadas para DLQ ou descartadas por esgotamento/poison pill.
	WagerDLQMessagesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "wager_dlq_messages_total",
			Help: "Total number of messages sent to DLQ or discarded as poison pill.",
		},
		[]string{"reason"}, // "max_retries_exceeded", "poison_pill"
	)

	// WagerConcurrencyConflictsTotal registra conflitos de concorrência ou payload conflict.
	WagerConcurrencyConflictsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "wager_concurrency_conflicts_total",
			Help: "Total number of concurrency or payload conflicts encountered.",
		},
		[]string{"operation", "type"}, // operation: "process_wager"; type: "payload_conflict", "lock_timeout"
	)

	// OutboxPublishDelaySeconds mede o atraso em segundos entre a ocorrência do evento de domínio e sua publicação.
	OutboxPublishDelaySeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "outbox_publish_delay_seconds",
			Help:    "Delay in seconds between domain event creation and outbox publication.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
		},
	)

	// OutboxEventsPublishedTotal conta eventos publicados pela outbox.
	OutboxEventsPublishedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "outbox_published_events_total",
			Help: "Total number of outbox events published.",
		},
		[]string{"event_type", "status"}, // status: "success", "failure"
	)

	// ReconciliationChecksTotal conta as checagens de reconciliação executadas.
	ReconciliationChecksTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "reconciliation_checks_total",
			Help: "Total number of wallet reconciliations performed.",
		},
		[]string{"status"}, // "ok", "divergent"
	)

	// ReconciliationDivergencesTotal conta divergências detectadas na reconciliação de saldo.
	ReconciliationDivergencesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "reconciliation_divergences_total",
			Help: "Total number of financial divergences detected in wallet reconciliations.",
		},
		[]string{"currency"},
	)
)

// InitJSONLogger configura o logger padrão estruturado em formato JSON.
func InitJSONLogger(level slog.Level) *slog.Logger {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
	slog.SetDefault(logger)
	return logger
}
