package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
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

func main() {
	// Logger JSON estruturado padronizado para stdout
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	app := fx.New(
		// 1. Configuração
		fx.Provide(config.Load),

		// 2. Infraestrutura de Banco e Migrations
		fx.Provide(provideDatabasePool),
		fx.Provide(provideDBTX),
		fx.Provide(postgres.NewUnitOfWork),

		// 3. Repositórios
		fx.Provide(provideRepositories),

		// 4. Casos de Uso
		fx.Provide(provideUseCases),

		// 5. Autenticação e Handlers HTTP
		fx.Provide(provideAuthValidator),
		fx.Provide(provideHealthHandler),
		fx.Provide(handler.NewWalletHandler),
		fx.Provide(handler.NewWagerHandler),
		fx.Provide(apphttp.NewRouter),

		// 6. Mensageria AWS/LocalStack SQS Client
		fx.Provide(provideSQSClient),

		// 7. Workers em Background (Consumidor SQS, Outbox, Pending References)
		fx.Provide(provideSQSConsumer),
		fx.Provide(provideOutboxWorker),
		fx.Provide(workerpendingref.NewPendingReferenceWorker),

		// 8. Inicialização e Ciclo de Vida (fx.Lifecycle)
		fx.Invoke(registerLifecycleHooks),
	)

	app.Run()
}

// Provedores de Dependências

func provideDatabasePool(lc fx.Lifecycle, cfg *config.Config) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := postgres.NewPool(ctx, cfg.Database.URL)
	if err != nil {
		return nil, fmt.Errorf("main: init postgres pool: %w", err)
	}

	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			slog.Info("closing postgres connection pool")
			pool.Close()
			return nil
		},
	})

	return pool, nil
}

func provideDBTX(pool *pgxpool.Pool) port.DBTX {
	return pool
}

type Repositories struct {
	fx.Out

	WalletRepo port.WalletRepository
	TxnRepo    port.WagerTransactionRepository
	LedgerRepo port.LedgerRepository
	InboxRepo  port.InboxRepository
	OutboxRepo port.OutboxRepository
}

func provideRepositories() Repositories {
	return Repositories{
		WalletRepo: postgres.NewWalletRepository(),
		TxnRepo:    postgres.NewWagerTransactionRepository(),
		LedgerRepo: postgres.NewLedgerRepository(),
		InboxRepo:  postgres.NewInboxRepository(),
		OutboxRepo: postgres.NewOutboxRepository(),
	}
}

type UseCases struct {
	fx.Out

	OpenWalletUC      *usecase.OpenWalletUseCase
	ProcessWagerUC    *usecase.ProcessWagerUseCase
	ReconcileUC       *usecase.ReconcileWalletUseCase
	RetryPendingRefUC *usecase.RetryPendingReferencesUseCase
}

func provideUseCases(
	uow port.UnitOfWork,
	walletRepo port.WalletRepository,
	txnRepo port.WagerTransactionRepository,
	ledgerRepo port.LedgerRepository,
	inboxRepo port.InboxRepository,
	outboxRepo port.OutboxRepository,
	cfg *config.Config,
) UseCases {
	return UseCases{
		OpenWalletUC:      usecase.NewOpenWalletUseCase(uow, walletRepo, txnRepo, ledgerRepo, outboxRepo),
		ProcessWagerUC:    usecase.NewProcessWagerUseCase(uow, walletRepo, txnRepo, ledgerRepo, inboxRepo, outboxRepo),
		ReconcileUC:       usecase.NewReconcileWalletUseCase(uow, walletRepo, ledgerRepo),
		RetryPendingRefUC: usecase.NewRetryPendingReferencesUseCase(uow, walletRepo, txnRepo, ledgerRepo, outboxRepo, cfg.PendingRef.MaxRetries),
	}
}

func provideAuthValidator(cfg *config.Config) auth.TokenValidator {
	return auth.NewKeycloakValidator(cfg.Auth.JWKSURL, cfg.Auth.Issuer)
}

