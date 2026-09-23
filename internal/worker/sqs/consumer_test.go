//go:build integration

package sqs_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/felipecristiano/desafio/internal/application/port"
	"github.com/felipecristiano/desafio/internal/application/usecase"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/felipecristiano/desafio/internal/domain/transaction"
	"github.com/felipecristiano/desafio/internal/infra/db/postgres"
	workersqs "github.com/felipecristiano/desafio/internal/worker/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func getTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://desafio:desafio@localhost:5432/desafio?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := postgres.NewPool(ctx, dbURL)
	if err != nil {
		t.Fatalf("failed to connect to test db: %v", err)
	}
	return pool
}

func getSQSClient(t *testing.T) *awssqs.Client {
	t.Helper()
	endpoint := os.Getenv("SQS_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:4566"
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("failed to load aws config: %v", err)
	}

	client := awssqs.NewFromConfig(cfg, func(o *awssqs.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})
	return client
}

func TestSQSConsumer_EndToEnd(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	sqsClient := getSQSClient(t)
	ctx := context.Background()

	_, _ = pool.Exec(ctx, "TRUNCATE outbox_events, inbox_messages, wallet_ledger_entries, wager_transactions, wallets CASCADE")

	uow := postgres.NewUnitOfWork(pool)
	walletRepo := postgres.NewWalletRepository()
	txnRepo := postgres.NewWagerTransactionRepository()
	ledgerRepo := postgres.NewLedgerRepository()
	inboxRepo := postgres.NewInboxRepository()
	outboxRepo := postgres.NewOutboxRepository()

	openWalletUC := usecase.NewOpenWalletUseCase(uow, walletRepo, txnRepo, ledgerRepo, outboxRepo)
	processWagerUC := usecase.NewProcessWagerUseCase(uow, walletRepo, txnRepo, ledgerRepo, inboxRepo, outboxRepo)

	// 1. Cria carteira com saldo inicial de 100.00 BRL
	playerID := uuid.New()
	walletOut, err := openWalletUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: money.NewFromInt64(10000, money.BRL),
		CorrelationID:  uuid.New(),
	})
	if err != nil {
		t.Fatalf("failed to create wallet: %v", err)
	}
	walletID := walletOut.ID

	queueURL := os.Getenv("SQS_QUEUE_URL")
	if queueURL == "" {
		queueURL = "http://localhost:4566/000000000000/wager-transactions.fifo"
	}

	consumer := workersqs.NewConsumer(sqsClient, processWagerUC, workersqs.ConsumerConfig{
		QueueURL:          queueURL,
		ConsumerName:      "test-consumer",
		MaxMessages:       5,
		VisibilityTimeout: 10,
		WaitTimeSeconds:   1,
	})

	// Inicia o consumidor assíncrono
	consumerCtx, cancelConsumer := context.WithCancel(context.Background())
	defer cancelConsumer()
	consumer.Start(consumerCtx)

	// 2. Envia mensagem SQS com BET de 30.00 BRL
	extTxnID := "tx-sqs-" + uuid.New().String()[:8]
	envelope := workersqs.EnvelopeMessage{
		MessageID:  "msg-" + extTxnID,
		Type:       "WagerTransactionRequested",
		OccurredAt: time.Now().UTC().Format(time.RFC3339),
		Data: workersqs.WagerMessageData{
			ProviderID:            "provider-a",
			ExternalTransactionID: extTxnID,
			IdempotencyKey:        "provider-a:" + extTxnID,
			PlayerID:              playerID.String(),
			WalletID:              walletID.String(),
			RoundID:               "round-sqs-1",
			GameID:                "fortune-sqs",
			Kind:                  "BET",
			Money: workersqs.MoneyDTO{
				Amount:   "30.00",
				Currency: "BRL",
			},
		},
	}
	bodyBytes, _ := json.Marshal(envelope)

	_, err = sqsClient.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:               aws.String(queueURL),
		MessageBody:            aws.String(string(bodyBytes)),
		MessageGroupId:         aws.String(walletID.String()), // MessageGroupId = walletId
		MessageDeduplicationId: aws.String(extTxnID),
	})
	if err != nil {
		t.Fatalf("failed to send sqs message: %v", err)
	}

	// 3. Aguarda o processamento pelo consumidor
	var processedTxn *transaction.WagerTransaction
	for i := 0; i < 20; i++ {
		time.Sleep(300 * time.Millisecond)
		_ = uow.WithTx(ctx, func(tx port.DBTX) error {
			t, err := txnRepo.GetByProviderAndExternalID(ctx, tx, "provider-a", extTxnID)
			if err == nil && t != nil {
				processedTxn = t
			}
			return nil
		})
		if processedTxn != nil {
			break
		}
	}

	if processedTxn == nil {
		t.Fatalf("timed out waiting for sqs message to be processed")
	}

	if processedTxn.Status() != transaction.StatusProcessed {
		t.Fatalf("expected transaction status PROCESSED, got %s", processedTxn.Status())
	}
	if processedTxn.ResultBalance().Amount() != 7000 { // 100.00 - 30.00 = 70.00
		t.Fatalf("expected balance 7000, got %d", processedTxn.ResultBalance().Amount())
	}

	// 4. Verifica deduplicação na Inbox
	var inboxCount int
	_ = pool.QueryRow(ctx, "SELECT COUNT(*) FROM inbox_messages WHERE message_id = $1", envelope.MessageID).Scan(&inboxCount)
	if inboxCount != 1 {
		t.Fatalf("expected 1 record in inbox_messages, got %d", inboxCount)
	}

	// 5. Shutdown gracioso do consumidor
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := consumer.Stop(stopCtx); err != nil {
		t.Fatalf("failed to stop consumer cleanly: %v", err)
	}
}
