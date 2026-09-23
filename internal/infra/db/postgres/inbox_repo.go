package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/felipecristiano/desafio/internal/application/port"
	"github.com/felipecristiano/desafio/internal/domain/inbox"
)

type InboxRepository struct{}

func NewInboxRepository() *InboxRepository {
	return &InboxRepository{}
}

func (r *InboxRepository) CreateIfAbsent(ctx context.Context, tx port.DBTX, msg *inbox.InboxMessage) (bool, error) {
	// Utiliza ON CONFLICT DO NOTHING para idempotência no registro da mensagem
	query := `
		INSERT INTO inbox_messages (id, consumer_name, message_id, payload_hash, received_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (consumer_name, message_id) DO NOTHING
	`
	tag, err := tx.Exec(ctx, query,
		msg.ID(),
		msg.ConsumerName(),
		msg.MessageID(),
		msg.PayloadHash(),
		msg.ReceivedAt(),
	)
	if err != nil {
		return false, fmt.Errorf("inbox repo: create if absent: %w", err)
	}

	// RowsAffected == 1 significa que foi inserido (nova mensagem); 0 significa já existente
	return tag.RowsAffected() == 1, nil
}

func (r *InboxRepository) MarkProcessed(ctx context.Context, tx port.DBTX, consumerName, messageID string) error {
	query := `
		UPDATE inbox_messages
		SET processed_at = $1
		WHERE consumer_name = $2 AND message_id = $3
	`
	_, err := tx.Exec(ctx, query, time.Now().UTC(), consumerName, messageID)
	if err != nil {
		return fmt.Errorf("inbox repo: mark processed: %w", err)
	}
	return nil
}
