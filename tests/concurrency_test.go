//go:build integration

package tests_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
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
	apphttp "github.com/felipecristiano/desafio/internal/http"
	"github.com/felipecristiano/desafio/internal/http/handler"
	"github.com/felipecristiano/desafio/internal/infra/auth"
	"github.com/felipecristiano/desafio/internal/infra/db/postgres"
	workeroutbox "github.com/felipecristiano/desafio/internal/worker/outbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type dummyTokenValidator struct{}

func (d *dummyTokenValidator) ValidateToken(ctx context.Context, token string) (*auth.Claims, error) {
	c := &auth.Claims{ClientID: "provider-a"}
	c.RealmAccess.Roles = []string{"internal", "provider"}
	return c, nil
}

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

	return awssqs.NewFromConfig(cfg, func(o *awssqs.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})
}

func setupDependencies(pool *pgxpool.Pool) (
	port.UnitOfWork,
	port.WalletRepository,
	port.WagerTransactionRepository,
	port.LedgerRepository,
	port.InboxRepository,
	port.OutboxRepository,
	*usecase.OpenWalletUseCase,
	*usecase.ProcessWagerUseCase,
	*usecase.ReconcileWalletUseCase,
	*usecase.RetryPendingReferencesUseCase,
) {
	uow := postgres.NewUnitOfWork(pool)
	walletRepo := postgres.NewWalletRepository()
	txnRepo := postgres.NewWagerTransactionRepository()
	ledgerRepo := postgres.NewLedgerRepository()
	inboxRepo := postgres.NewInboxRepository()
	outboxRepo := postgres.NewOutboxRepository()

	openUC := usecase.NewOpenWalletUseCase(uow, walletRepo, txnRepo, ledgerRepo, outboxRepo)
	processUC := usecase.NewProcessWagerUseCase(uow, walletRepo, txnRepo, ledgerRepo, inboxRepo, outboxRepo)
	reconcileUC := usecase.NewReconcileWalletUseCase(uow, walletRepo, ledgerRepo)
	retryUC := usecase.NewRetryPendingReferencesUseCase(uow, walletRepo, txnRepo, ledgerRepo, outboxRepo, 5)

	return uow, walletRepo, txnRepo, ledgerRepo, inboxRepo, outboxRepo, openUC, processUC, reconcileUC, retryUC
}

// -------------------------------------------------------------------------------------------------
// 1. Cenário: 50 apostas simultâneas da MESMA operação com único débito (Seção 13.1)
// -------------------------------------------------------------------------------------------------
func TestConcurrency_50SameBet_SingleDebit(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	_, _, _, _, _, _, openUC, processUC, reconcileUC, _ := setupDependencies(pool)

	playerID := uuid.New()
	initBal := money.NewFromInt64(100000, money.BRL) // 1000.00 BRL
	openOut, err := openUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initBal,
		CorrelationID:  uuid.New(),
	})
	if err != nil {
		t.Fatalf("failed to open wallet: %v", err)
	}

	walletID := openOut.ID
	betAmount := money.NewFromInt64(2500, money.BRL) // 25.00 BRL
	extTxID := fmt.Sprintf("tx-parallel-50-%s", uuid.New().String()[:8])
	idempotencyKey := fmt.Sprintf("provider-a:%s", extTxID)

	const concurrencyCount = 50
	var wg sync.WaitGroup
	wg.Add(concurrencyCount)

	type callResult struct {
		output *usecase.ProcessWagerOutput
		err    error
	}
	results := make([]callResult, concurrencyCount)

	for i := 0; i < concurrencyCount; i++ {
		go func(idx int) {
			defer wg.Done()
			corrID := uuid.New()
			out, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
				Source:                "HTTP",
				IdempotencyKey:        idempotencyKey,
				CorrelationID:         corrID,
				ProviderID:            "provider-a",
				ExternalTransactionID: extTxID,
				PlayerID:              playerID,
				WalletID:              walletID,
				RoundID:               "round-conc-50",
				GameID:                "fortune-chimp",
				Kind:                  transaction.KindBet,
				Money:                 betAmount,
			})
			results[idx] = callResult{output: out, err: err}
		}(i)
	}

	wg.Wait()

	var successCount, replayCount int
	for _, res := range results {
		if res.err != nil {
			t.Fatalf("unexpected error in concurrent bet: %v", res.err)
		}
		if res.output.Status == transaction.StatusProcessed {
			successCount++
		}
		if res.output.IdempotentReplay {
			replayCount++
		}
		// Todo retorno deve informar exatamente o saldo pós-débito de 975.00 BRL
		if res.output.Balance.Amount() != 97500 {
			t.Errorf("expected balance 975.00 (97500 cents), got %s", res.output.Balance)
		}
	}

	if successCount != concurrencyCount {
		t.Fatalf("expected all %d calls to succeed, got %d", concurrencyCount, successCount)
	}
	if replayCount != concurrencyCount-1 {
		t.Fatalf("expected %d idempotent replays, got %d", concurrencyCount-1, replayCount)
	}

	// Reconciliação: Saldo armazenado deve bater perfeitamente com a soma do ledger
	recOut, err := reconcileUC.Execute(ctx, usecase.ReconcileWalletInput{WalletID: walletID})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if !recOut.Consistent {
		t.Fatalf("wallet balance inconsistent with ledger: diff = %s", recOut.Difference)
	}
	if recOut.StoredBalance.Amount() != 97500 {
		t.Fatalf("expected final stored balance 975.00 BRL, got %s", recOut.StoredBalance)
	}
}

