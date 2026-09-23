package inbox

import (
	"time"

	"github.com/google/uuid"
)

type InboxMessage struct {
	id           uuid.UUID
	consumerName string
	messageID    string
	payloadHash  string
	receivedAt   time.Time
	processedAt  *time.Time
}

func New(consumerName, messageID, payloadHash string) *InboxMessage {
	return &InboxMessage{
		id:           uuid.New(),
		consumerName: consumerName,
		messageID:    messageID,
		payloadHash:  payloadHash,
		receivedAt:   time.Now().UTC(),
	}
}

func Rehydrate(
	id uuid.UUID,
	consumerName, messageID, payloadHash string,
	receivedAt time.Time,
	processedAt *time.Time,
) *InboxMessage {
	return &InboxMessage{
		id:           id,
		consumerName: consumerName,
		messageID:    messageID,
		payloadHash:  payloadHash,
		receivedAt:   receivedAt,
		processedAt:  processedAt,
	}
}

func (m *InboxMessage) MarkProcessed() {
	now := time.Now().UTC()
	m.processedAt = &now
}

func (m *InboxMessage) IsProcessed() bool {
	return m.processedAt != nil
}

func (m *InboxMessage) ID() uuid.UUID           { return m.id }
func (m *InboxMessage) ConsumerName() string    { return m.consumerName }
func (m *InboxMessage) MessageID() string       { return m.messageID }
func (m *InboxMessage) PayloadHash() string     { return m.payloadHash }
func (m *InboxMessage) ReceivedAt() time.Time   { return m.receivedAt }
func (m *InboxMessage) ProcessedAt() *time.Time { return m.processedAt }
