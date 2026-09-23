//go:build integration

package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	apphttp "github.com/felipecristiano/desafio/internal/http"
	"github.com/felipecristiano/desafio/internal/http/handler"
	"github.com/felipecristiano/desafio/internal/infra/auth"
	"github.com/felipecristiano/desafio/internal/infra/db/postgres"
	"github.com/felipecristiano/desafio/internal/application/usecase"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type mockTokenValidator struct {
	clientID string
	roles    []string
	err      error
}

func (m *mockTokenValidator) ValidateToken(ctx context.Context, tokenString string) (*auth.Claims, error) {
	if m.err != nil {
		return nil, m.err
	}
	claims := &auth.Claims{
		ClientID: m.clientID,
	}
	claims.RealmAccess.Roles = m.roles
	return claims, nil
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

func TestHTTPAPI_EndToEnd(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

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
	reconcileUC := usecase.NewReconcileWalletUseCase(uow, walletRepo, ledgerRepo)

	healthH := handler.NewHealthHandler(pool)
	walletH := handler.NewWalletHandler(openWalletUC, reconcileUC, walletRepo, ledgerRepo, uow)
	wagerH := handler.NewWagerHandler(processWagerUC, txnRepo, uow)

	validator := &mockTokenValidator{
		clientID: "provider-a",
		roles:    []string{"internal", "provider"},
	}

	router := apphttp.NewRouter(validator, healthH, walletH, wagerH)
	server := httptest.NewServer(router)
	defer server.Close()

	// 1. Health Checks Públicos
	resp, err := http.Get(server.URL + "/health/live")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("health live failed: status=%d, err=%v", resp.StatusCode, err)
	}

	resp, err = http.Get(server.URL + "/health/ready")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("health ready failed: status=%d, err=%v", resp.StatusCode, err)
	}

	// 2. Abertura de Carteira: POST /wallets
	playerID := uuid.New()
	openReqBody := map[string]any{
		"playerId": playerID.String(),
		"initialBalance": map[string]string{
			"amount":   "100.00",
			"currency": "BRL",
		},
	}
	bodyBytes, _ := json.Marshal(openReqBody)

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/wallets", bytes.NewReader(bodyBytes))
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open wallet request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", resp.StatusCode)
	}

	var walletResp map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&walletResp)
	walletID := walletResp["id"].(string)

	// 3. Envio de Operação: POST /wagering/transactions (BET 25.00)
	extTxnID := "tx-http-001"
	betReqBody := map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": extTxnID,
		"playerId":              playerID.String(),
		"walletId":              walletID,
		"roundId":               "round-http-1",
		"gameId":                "game-1",
		"kind":                  "BET",
		"money": map[string]string{
			"amount":   "25.00",
			"currency": "BRL",
		},
	}
	bodyBytes, _ = json.Marshal(betReqBody)

	req, _ = http.NewRequest(http.MethodPost, server.URL+"/wagering/transactions", bytes.NewReader(bodyBytes))
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "provider-a:"+extTxnID)

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("process wager request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var wagerResp map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&wagerResp)
	if wagerResp["status"] != "PROCESSED" {
		t.Fatalf("expected PROCESSED, got %v", wagerResp["status"])
	}
	balMap := wagerResp["balance"].(map[string]any)
	if balMap["amount"] != "75.00" {
		t.Fatalf("expected balance 75.00, got %v", balMap["amount"])
	}

	// 4. Replay Idempotente com header Idempotency-Key idêntico
	req, _ = http.NewRequest(http.MethodPost, server.URL+"/wagering/transactions", bytes.NewReader(bodyBytes))
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "provider-a:"+extTxnID)

	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("replay failed: status=%d, err=%v", resp.StatusCode, err)
	}
	var replayResp map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&replayResp)
	if replayResp["idempotentReplay"] != true {
		t.Fatalf("expected idempotentReplay=true")
	}

	// 5. Consulta por Provider e ExternalTransactionID
	queryURL := fmt.Sprintf("%s/providers/provider-a/wagering/transactions/%s", server.URL, extTxnID)
	req, _ = http.NewRequest(http.MethodGet, queryURL, nil)
	req.Header.Set("Authorization", "Bearer valid-token")

	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("query transaction by provider and external id failed: status=%d, err=%v", resp.StatusCode, err)
	}

	// 6. Consulta do Ledger com Cursor: GET /wallets/:walletId/ledger
	ledgerURL := fmt.Sprintf("%s/wallets/%s/ledger?limit=10", server.URL, walletID)
	req, _ = http.NewRequest(http.MethodGet, ledgerURL, nil)
	req.Header.Set("Authorization", "Bearer valid-token")

	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("get ledger failed: status=%d, err=%v", resp.StatusCode, err)
	}
	var ledgerResp map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&ledgerResp)
	entries := ledgerResp["entries"].([]any)
	if len(entries) != 2 { // 1 OPENING crédito + 1 BET débito
		t.Fatalf("expected 2 ledger entries, got %d", len(entries))
	}

	// 7. Reconciliação: POST /wallets/:walletId/reconciliation
	recURL := fmt.Sprintf("%s/wallets/%s/reconciliation", server.URL, walletID)
	req, _ = http.NewRequest(http.MethodPost, recURL, nil)
	req.Header.Set("Authorization", "Bearer valid-token")

	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("reconcile failed: status=%d, err=%v", resp.StatusCode, err)
	}
	var recResp map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&recResp)
	if recResp["consistent"] != true {
		t.Fatalf("expected consistent=true, got %v", recResp["consistent"])
	}
}