// -------------------------------------------------------------------------------------------------
// 2. Cenário: Disputa de 2 apostas de 80.00 sobre saldo de 100.00 (Seção 13.2)
// -------------------------------------------------------------------------------------------------
func TestConcurrency_Two80BetsOn100Balance(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	_, _, _, _, _, _, openUC, processUC, reconcileUC, _ := setupDependencies(pool)

	playerID := uuid.New()
	initBal := money.NewFromInt64(10000, money.BRL) // 100.00 BRL
	openOut, err := openUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initBal,
		CorrelationID:  uuid.New(),
	})
	if err != nil {
		t.Fatalf("failed to open wallet: %v", err)
	}
	walletID := openOut.ID

	betAmount := money.NewFromInt64(8000, money.BRL) // 80.00 BRL
	runID := uuid.New().String()[:8]
	extTxID1 := fmt.Sprintf("dispute-bet-1-%s", runID)
	extTxID2 := fmt.Sprintf("dispute-bet-2-%s", runID)

	var wg sync.WaitGroup
	wg.Add(2)

	outA := make(chan *usecase.ProcessWagerOutput, 1)
	outB := make(chan *usecase.ProcessWagerOutput, 1)

	go func() {
		defer wg.Done()
		out, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
			Source:                "HTTP",
			IdempotencyKey:        fmt.Sprintf("provider-a:%s", extTxID1),
			CorrelationID:         uuid.New(),
			ProviderID:            "provider-a",
			ExternalTransactionID: extTxID1,
			PlayerID:              playerID,
			WalletID:              walletID,
			RoundID:               "round-dispute-1",
			GameID:                "fortune-tiger",
			Kind:                  transaction.KindBet,
			Money:                 betAmount,
		})
		if err != nil {
			t.Errorf("bet 1 error: %v", err)
		}
		outA <- out
	}()

	go func() {
		defer wg.Done()
		out, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
			Source:                "HTTP",
			IdempotencyKey:        fmt.Sprintf("provider-a:%s", extTxID2),
			CorrelationID:         uuid.New(),
			ProviderID:            "provider-a",
			ExternalTransactionID: extTxID2,
			PlayerID:              playerID,
			WalletID:              walletID,
			RoundID:               "round-dispute-2",
			GameID:                "fortune-tiger",
			Kind:                  transaction.KindBet,
			Money:                 betAmount,
		})
		if err != nil {
			t.Errorf("bet 2 error: %v", err)
		}
		outB <- out
	}()

	wg.Wait()
	resA := <-outA
	resB := <-outB

	var processedCount, rejectedCount int
	if resA.Status == transaction.StatusProcessed {
		processedCount++
	} else if resA.Status == transaction.StatusRejected {
		rejectedCount++
	}

	if resB.Status == transaction.StatusProcessed {
		processedCount++
	} else if resB.Status == transaction.StatusRejected {
		rejectedCount++
	}

	if processedCount != 1 || rejectedCount != 1 {
		t.Fatalf("expected exactly 1 PROCESSED and 1 REJECTED, got %d processed and %d rejected",
			processedCount, rejectedCount)
	}

	// O saldo final DEVE ser exatamente 20.00 BRL
	recOut, err := reconcileUC.Execute(ctx, usecase.ReconcileWalletInput{WalletID: walletID})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if !recOut.Consistent {
		t.Fatalf("wallet balance inconsistent with ledger: diff = %s", recOut.Difference)
	}
	if recOut.StoredBalance.Amount() != 2000 {
		t.Fatalf("expected exact remaining balance 20.00 BRL (2000 cents), got %s", recOut.StoredBalance)
	}
}

