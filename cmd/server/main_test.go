//go:build integration

package main_test

import (
	"context"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/felipecristiano/desafio/internal/application/port"
	"github.com/felipecristiano/desafio/internal/application/usecase"
	apphttp "github.com/felipecristiano/desafio/internal/http"
	"github.com/felipecristiano/desafio/internal/http/handler"
	"github.com/felipecristiano/desafio/internal/infra/auth"
	"github.com/felipecristiano/desafio/internal/infra/config"
	"github.com/felipecristiano/desafio/internal/infra/db/postgres"
	workeroutbox "github.com/felipecristiano/desafio/internal/worker/outbox"
	workerpendingref "github.com/felipecristiano/desafio/internal/worker/pendingref"
	workersqs "github.com/felipecristiano/desafio/internal/worker/sqs"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
)

type dummyValidator struct{}

func (d *dummyValidator) ValidateToken(ctx context.Context, token string) (*auth.Claims, error) {
	c := &auth.Claims{ClientID: "provider-a"}
	c.RealmAccess.Roles = []string{"internal", "provider"}
	return c, nil
}

func TestUberFxComposition_Lifecycle(t *testing.T) {
	// Configura variáveis de ambiente temporárias para o teste
	_ = os.Setenv("HTTP_PORT", "3899")
	_ = os.Setenv("DATABASE_URL", "postgres://desafio:desafio@localhost:5432/desafio?sslmode=disable")
	_ = os.Setenv("SQS_ENDPOINT", "http://localhost:4566")
	_ = os.Setenv("SQS_QUEUE_URL", "http://localhost:4566/000000000000/wager-transactions.fifo")
	_ = os.Setenv("SQS_DLQ_URL", "http://localhost:4566/000000000000/wager-transactions-dlq.fifo")
	_ = os.Setenv("SQS_EVENTS_QUEUE_URL", "http://localhost:4566/000000000000/wager-events.fifo")
	_ = os.Setenv("KEYCLOAK_ISSUER", "http://localhost:8080/realms/desafio")
	_ = os.Setenv("KEYCLOAK_JWKS_URL", "http://localhost:8080/realms/desafio/protocol/openid-connect/certs")

	var appRef *fx.App

	appRef = fx.New(
		fx.Provide(config.Load),
		fx.Provide(func(cfg *config.Config) (*pgxpool.Pool, error) {
			return postgres.NewPool(context.Background(), cfg.Database.URL)
		}),
		fx.Provide(func(p *pgxpool.Pool) port.DBTX { return p }),
		fx.Provide(postgres.NewUnitOfWork),
		fx.Provide(func() (port.WalletRepository, port.WagerTransactionRepository, port.LedgerRepository, port.InboxRepository, port.OutboxRepository) {
			return postgres.NewWalletRepository(),
				postgres.NewWagerTransactionRepository(),
				postgres.NewLedgerRepository(),
				postgres.NewInboxRepository(),
				postgres.NewOutboxRepository()
		}),
		fx.Provide(usecase.NewOpenWalletUseCase),
		fx.Provide(usecase.NewProcessWagerUseCase),
		fx.Provide(usecase.NewReconcileWalletUseCase),
		fx.Provide(func(u port.UnitOfWork, w port.WalletRepository, t port.WagerTransactionRepository, l port.LedgerRepository, o port.OutboxRepository, c *config.Config) *usecase.RetryPendingReferencesUseCase {
			return usecase.NewRetryPendingReferencesUseCase(u, w, t, l, o, c.PendingRef.MaxRetries)
		}),
		fx.Provide(func() auth.TokenValidator { return &dummyValidator{} }),
		fx.Provide(func(p *pgxpool.Pool, sqs *awssqs.Client) *handler.HealthHandler {
			return handler.NewHealthHandlerWithSQS(p, sqs)
		}),
		fx.Provide(handler.NewWalletHandler),
		fx.Provide(handler.NewWagerHandler),
		fx.Provide(apphttp.NewRouter),
		fx.Provide(func(cfg *config.Config) (*awssqs.Client, error) {
			awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
				awsconfig.WithRegion("us-east-1"),
				awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
			)
			if err != nil {
				return nil, err
			}
			return awssqs.NewFromConfig(awsCfg, func(o *awssqs.Options) {
				o.BaseEndpoint = aws.String(cfg.SQS.Endpoint)
			}), nil
		}),
		fx.Provide(func(sqsClient *awssqs.Client, uc *usecase.ProcessWagerUseCase, cfg *config.Config) *workersqs.Consumer {
			return workersqs.NewConsumer(sqsClient, uc, workersqs.ConsumerConfig{
				QueueURL:          cfg.SQS.QueueURL,
				ConsumerName:      "test-consumer",
				MaxMessages:       1,
				VisibilityTimeout: 10,
				WaitTimeSeconds:   1,
			})
		}),
		fx.Provide(func(sqsClient *awssqs.Client, uow port.UnitOfWork, outboxRepo port.OutboxRepository, cfg *config.Config) *workeroutbox.Worker {
			return workeroutbox.NewWorker(sqsClient, uow, outboxRepo, workeroutbox.WorkerConfig{
				EventsQueueURL: cfg.SQS.EventsQueueURL,
				PollInterval:   500 * time.Millisecond,
				BatchSize:      5,
				MaxAttempts:    3,
			})
		}),
		fx.Provide(workerpendingref.NewPendingReferenceWorker),

		fx.Invoke(func(
			lc fx.Lifecycle,
			router *chi.Mux,
			sqsConsumer *workersqs.Consumer,
			outboxWorker *workeroutbox.Worker,
			pendingRefWorker *workerpendingref.PendingReferenceWorker,
			cfg *config.Config,
		) {
			server := &http.Server{Addr: ":" + cfg.HTTP.Port, Handler: router}

			lc.Append(fx.Hook{
				OnStart: func(ctx context.Context) error {
					sqsConsumer.Start(context.Background())
					outboxWorker.Start(context.Background())
					pendingRefWorker.Start(context.Background())
					go func() { _ = server.ListenAndServe() }()
					return nil
				},
				OnStop: func(ctx context.Context) error {
					_ = server.Shutdown(ctx)
					var wg sync.WaitGroup
					wg.Add(3)
					go func() { defer wg.Done(); _ = sqsConsumer.Stop(ctx) }()
					go func() { defer wg.Done(); _ = outboxWorker.Stop(ctx) }()
					go func() { defer wg.Done(); _ = pendingRefWorker.Stop(ctx) }()
					wg.Wait()
					return nil
				},
			})
		}),
	)

	startCtx, cancelStart := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStart()

	if err := appRef.Start(startCtx); err != nil {
		t.Fatalf("failed to start fx app: %v", err)
	}

	// Verifica se a aplicação responde via HTTP
	resp, err := http.Get("http://localhost:3899/health/live")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("http server not responding on /health/live: %v", err)
	}
	_ = resp.Body.Close()

	readyResp, err := http.Get("http://localhost:3899/health/ready")
	if err != nil || readyResp.StatusCode != http.StatusOK {
		t.Fatalf("http server not ready on /health/ready: %v", err)
	}
	_ = readyResp.Body.Close()

	// Testa encerramento gracioso via fx.Lifecycle
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStop()

	if err := appRef.Stop(stopCtx); err != nil {
		t.Fatalf("failed to gracefully stop fx app: %v", err)
	}
}