func provideHealthHandler(pool *pgxpool.Pool, sqsClient *awssqs.Client) *handler.HealthHandler {
	return handler.NewHealthHandlerWithSQS(pool, sqsClient)
}

func provideSQSClient(cfg *config.Config) (*awssqs.Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		return nil, fmt.Errorf("main: load aws config: %w", err)
	}

	client := awssqs.NewFromConfig(awsCfg, func(o *awssqs.Options) {
		if cfg.SQS.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.SQS.Endpoint)
		}
	})
	return client, nil
}

func provideSQSConsumer(
	sqsClient *awssqs.Client,
	processWagerUC *usecase.ProcessWagerUseCase,
	cfg *config.Config,
) *workersqs.Consumer {
	return workersqs.NewConsumer(sqsClient, processWagerUC, workersqs.ConsumerConfig{
		QueueURL:          cfg.SQS.QueueURL,
		ConsumerName:      "desafio-sqs-consumer",
		MaxMessages:       cfg.SQS.MaxMessages,
		VisibilityTimeout: cfg.SQS.VisibilityTimeout,
		WaitTimeSeconds:   cfg.SQS.WaitTimeSeconds,
	})
}

func provideOutboxWorker(
	sqsClient *awssqs.Client,
	uow port.UnitOfWork,
	outboxRepo port.OutboxRepository,
	cfg *config.Config,
) *workeroutbox.Worker {
	return workeroutbox.NewWorker(sqsClient, uow, outboxRepo, workeroutbox.WorkerConfig{
		EventsQueueURL: cfg.SQS.EventsQueueURL,
		PollInterval:   cfg.Outbox.PollInterval,
		BatchSize:      cfg.Outbox.BatchSize,
		MaxAttempts:    cfg.Outbox.MaxAttempts,
	})
}

// Gerenciamento Completo do Ciclo de Vida (fx.Lifecycle)
func registerLifecycleHooks(
	lc fx.Lifecycle,
	router *chi.Mux,
	sqsConsumer *workersqs.Consumer,
	outboxWorker *workeroutbox.Worker,
	pendingRefWorker *workerpendingref.PendingReferenceWorker,
	cfg *config.Config,
) {
	httpServer := &http.Server{
		Addr:    ":" + cfg.HTTP.Port,
		Handler: router,
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			slog.Info("starting application components",
				"http_port", cfg.HTTP.Port,
				"sqs_queue", cfg.SQS.QueueURL,
				"events_queue", cfg.SQS.EventsQueueURL,
			)

			// 1. Inicia workers em background
			sqsConsumer.Start(context.Background())
			outboxWorker.Start(context.Background())
			pendingRefWorker.Start(context.Background())

			// 2. Inicia servidor HTTP
			go func() {
				slog.Info("http server listening", "addr", httpServer.Addr)
				if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					slog.Error("http server failed", "error", err)
				}
			}()

			return nil
		},
		OnStop: func(ctx context.Context) error {
			slog.Info("initiating graceful shutdown", "timeout", cfg.Shutdown.Timeout)

			shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Shutdown.Timeout)
			defer cancel()

			// 1. Interrompe servidor HTTP e para de aceitar conexões
			slog.Info("stopping http server")
			if err := httpServer.Shutdown(shutdownCtx); err != nil {
				slog.Warn("http server shutdown error", "error", err)
			}

			// 2. Finaliza workers em background em paralelo com drain
			workers := []struct {
				name string
				stop func(context.Context) error
			}{
				{"sqs consumer", sqsConsumer.Stop},
				{"outbox worker", outboxWorker.Stop},
				{"pending references worker", pendingRefWorker.Stop},
			}

			var wg sync.WaitGroup
			wg.Add(len(workers))
			for _, w := range workers {
				go func(name string, stopFn func(context.Context) error) {
					defer wg.Done()
					slog.Info("stopping " + name)
					if err := stopFn(shutdownCtx); err != nil {
						slog.Warn(name+" stop error", "error", err)
					}
				}(w.name, w.stop)
			}
			wg.Wait()

			slog.Info("application shutdown complete")
			return nil
		},
	})
}