// -------------------------------------------------------------------------------------------------
// 3. Cenário: Carteiras distintas processando simultaneamente em alto paralelismo (Seção 13.3)
// -------------------------------------------------------------------------------------------------
func TestConcurrency_DistinctWallets_HighParallelism(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	_, _, _, _, _, _, openUC, processUC, reconcileUC, _ := setupDependencies(pool)

	const numWallets = 10
	type walletMeta struct {
		id       uuid.UUID
		playerID uuid.UUID
	}
	wallets := make([]walletMeta, numWallets)

	initBal := money.NewFromInt64(10000, money.BRL) // 100.00 BRL
	for i := 0; i < numWallets; i++ {
		pID := uuid.New()
		out, err := openUC.Execute(ctx, usecase.OpenWalletInput{
			PlayerID:       pID,
			InitialBalance: initBal,
			CorrelationID:  uuid.New(),
		})
		if err != nil {
			t.Fatalf("failed to open wallet %d: %v", i, err)
		}
		wallets[i] = walletMeta{id: out.ID, playerID: pID}
	}

	// Executa apostas concorrentes em todas as 10 carteiras simultaneamente
	var wg sync.WaitGroup
	wg.Add(numWallets)
	debitAmount := money.NewFromInt64(1500, money.BRL) // 15.00 BRL

	for i := 0; i < numWallets; i++ {
		w := wallets[i]
		go func(idx int, wm walletMeta) {
			defer wg.Done()
			extTxID := fmt.Sprintf("tx-parallel-wallet-%d-%s", idx, uuid.New().String()[:8])
			out, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
				Source:                "HTTP",
				IdempotencyKey:        fmt.Sprintf("provider-a:%s", extTxID),
				CorrelationID:         uuid.New(),
				ProviderID:            "provider-a",
				ExternalTransactionID: extTxID,
				PlayerID:              wm.playerID,
				WalletID:              wm.id,
				RoundID:               fmt.Sprintf("round-p-%d", idx),
				GameID:                "fortune-gems",
				Kind:                  transaction.KindBet,
				Money:                 debitAmount,
			})
			if err != nil {
				t.Errorf("wallet %d bet error: %v", idx, err)
			}
			if out.Status != transaction.StatusProcessed || out.Balance.Amount() != 8500 {
				t.Errorf("wallet %d unexpected balance: %s", idx, out.Balance)
			}
		}(i, w)
	}

	wg.Wait()

	// Valida reconciliação em todas as 10 carteiras
	for i, w := range wallets {
		rec, err := reconcileUC.Execute(ctx, usecase.ReconcileWalletInput{WalletID: w.id})
		if err != nil || !rec.Consistent || rec.StoredBalance.Amount() != 8500 {
			t.Errorf("wallet %d failed reconciliation: consistent=%v, balance=%s, err=%v",
				i, rec.Consistent, rec.StoredBalance, err)
		}
	}
}

