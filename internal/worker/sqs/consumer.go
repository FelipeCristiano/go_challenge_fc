package sqs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/felipecristiano/desafio/internal/application/usecase"
	"github.com/felipecristiano/desafio/internal/domain/errs"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/felipecristiano/desafio/internal/domain/transaction"
	"github.com/felipecristiano/desafio/internal/infra/observability"
	"github.com/google/uuid"
)

// SQSClientAPI define o contrato mínimo do AWS SDK SQS para o consumidor.
type SQSClientAPI interface {
	ReceiveMessage(ctx context.Context, params *awssqs.ReceiveMessageInput, optFns ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, params *awssqs.DeleteMessageInput, optFns ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, params *awssqs.ChangeMessageVisibilityInput, optFns ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error)
}

type ConsumerConfig struct {
	QueueURL          string
	ConsumerName      string
	MaxMessages       int32
	VisibilityTimeout int32
	WaitTimeSeconds   int32
}

// EnvelopeMessage representa a estrutura da mensagem consumida da fila SQS FIFO.
type EnvelopeMessage struct {
	MessageID  string          `json:"messageId"`
	Type       string          `json:"type"`
	OccurredAt string          `json:"occurredAt"`
	Data       WagerMessageData `json:"data"`
}

type WagerMessageData struct {
	ProviderID                     string   `json:"providerId"`
	ExternalTransactionID          string   `json:"externalTransactionId"`
	IdempotencyKey                 string   `json:"idempotencyKey"`
	PlayerID                       string   `json:"playerId"`
	WalletID                       string   `json:"walletId"`
	RoundID                        string   `json:"roundId"`
	GameID                         string   `json:"gameId"`
	Kind                           string   `json:"kind"`
	Money                          MoneyDTO `json:"money"`
	ReferenceExternalTransactionID *string  `json:"referenceExternalTransactionId,omitempty"`
}

type MoneyDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type Consumer struct {
	sqsClient      SQSClientAPI
	processWagerUC *usecase.ProcessWagerUseCase
	cfg            ConsumerConfig
	stopChan       chan struct{}
	wg             sync.WaitGroup
	running        bool
	mu             sync.Mutex
}

func NewConsumer(
	sqsClient SQSClientAPI,
	processWagerUC *usecase.ProcessWagerUseCase,
	cfg ConsumerConfig,
) *Consumer {
	if cfg.MaxMessages <= 0 {
		cfg.MaxMessages = 10
	}
	if cfg.VisibilityTimeout <= 0 {
		cfg.VisibilityTimeout = 60
	}
	if cfg.WaitTimeSeconds <= 0 {
		cfg.WaitTimeSeconds = 20
	}
	if cfg.ConsumerName == "" {
		cfg.ConsumerName = "sqs-wager-consumer"
	}

	return &Consumer{
		sqsClient:      sqsClient,
		processWagerUC: processWagerUC,
		cfg:            cfg,
		stopChan:       make(chan struct{}),
	}
}

// Start inicia o loop de consumo assíncrono em background.
func (c *Consumer) Start(ctx context.Context) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.mu.Unlock()

	c.wg.Add(1)
	go c.pollLoop(ctx)
}

// Stop sinaliza a parada do consumo e aguarda o término do lote atual respeitando o contexto.
func (c *Consumer) Stop(ctx context.Context) error {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return nil
	}
	c.running = false
	close(c.stopChan)
	c.mu.Unlock()

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("sqs consumer stopped cleanly")
		return nil
	case <-ctx.Done():
		slog.Warn("sqs consumer stop timed out, some in-flight messages may be redelivered")
		return ctx.Err()
	}
}

func (c *Consumer) pollLoop(ctx context.Context) {
	defer c.wg.Done()

	slog.Info("sqs consumer started", "queue", c.cfg.QueueURL)

	for {
		select {
		case <-c.stopChan:
			return
		case <-ctx.Done():
			return
		default:
		}

		output, err := c.sqsClient.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:              aws.String(c.cfg.QueueURL),
			MaxNumberOfMessages:   c.cfg.MaxMessages,
			VisibilityTimeout:     c.cfg.VisibilityTimeout,
			WaitTimeSeconds:       c.cfg.WaitTimeSeconds,
			AttributeNames:        []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameAll},
			MessageAttributeNames: []string{"All"},
		})

		if err != nil {
			if errors.Is(err, context.Canceled) || !c.isRunning() {
				return
			}
			slog.Error("error receiving sqs messages", "error", err)
			time.Sleep(1 * time.Second)
			continue
		}

		for _, msg := range output.Messages {
			c.handleMessage(ctx, msg)
		}
	}
}

