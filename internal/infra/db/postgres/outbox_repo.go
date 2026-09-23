package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/felipecristiano/desafio/internal/application/port"
	"github.com/felipecristiano/desafio/internal/domain/outbox"
	"github.com/google/uuid"
)

type OutboxRepository struct{}

func NewOutboxRepository() *OutboxRepository {
	return &OutboxRepository{}
}

func (r *OutboxRepository) Create(ctx context.Context, tx port.DBTX, event *outbox.OutboxEvent) error {
	query := `
		INSERT INTO outbox_events (
			id, event_type, aggregate_id, aggregate_type, correlation_id, causation_id,
			payload, occurred_at, version, published_at, attempts, next_attempt_at, last_error, created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12, $13, $14
		)
	`
	_, err := tx.Exec(ctx, query,
		event.ID(),
		event.EventType(),
		event.AggregateID(),
		event.AggregateType(),
		event.CorrelationID(),
		event.CausationID(),
		event.Payload(),
		event.OccurredAt(),
		event.Version(),
		event.PublishedAt(),
		event.Attempts(),
		event.NextAttemptAt(),
		event.LastError(),
		event.CreatedAt(),
	)
	if err != nil {
		return fmt.Errorf("outbox repo: create: %w", err)
	}
	return nil
}

func (r *OutboxRepository) FetchPendingForPublish(ctx context.Context, tx port.DBTX, batchSize int) ([]*outbox.OutboxEvent, error) {
	if batchSize <= 0 {
		batchSize = 50
	}

	// FOR UPDATE SKIP LOCKED permite múltiplos publishers sem contenção nem lock global
	query := `
		SELECT
			id, event_type, aggregate_id, aggregate_type, correlation_id, causation_id,
			payload, occurred_at, version, published_at, attempts, next_attempt_at, last_error, created_at
		FROM outbox_events
		WHERE published_at IS NULL AND next_attempt_at <= NOW()
		ORDER BY next_attempt_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`

	rows, err := tx.Query(ctx, query, batchSize)
	if err != nil {
		return nil, fmt.Errorf("outbox repo: fetch pending: %w", err)
	}
	defer rows.Close()

	var events []*outbox.OutboxEvent
	for rows.Next() {
		var (
			id            uuid.UUID
			eventType     string
			aggregateID   uuid.UUID
			aggregateType string
			correlationID *uuid.UUID
			causationID   *uuid.UUID
			payload       []byte
			occurredAt    time.Time
			version       int
			publishedAt   *time.Time
			attempts      int
			nextAttemptAt time.Time
			lastError     *string
			createdAt     time.Time
		)

		err := rows.Scan(
			&id, &eventType, &aggregateID, &aggregateType, &correlationID, &causationID,
			&payload, &occurredAt, &version, &publishedAt, &attempts, &nextAttemptAt, &lastError, &createdAt,
		)
		if err != nil {
			return nil, fmt.Errorf("outbox repo: scan pending event: %w", err)
		}

		events = append(events, outbox.Rehydrate(
			id, eventType, aggregateID, aggregateType, correlationID, causationID,
			json.RawMessage(payload), occurredAt, version, publishedAt, attempts, nextAttemptAt, lastError, createdAt,
		))
	}

	return events, rows.Err()
}

func (r *OutboxRepository) MarkPublished(ctx context.Context, tx port.DBTX, id uuid.UUID) error {
	query := `
		UPDATE outbox_events
		SET published_at = $1
		WHERE id = $2
	`
	_, err := tx.Exec(ctx, query, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("outbox repo: mark published: %w", err)
	}
	return nil
}

func (r *OutboxRepository) RecordFailure(ctx context.Context, tx port.DBTX, id uuid.UUID, lastErr string, nextAttemptAfterSecs int) error {
	query := `
		UPDATE outbox_events
		SET attempts = attempts + 1,
			last_error = $1,
			next_attempt_at = NOW() + ($2 || ' seconds')::interval
		WHERE id = $3
	`
	_, err := tx.Exec(ctx, query, lastErr, fmt.Sprintf("%d", nextAttemptAfterSecs), id)
	if err != nil {
		return fmt.Errorf("outbox repo: record failure: %w", err)
	}
	return nil
}