// -------------------------------------------------------------------------------------------------
// 4. Cenário: Múltiplas instâncias independentes compartilhando o banco (Seção 13.4)
// -------------------------------------------------------------------------------------------------
func TestConcurrency_MultiInstance_Distributed(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	uow, walletRepo, txnRepo, ledgerRepo, _, _, openUC, processUC, reconcileUC, _ := setupDependencies(pool)

	val := &dummyTokenValidator{}
	healthH := handler.NewHealthHandler(pool)
	walletH := handler.NewWalletHandler(openUC, reconcileUC, walletRepo, ledgerRepo, uow)
	wagerH := handler.NewWagerHandler(processUC, txnRepo, uow)

	// Cria 3 instâncias de servidor HTTP apontando para o mesmo banco de dados
	server1 := httptest.NewServer(apphttp.NewRouter(val, healthH, walletH, wagerH))
	defer server1.Close()
	server2 := httptest.NewServer(apphttp.NewRouter(val, healthH, walletH, wagerH))
	defer server2.Close()
	server3 := httptest.NewServer(apphttp.NewRouter(val, healthH, walletH, wagerH))
	defer server3.Close()

	servers := []string{server1.URL, server2.URL, server3.URL}

	// Cria uma carteira comum com 300.00 BRL
	playerID := uuid.New()
	initBal := money.NewFromInt64(30000, money.BRL)
	openOut, err := openUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initBal,
		CorrelationID:  uuid.New(),
	})
	if err != nil {
		t.Fatalf("failed to open wallet: %v", err)
	}
	walletID := openOut.ID

	// Distribui 30 apostas de 10.00 BRL de forma concorrente entre os 3 servidores independentes
	const totalBets = 30
	var wg sync.WaitGroup
	wg.Add(totalBets)

	for i := 0; i < totalBets; i++ {
		srvURL := servers[i%len(servers)]
		go func(idx int, targetURL string) {
			defer wg.Done()
			extTxID := fmt.Sprintf("tx-multi-inst-%d-%s", idx, uuid.New().String()[:8])
			reqBody, _ := json.Marshal(map[string]any{
				"providerId":            "provider-a",
				"externalTransactionId": extTxID,
				"playerId":              playerID.String(),
				"walletId":              walletID.String(),
				"roundId":               fmt.Sprintf("round-inst-%d", idx),
				"gameId":                "game-inst",
				"kind":                  "BET",
				"money": map[string]string{
					"amount":   "10.00",
					"currency": "BRL",
				},
			})

			req, _ := http.NewRequest(http.MethodPost, targetURL+"/wagering/transactions", bytes.NewReader(reqBody))
			req.Header.Set("Authorization", "Bearer valid-token")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", fmt.Sprintf("provider-a:%s", extTxID))

			resp, err := http.DefaultClient.Do(req)
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Errorf("bet %d on %s failed: status=%v, err=%v", idx, targetURL, resp.StatusCode, err)
			}
			_ = resp.Body.Close()
		}(i, srvURL)
	}

	wg.Wait()

	// Validação final de consistência e saldo (300.00 - 30 * 10.00 = 0.00 BRL)
	rec, err := reconcileUC.Execute(ctx, usecase.ReconcileWalletInput{WalletID: walletID})
	if err != nil || !rec.Consistent || rec.StoredBalance.Amount() != 0 {
		t.Fatalf("multi-instance reconciliation failed: consistent=%v, balance=%s", rec.Consistent, rec.StoredBalance)
	}
}