func (c *Consumer) handleMessage(ctx context.Context, msg sqstypes.Message) {
	if msg.Body == nil {
		c.deleteMessage(ctx, msg)
		return
	}

	var envelope EnvelopeMessage
	if err := json.Unmarshal([]byte(*msg.Body), &envelope); err != nil {
		observability.WagerDLQMessagesTotal.WithLabelValues("poison_pill").Inc()
		slog.Error("malformed message body in sqs, deleting poison pill",
			"error", err,
			"receiptHandle", *msg.ReceiptHandle,
		)
		// Mensagens malformadas de forma irrecuperável são deletadas para evitar loop eterno
		c.deleteMessage(ctx, msg)
		return
	}

	if envelope.Type != "WagerTransactionRequested" {
		observability.WagerDLQMessagesTotal.WithLabelValues("unknown_type").Inc()
		slog.Warn("ignoring unknown sqs message type", "type", envelope.Type)
		c.deleteMessage(ctx, msg)
		return
	}

	// Parsing dos campos de negócio
	data := envelope.Data
	playerID, err := uuid.Parse(data.PlayerID)
	if err != nil {
		observability.WagerDLQMessagesTotal.WithLabelValues("poison_pill").Inc()
		slog.Error("invalid playerId in sqs message", "playerId", data.PlayerID, "messageId", envelope.MessageID)
		c.deleteMessage(ctx, msg)
		return
	}

	walletID, err := uuid.Parse(data.WalletID)
	if err != nil {
		observability.WagerDLQMessagesTotal.WithLabelValues("poison_pill").Inc()
		slog.Error("invalid walletId in sqs message", "walletId", data.WalletID, "messageId", envelope.MessageID)
		c.deleteMessage(ctx, msg)
		return
	}

	m, err := money.NewFromExternalString(data.Money.Amount, money.Currency(data.Money.Currency))
	if err != nil {
		observability.WagerDLQMessagesTotal.WithLabelValues("poison_pill").Inc()
		slog.Error("invalid money in sqs message", "amount", data.Money.Amount, "currency", data.Money.Currency, "messageId", envelope.MessageID)
		c.deleteMessage(ctx, msg)
		return
	}

	idempotencyKey := data.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey = fmt.Sprintf("%s:%s", data.ProviderID, data.ExternalTransactionID)
	}

	corrID := uuid.New()
	slog.Info("processing sqs wager transaction",
		"correlationId", corrID.String(),
		"messageId", envelope.MessageID,
		"walletId", walletID.String(),
		"providerId", data.ProviderID,
		"idempotencyKey", idempotencyKey,
		"kind", data.Kind,
	)

	// Executa caso de uso compartilhado
	out, err := c.processWagerUC.Execute(ctx, usecase.ProcessWagerInput{
		Source:                         "SQS",
		ConsumerName:                   c.cfg.ConsumerName,
		MessageID:                      envelope.MessageID,
		IdempotencyKey:                 idempotencyKey,
		CorrelationID:                  corrID,
		ProviderID:                     data.ProviderID,
		ExternalTransactionID:          data.ExternalTransactionID,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        data.RoundID,
		GameID:                         data.GameID,
		Kind:                           transaction.Kind(data.Kind),
		Money:                          m,
		ReferenceExternalTransactionID: data.ReferenceExternalTransactionID,
	})

	if err != nil {
		// Erros de domínio irreversíveis (ex.: conflito de payload, carteira inexistente, OPENING proibido)
		if errors.Is(err, errs.ErrPayloadConflict) ||
			errors.Is(err, errs.ErrOpeningForbidden) ||
			errors.Is(err, errs.ErrWalletNotFound) ||
			errors.Is(err, errs.ErrInvalidInput) {
			observability.WagerDLQMessagesTotal.WithLabelValues("terminal_domain_error").Inc()
			slog.Error("terminal domain error in sqs message, removing from queue",
				"correlationId", corrID.String(),
				"messageId", envelope.MessageID,
				"walletId", walletID.String(),
				"providerId", data.ProviderID,
				"error", err.Error(),
			)
			c.deleteMessage(ctx, msg)
			return
		}

		// Falhas transitórias (ex.: timeout de conexão com banco de dados)
		observability.WagerRetriesTotal.WithLabelValues("sqs", "failure").Inc()
		slog.Warn("transient error processing sqs message, releasing visibility for retry",
			"correlationId", corrID.String(),
			"messageId", envelope.MessageID,
			"walletId", walletID.String(),
			"providerId", data.ProviderID,
			"error", err.Error(),
		)
		c.releaseVisibility(ctx, msg)
		return
	}

	// Transações concluídas (PROCESSED, REJECTED ou PENDING_REFERENCE) foram persistidas duravelmente
	slog.Info("sqs wager transaction processed successfully",
		"correlationId", corrID.String(),
		"messageId", envelope.MessageID,
		"transactionId", out.TransactionID.String(),
		"walletId", walletID.String(),
		"providerId", data.ProviderID,
		"status", string(out.Status),
		"idempotentReplay", out.IdempotentReplay,
	)

	// Só remove da fila após o commit no banco
	c.deleteMessage(ctx, msg)
}

func (c *Consumer) deleteMessage(ctx context.Context, msg sqstypes.Message) {
	_, err := c.sqsClient.DeleteMessage(ctx, &awssqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.cfg.QueueURL),
		ReceiptHandle: msg.ReceiptHandle,
	})
	if err != nil {
		slog.Error("failed to delete message from sqs", "error", err, "messageId", *msg.MessageId)
	}
}

func (c *Consumer) releaseVisibility(ctx context.Context, msg sqstypes.Message) {
	_, err := c.sqsClient.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(c.cfg.QueueURL),
		ReceiptHandle:     msg.ReceiptHandle,
		VisibilityTimeout: 0, // Libera imediatamente para nova tentativa
	})
	if err != nil {
		slog.Error("failed to release message visibility", "error", err, "messageId", *msg.MessageId)
	}
}

func (c *Consumer) isRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}
