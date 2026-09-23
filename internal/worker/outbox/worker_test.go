//go:build integration

package outbox_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/felipecristiano/desafio/internal/application/port"
	"github.com/felipecristiano/desafio/internal/domain/events"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/felipecristiano/desafio/internal/domain/outbox"
	"github.com/felipecristiano/desafio/internal/domain/transaction"
	"github.com/felipecristiano/desafio/internal/infra/db/postgres"
	workeroutbox "github.com/felipecristiano/desafio/internal/worker/outbox"
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

func TestOutboxWorker_EndToEnd(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	sqsClient := getSQSClient(t)
	ctx := context.Background()

	_, execErr := pool.Exec(ctx, "DELETE FROM outbox_events")
	if execErr != nil {
		t.Fatalf("failed to clear outbox_events: %v", execErr)
	}

	uow := postgres.NewUnitOfWork(pool)
	outboxRepo := postgres.NewOutboxRepository()

	eventsQueueURL := os.Getenv("SQS_EVENTS_QUEUE_URL")
	if eventsQueueURL == "" {
		eventsQueueURL = "http://localhost:4566/000000000000/wager-events.fifo"
	}

	worker := workeroutbox.NewWorker(sqsClient, uow, outboxRepo, workeroutbox.WorkerConfig{
		EventsQueueURL: eventsQueueURL,
		PollInterval:   200 * time.Millisecond,
		BatchSize:      10,
		MaxAttempts:    5,
	})

	// Limpa outbox_events para isolamento do teste
	_, _ = pool.Exec(ctx, "DELETE FROM outbox_events")

	// 1. Cria 2 eventos de outbox pendentes no banco
	walletID := uuid.New()
	playerID := uuid.New()
	txnID := uuid.New()
	corrID := uuid.New()

	evt1 := events.NewWagerTransactionProcessed(
		txnID, walletID, playerID, nil, nil,
		string(transaction.KindBet),
		money.NewFromInt64(2500, money.BRL),
		money.NewFromInt64(97500, money.BRL),
		corrID, nil,
	)
	outEvt1, err := outbox.New(
		evt1.EventID, evt1.EventType, walletID, "Wallet",
		&corrID, nil, evt1, time.Now().UTC(), 1,
	)
	if err != nil {
		t.Fatalf("failed to create outbox event 1: %v", err)
	}

	evt2 := events.NewWalletBalanceChanged(
		walletID, txnID, "DEBIT",
		money.NewFromInt64(2500, money.BRL),
		money.NewFromInt64(100000, money.BRL),
		money.NewFromInt64(97500, money.BRL),
		2, corrID, nil,
	)
	outEvt2, err := outbox.New(
		evt2.EventID, evt2.EventType, walletID, "Wallet",
		&corrID, nil, evt2, time.Now().UTC(), 1,
	)
	if err != nil {
		t.Fatalf("failed to create outbox event 2: %v", err)
	}

	err = uow.WithTx(ctx, func(tx port.DBTX) error {
		if err := outboxRepo.Create(ctx, tx, outEvt1); err != nil {
			return err
		}
		return outboxRepo.Create(ctx, tx, outEvt2)
	})
	if err != nil {
		t.Fatalf("failed to insert outbox events: %v", err)
	}

	// 2. Executa ProcessBatch diretamente e valida publicação
	published := worker.ProcessBatch(ctx)
	if published < 2 {
		t.Fatalf("expected at least 2 published events, got %d", published)
	}

	// 3. Verifica no banco que os 2 eventos do teste foram marcados como publicados
	var pubCount int
	_ = pool.QueryRow(ctx, "SELECT COUNT(*) FROM outbox_events WHERE id IN ($1, $2) AND published_at IS NOT NULL", outEvt1.ID(), outEvt2.ID()).Scan(&pubCount)
	if pubCount != 2 {
		t.Fatalf("expected both test events to have published_at filled, got %d", pubCount)
	}

	// 4. Inicia o worker em background e testa parada graciosa
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	worker.Start(workerCtx)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := worker.Stop(stopCtx); err != nil {
		t.Fatalf("failed to stop outbox worker cleanly: %v", err)
	}
}