// -------------------------------------------------------------------------------------------------
// 5. Cenário de Chaos: Interrupção pós-commit antes de remover SQS e validação de reentrega (Seção 13.5)
// -------------------------------------------------------------------------------------------------
func TestChaos_SQSConsumer_RedeliveryDeduplication(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	_, walletRepo, _, ledgerRepo, _, _, openUC, processUC, _, _ := setupDependencies(pool)

	playerID := uuid.New()
	initBal := money.NewFromInt64(10000, money.BRL) // 100.00 BRL
	openOut, err := openUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initBal,
		CorrelationID:  uuid.New(),
	})
	if err != nil {
		t.Fatalf("failed to open wallet: %v", err)
	}
	walletID := openOut.ID

	msgID := fmt.Sprintf("msg-chaos-%s", uuid.New().String()[:8])
	extTxID := fmt.Sprintf("tx-chaos-%s", uuid.New().String()[:8])
	idempotencyKey := fmt.Sprintf("provider-a:%s", extTxID)

	// Simula a 1ª entrega da mensagem SQS: o caso de uso executa com sucesso e commita no banco
	out1, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
		Source:                "SQS",
		ConsumerName:          "chaos-worker-1",
		MessageID:             msgID,
		IdempotencyKey:        idempotencyKey,
		CorrelationID:         uuid.New(),
		ProviderID:            "provider-a",
		ExternalTransactionID: extTxID,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-chaos-1",
		GameID:                "fortune-crash",
		Kind:                  transaction.KindBet,
		Money:                 money.NewFromInt64(2000, money.BRL), // 20.00 BRL
	})
	if err != nil {
		t.Fatalf("first delivery failed: %v", err)
	}
	if out1.Status != transaction.StatusProcessed || out1.IdempotentReplay {
		t.Fatalf("expected first execution to be fresh processed, got replay=%v", out1.IdempotentReplay)
	}

	// Simula falha do processo ANTES de chamar DeleteMessage no SQS broker.
	// O SQS redespacha a mesma mensagem com o mesmo messageId para um segundo worker diferente ("chaos-worker-2").
	out2, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
		Source:                "SQS",
		ConsumerName:          "chaos-worker-2",
		MessageID:             msgID,
		IdempotencyKey:        idempotencyKey,
		CorrelationID:         uuid.New(),
		ProviderID:            "provider-a",
		ExternalTransactionID: extTxID,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-chaos-1",
		GameID:                "fortune-crash",
		Kind:                  transaction.KindBet,
		Money:                 money.NewFromInt64(2000, money.BRL),
	})
	if err != nil {
		t.Fatalf("redelivered execution failed: %v", err)
	}

	// A segunda execução deve ser identificada como replay idempotente
	if !out2.IdempotentReplay {
		t.Fatalf("expected redelivered message to be flagged as idempotentReplay")
	}

	// O saldo final deve ser exatamente 80.00 BRL (apenas 1 débito de 20.00)
	w, _ := walletRepo.GetByID(ctx, pool, walletID)
	if w.Balance().Amount() != 8000 {
		t.Fatalf("balance was double debited! expected 80.00 BRL, got %s", w.Balance())
	}

	// O ledger deve conter estritamente 1 débito
	entries, _ := ledgerRepo.GetByWalletID(ctx, pool, walletID, nil, 10)
	// 1 abertura (credit 100.00) + 1 aposta (debit 20.00) = 2 lançamentos no total
	if len(entries) != 2 {
		t.Fatalf("expected exactly 2 ledger entries, got %d", len(entries))
	}
}

