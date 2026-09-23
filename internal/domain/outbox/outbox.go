package outbox

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type OutboxEvent struct {
	id            uuid.UUID
	eventType     string
	aggregateID   uuid.UUID
	aggregateType string
	correlationID *uuid.UUID
	causationID   *uuid.UUID
	payload       json.RawMessage
	occurredAt    time.Time
	version       int
	publishedAt   *time.Time
	attempts      int
	nextAttemptAt time.Time
	lastError     *string

	createdAt time.Time
}

func New(
	eventID uuid.UUID,
	eventType string,
	aggregateID uuid.UUID,
	aggregateType string,
	correlationID *uuid.UUID,
	causationID *uuid.UUID,
	payload any,
	occurredAt time.Time,
	version int,
) (*OutboxEvent, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("outbox: marshal payload for %s: %w", eventType, err)
	}
	now := time.Now().UTC()
	return &OutboxEvent{
		id:            eventID,
		eventType:     eventType,
		aggregateID:   aggregateID,
		aggregateType: aggregateType,
		correlationID: correlationID,
		causationID:   causationID,
		payload:       raw,
		occurredAt:    occurredAt,
		version:       version,
		nextAttemptAt: now,
		createdAt:     now,
	}, nil
}


func Rehydrate(
	id uuid.UUID,
	eventType string,
	aggregateID uuid.UUID,
	aggregateType string,
	correlationID, causationID *uuid.UUID,
	payload json.RawMessage,
	occurredAt time.Time,
	version int,
	publishedAt *time.Time,
	attempts int,
	nextAttemptAt time.Time,
	lastError *string,
	createdAt time.Time,
) *OutboxEvent {
	return &OutboxEvent{
		id:            id,
		eventType:     eventType,
		aggregateID:   aggregateID,
		aggregateType: aggregateType,
		correlationID: correlationID,
		causationID:   causationID,
		payload:       payload,
		occurredAt:    occurredAt,
		version:       version,
		publishedAt:   publishedAt,
		attempts:      attempts,
		nextAttemptAt: nextAttemptAt,
		lastError:     lastError,
		createdAt:     createdAt,
	}
}


func (e *OutboxEvent) ID() uuid.UUID              { return e.id }
func (e *OutboxEvent) EventType() string          { return e.eventType }
func (e *OutboxEvent) AggregateID() uuid.UUID     { return e.aggregateID }
func (e *OutboxEvent) AggregateType() string      { return e.aggregateType }
func (e *OutboxEvent) CorrelationID() *uuid.UUID  { return e.correlationID }
func (e *OutboxEvent) CausationID() *uuid.UUID    { return e.causationID }
func (e *OutboxEvent) Payload() json.RawMessage   { return e.payload }
func (e *OutboxEvent) OccurredAt() time.Time      { return e.occurredAt }
func (e *OutboxEvent) Version() int               { return e.version }
func (e *OutboxEvent) PublishedAt() *time.Time    { return e.publishedAt }
func (e *OutboxEvent) Attempts() int              { return e.attempts }
func (e *OutboxEvent) NextAttemptAt() time.Time   { return e.nextAttemptAt }
func (e *OutboxEvent) LastError() *string         { return e.lastError }
func (e *OutboxEvent) CreatedAt() time.Time       { return e.createdAt }
func (e *OutboxEvent) IsPublished() bool          { return e.publishedAt != nil }