// -------------------------------------------------------------------------------------------------
// 6. Cenário: Dois publishers concorrentes disputando a mesma outbox com SKIP LOCKED (Seção 13.6)
// -------------------------------------------------------------------------------------------------
func TestConcurrency_DualOutboxPublishers(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	uow, _, _, _, _, outboxRepo, openUC, processUC, _, _ := setupDependencies(pool)

	// Limpa outbox_events para isolar a contagem
	_, _ = pool.Exec(ctx, "DELETE FROM outbox_events")

	// Gera eventos na outbox criando 5 transações com mutação financeira
	playerID := uuid.New()
	initBal := money.NewFromInt64(50000, money.BRL)
	openOut, err := openUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initBal,
		CorrelationID:  uuid.New(),
	})
	if err != nil {
		t.Fatalf("open wallet failed: %v", err)
	}

	for i := 0; i < 5; i++ {
		extTxID := fmt.Sprintf("tx-outbox-dual-%d-%s", i, uuid.New().String()[:6])
		_, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
			Source:                "HTTP",
			IdempotencyKey:        fmt.Sprintf("provider-a:%s", extTxID),
			CorrelationID:         uuid.New(),
			ProviderID:            "provider-a",
			ExternalTransactionID: extTxID,
			PlayerID:              playerID,
			WalletID:              openOut.ID,
			RoundID:               fmt.Sprintf("round-dual-%d", i),
			GameID:                "fortune-dual",
			Kind:                  transaction.KindBet,
			Money:                 money.NewFromInt64(1000, money.BRL),
		})
		if err != nil {
			t.Fatalf("process wager failed: %v", err)
		}
	}

	var totalPending int
	_ = pool.QueryRow(ctx, "SELECT COUNT(*) FROM outbox_events WHERE published_at IS NULL").Scan(&totalPending)
	if totalPending == 0 {
		t.Fatalf("expected pending outbox events, got 0")
	}

	// Cria 2 workers de Outbox concorrentes
	sqsClient := getSQSClient(t)
	eventsQueueURL := os.Getenv("SQS_EVENTS_QUEUE_URL")
	if eventsQueueURL == "" {
		eventsQueueURL = "http://localhost:4566/000000000000/wager-events.fifo"
	}

	worker1 := workeroutbox.NewWorker(sqsClient, uow, outboxRepo, workeroutbox.WorkerConfig{
		EventsQueueURL: eventsQueueURL,
		BatchSize:      10,
		MaxAttempts:    3,
	})
	worker2 := workeroutbox.NewWorker(sqsClient, uow, outboxRepo, workeroutbox.WorkerConfig{
		EventsQueueURL: eventsQueueURL,
		BatchSize:      10,
		MaxAttempts:    3,
	})

	var published1, published2 int32
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		p := worker1.ProcessBatch(ctx)
		atomic.AddInt32(&published1, int32(p))
	}()

	go func() {
		defer wg.Done()
		p := worker2.ProcessBatch(ctx)
		atomic.AddInt32(&published2, int32(p))
	}()

	wg.Wait()

	totalPublished := int(published1 + published2)
	if totalPublished != totalPending {
		t.Fatalf("expected total published %d, got %d (w1: %d, w2: %d)",
			totalPending, totalPublished, published1, published2)
	}

	// Confirma que não restou nenhum evento pendente
	var remainingPending int
	_ = pool.QueryRow(ctx, "SELECT COUNT(*) FROM outbox_events WHERE published_at IS NULL").Scan(&remainingPending)
	if remainingPending != 0 {
		t.Fatalf("expected 0 remaining pending events, got %d", remainingPending)
	}
}

// -------------------------------------------------------------------------------------------------
// 7. Cenário: REFUND e ROLLBACK entregues antes da referência e resolução/expiração (Seção 13.7)
// -------------------------------------------------------------------------------------------------
func TestChaos_PendingReference_ResolutionAndExpiration(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	_, walletRepo, txnRepo, _, _, _, openUC, processUC, _, retryUC := setupDependencies(pool)

	playerID := uuid.New()
	initBal := money.NewFromInt64(10000, money.BRL) // 100.00 BRL
	openOut, err := openUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initBal,
		CorrelationID:  uuid.New(),
	})
	if err != nil {
		t.Fatalf("open wallet failed: %v", err)
	}
	walletID := openOut.ID

	refBetExtID := fmt.Sprintf("tx-ref-original-%s", uuid.New().String()[:8])
	refundExtID := fmt.Sprintf("tx-refund-ahead-%s", uuid.New().String()[:8])

	// Submete o REFUND antes da existência da aposta original
	refundOut, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
		Source:                         "HTTP",
		IdempotencyKey:                 fmt.Sprintf("provider-a:%s", refundExtID),
		CorrelationID:                  uuid.New(),
		ProviderID:                     "provider-a",
		ExternalTransactionID:          refundExtID,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        "round-out-of-order-1",
		GameID:                         "fortune-panda",
		Kind:                           transaction.KindRefund,
		Money:                          money.NewFromInt64(2500, money.BRL), // 25.00 BRL
		ReferenceExternalTransactionID: &refBetExtID,
	})
	if err != nil {
		t.Fatalf("refund submission failed: %v", err)
	}
	if refundOut.Status != transaction.StatusPendingReference {
		t.Fatalf("expected PENDING_REFERENCE status, got %s", refundOut.Status)
	}

	// Executa uma rodada do retry worker: como a aposta ainda não existe, não deve resolver
	resolved, err := retryUC.ExecuteBatch(ctx, 10)
	if err != nil || resolved != 0 {
		t.Fatalf("expected 0 resolved references, got %d, err=%v", resolved, err)
	}

	// Agora a aposta original (BET 25.00 BRL) é finalmente entregue e processada
	betOut, err := processUC.Execute(ctx, usecase.ProcessWagerInput{
		Source:                "HTTP",
		IdempotencyKey:        fmt.Sprintf("provider-a:%s", refBetExtID),
		CorrelationID:         uuid.New(),
		ProviderID:            "provider-a",
		ExternalTransactionID: refBetExtID,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-out-of-order-1",
		GameID:                "fortune-panda",
		Kind:                  transaction.KindBet,
		Money:                 money.NewFromInt64(2500, money.BRL),
	})
	if err != nil || betOut.Status != transaction.StatusProcessed {
		t.Fatalf("original bet processing failed: status=%s, err=%v", betOut.Status, err)
	}

	// Força retry_after para o passado para o worker pegar imediatamente
	_, _ = pool.Exec(ctx, "UPDATE wager_transactions SET retry_after = NOW() - INTERVAL '1 second' WHERE status = 'PENDING_REFERENCE'")

	// Executa o worker de referências pendentes novamente: agora deve resolver e aplicar crédito
	resolved, err = retryUC.ExecuteBatch(ctx, 10)
	if err != nil || resolved != 1 {
		t.Fatalf("expected exactly 1 resolved reference, got %d, err=%v", resolved, err)
	}

	// Valida se o status da transação REFUND agora é PROCESSED
	refundTxn, _ := txnRepo.GetByProviderAndExternalID(ctx, pool, "provider-a", refundExtID)
	if refundTxn.Status() != transaction.StatusProcessed {
		t.Fatalf("expected refund status PROCESSED, got %s", refundTxn.Status())
	}

	// Saldo final deve ter sido restaurado para 100.00 BRL (100.00 - 25.00 + 25.00)
	w, _ := walletRepo.GetByID(ctx, pool, walletID)
	if w.Balance().Amount() != 10000 {
		t.Fatalf("expected balance 100.00 BRL, got %s", w.Balance())
	}

	// Subcenário B: Reversão órfã que nunca recebe a referência até estourar maxRetries
	orphanExtID := fmt.Sprintf("tx-orphan-%s", uuid.New().String()[:8])
	nonExistentRefID := "tx-ghost-reference"
	_, err = processUC.Execute(ctx, usecase.ProcessWagerInput{
		Source:                         "HTTP",
		IdempotencyKey:                 fmt.Sprintf("provider-a:%s", orphanExtID),
		CorrelationID:                  uuid.New(),
		ProviderID:                     "provider-a",
		ExternalTransactionID:          orphanExtID,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        "round-orphan",
		GameID:                         "fortune-ghost",
		Kind:                           transaction.KindRollback,
		Money:                          money.NewFromInt64(1000, money.BRL),
		ReferenceExternalTransactionID: &nonExistentRefID,
	})
	if err != nil {
		t.Fatalf("orphan rollback submission failed: %v", err)
	}

	// Executa retries sucessivos até atingir o limite
	for i := 0; i < 6; i++ {
		_, _ = pool.Exec(ctx, "UPDATE wager_transactions SET retry_after = NOW() - INTERVAL '1 second' WHERE status = 'PENDING_REFERENCE'")
		_, _ = retryUC.ExecuteBatch(ctx, 10)
	}

	orphanTxn, _ := txnRepo.GetByProviderAndExternalID(ctx, pool, "provider-a", orphanExtID)
	if orphanTxn.Status() != transaction.StatusRejected {
		t.Fatalf("expected orphan transaction to be REJECTED after max retries, got %s", orphanTxn.Status())
	}
	if orphanTxn.FailureCode() == nil || *orphanTxn.FailureCode() != "REFERENCE_NOT_FOUND" {
		t.Fatalf("expected failureCode REFERENCE_NOT_FOUND, got %v", orphanTxn.FailureCode())
	}
}

// -------------------------------------------------------------------------------------------------
// 8. Cenário: Cruzamento entre HTTP e SQS para a mesma operação financeira (Seção 13.8)
// -------------------------------------------------------------------------------------------------
func TestConcurrency_CrossHTTPAndSQS_Deduplication(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	_, walletRepo, _, ledgerRepo, _, _, openUC, processUC, reconcileUC, _ := setupDependencies(pool)

	playerID := uuid.New()
	initBal := money.NewFromInt64(50000, money.BRL) // 500.00 BRL
	openOut, err := openUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initBal,
		CorrelationID:  uuid.New(),
	})
	if err != nil {
		t.Fatalf("open wallet failed: %v", err)
	}
	walletID := openOut.ID

	extTxID := fmt.Sprintf("tx-cross-http-sqs-%s", uuid.New().String()[:8])
	idempotencyKey := fmt.Sprintf("provider-a:%s", extTxID)
	msgID := fmt.Sprintf("msg-cross-sqs-%s", uuid.New().String()[:8])
	amount := money.NewFromInt64(5000, money.BRL) // 50.00 BRL

	var wg sync.WaitGroup
	wg.Add(2)

	var httpOut, sqsOut *usecase.ProcessWagerOutput
	var httpErr, sqsErr error

	// Chamada simultânea via HTTP
	go func() {
		defer wg.Done()
		httpOut, httpErr = processUC.Execute(ctx, usecase.ProcessWagerInput{
			Source:                "HTTP",
			IdempotencyKey:        idempotencyKey,
			CorrelationID:         uuid.New(),
			ProviderID:            "provider-a",
			ExternalTransactionID: extTxID,
			PlayerID:              playerID,
			WalletID:              walletID,
			RoundID:               "round-cross-1",
			GameID:                "fortune-cross",
			Kind:                  transaction.KindBet,
			Money:                 amount,
		})
	}()

	// Chamada simultânea via SQS
	go func() {
		defer wg.Done()
		sqsOut, sqsErr = processUC.Execute(ctx, usecase.ProcessWagerInput{
			Source:                "SQS",
			ConsumerName:          "cross-consumer",
			MessageID:             msgID,
			IdempotencyKey:        idempotencyKey,
			CorrelationID:         uuid.New(),
			ProviderID:            "provider-a",
			ExternalTransactionID: extTxID,
			PlayerID:              playerID,
			WalletID:              walletID,
			RoundID:               "round-cross-1",
			GameID:                "fortune-cross",
			Kind:                  transaction.KindBet,
			Money:                 amount,
		})
	}()

	wg.Wait()

	if httpErr != nil {
		t.Fatalf("http execution error: %v", httpErr)
	}
	if sqsErr != nil {
		t.Fatalf("sqs execution error: %v", sqsErr)
	}

	// Um dos dois foi o executor original e o outro foi replay idempotente
	if httpOut.IdempotentReplay == sqsOut.IdempotentReplay {
		t.Fatalf("expected one execution to be original and the other replay, got httpReplay=%v, sqsReplay=%v",
			httpOut.IdempotentReplay, sqsOut.IdempotentReplay)
	}

	// Ambos devem reportar o saldo de 450.00 BRL
	if httpOut.Balance.Amount() != 45000 || sqsOut.Balance.Amount() != 45000 {
		t.Fatalf("balance mismatch: http=%s, sqs=%s", httpOut.Balance, sqsOut.Balance)
	}

	// Reconciliação deve acusar consistência absoluta
	rec, err := reconcileUC.Execute(ctx, usecase.ReconcileWalletInput{WalletID: walletID})
	if err != nil || !rec.Consistent || rec.StoredBalance.Amount() != 45000 {
		t.Fatalf("cross HTTP/SQS reconciliation failed: consistent=%v, balance=%s", rec.Consistent, rec.StoredBalance)
	}

	_ = walletRepo
	_ = ledgerRepo
}
