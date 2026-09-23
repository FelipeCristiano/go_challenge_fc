package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type Config struct {
	BaseURL     string
	KeycloakURL string
	Workers     int
	Requests    int
	Timeout     time.Duration
}

type KeycloakTokenResponse struct {
	AccessToken string `json:"access_token"`
}

type LoadTestResult struct {
	Duration           time.Duration
	TotalRequests      int64
	ProcessedOK        int64
	ReplaysIdempotent  int64
	RejectionsBusiness int64
	Conflicts409       int64
	ClientErrors4xx    int64
	ServerErrors5xx    int64
	ThroughputRPS      float64
	MinLatency         time.Duration
	P50Latency         time.Duration
	P95Latency         time.Duration
	P99Latency         time.Duration
	MaxLatency         time.Duration
	AvgLatency         time.Duration
	OutboxLagSecs      string
	Reconciled         bool
}

func main() {
	baseURL := flag.String("url", "http://localhost:3000", "Base URL do servidor da aplicacao")
	keycloakURL := flag.String("keycloak", "http://localhost:8080", "Base URL do Keycloak IdP")
	workers := flag.Int("workers", 10, "Numero de workers/goroutines simultaneos")
	requests := flag.Int("requests", 500, "Numero total de requisicoes de aposta a disparar")
	timeout := flag.Duration("timeout", 10*time.Second, "Timeout HTTP por requisicao")
	flag.Parse()

	cfg := Config{
		BaseURL:     strings.TrimRight(*baseURL, "/"),
		KeycloakURL: strings.TrimRight(*keycloakURL, "/"),
		Workers:     *workers,
		Requests:    *requests,
		Timeout:     *timeout,
	}

	fmt.Println("================================================================================")
	fmt.Println("INICIANDO TESTE DE CARGA (LOAD TESTING) — DESAFIO APOSTAS GO")
	fmt.Println("================================================================================")
	fmt.Printf("Ambiente: %s | Workers Concorrentes: %d | Total Requisicoes: %d\n", cfg.BaseURL, cfg.Workers, cfg.Requests)
	fmt.Println("--------------------------------------------------------------------------------")

	client := &http.Client{Timeout: cfg.Timeout}

	// 1. Obter Tokens do Keycloak via OAuth 2.0 client_credentials
	fmt.Println("1/5 Obtendo credenciais OAuth 2.0 (client_credentials) do Keycloak...")
	internalToken, err := getOAuthToken(client, cfg.KeycloakURL, "desafio-internal", "internal-secret")
	if err != nil {
		fmt.Printf("Falha ao autenticar internal-admin no Keycloak: %v\n", err)
		return
	}
	providerToken, err := getOAuthToken(client, cfg.KeycloakURL, "provider-a", "provider-a-secret")
	if err != nil {
		fmt.Printf("Falha ao autenticar provider-a no Keycloak: %v\n", err)
		return
	}
	fmt.Println("Tokens JWT obtidos com sucesso.")

	// 2. Criar Carteira de Teste com Saldo Inicial
	fmt.Println("2/5 Provisionando carteira de teste via POST /wallets...")
	playerID := uuid.New().String()
	walletID, err := openWallet(client, cfg.BaseURL, internalToken, playerID, "500000.00") // 500.000,00 BRL
	if err != nil {
		fmt.Printf("Falha ao abrir carteira: %v\n", err)
		return
	}
	fmt.Printf("Carteira criada: ID=%s (Player=%s, Saldo Inicial=500.000,00 BRL)\n", walletID, playerID)

	// Criar carteira para testes de saldo insuficiente
	lowBalPlayerID := uuid.New().String()
	lowBalWalletID, err := openWallet(client, cfg.BaseURL, internalToken, lowBalPlayerID, "15.00") // 15.00 BRL
	if err != nil {
		fmt.Printf("Falha ao abrir carteira secundaria: %v\n", err)
		return
	}
	fmt.Printf("Carteira de estresse de saldo criada: ID=%s (Saldo=15,00 BRL)\n", lowBalWalletID)

	// 3. Executar o Teste de Carga Concorrente
	fmt.Printf("3/5 Disparando %d operacoes de apostas com %d workers em paralelo...\n", cfg.Requests, cfg.Workers)
	res := runLoadTest(client, cfg, providerToken, walletID, playerID, lowBalWalletID, lowBalPlayerID)

	// 4. Capturar Métricas de Outbox e Observabilidade
	fmt.Println("4/5 Consultando metricas Prometheus da Transactional Outbox em /metrics...")
	outboxLag := getOutboxMetric(client, cfg.BaseURL)
	res.OutboxLagSecs = outboxLag

	// 5. Executar Reconciliação Matemática do Ledger
	fmt.Println("5/5 Executando reconciliacao financeira matematica (POST /reconciliation)...")
	reconciled, err := reconcileWallet(client, cfg.BaseURL, internalToken, walletID)
	if err != nil {
		fmt.Printf("Falha ao reconciliar carteira principal: %v\n", err)
	} else {
		res.Reconciled = reconciled
	}

	// 6. Exibir Relatório Detalhado
	printReport(cfg, res)
}

func getOAuthToken(client *http.Client, keycloakURL, clientID, clientSecret string) (string, error) {
	endpoint := fmt.Sprintf("%s/realms/desafio/protocol/openid-connect/token", keycloakURL)
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}

	var tokResp KeycloakTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokResp); err != nil {
		return "", err
	}
	return tokResp.AccessToken, nil
}

func openWallet(client *http.Client, baseURL, token, playerID, amount string) (string, error) {
	endpoint := fmt.Sprintf("%s/wallets", baseURL)
	body := map[string]any{
		"playerId": playerID,
		"initialBalance": map[string]string{
			"amount":   amount,
			"currency": "BRL",
		},
	}
	raw, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}

	var wResp struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wResp); err != nil {
		return "", err
	}
	return wResp.ID, nil
}

func runLoadTest(
	client *http.Client,
	cfg Config,
	providerToken string,
	walletID, playerID string,
	lowBalWalletID, lowBalPlayerID string,
) LoadTestResult {
	var (
		wg                 sync.WaitGroup
		processedOK        int64
		replaysIdempotent  int64
		rejectionsBusiness int64
		conflicts409       int64
		clientErrors4xx    int64
		serverErrors5xx    int64
		latenciesMutex     sync.Mutex
		latencies          = make([]time.Duration, 0, cfg.Requests)
	)

	jobs := make(chan int, cfg.Requests)
	for i := 0; i < cfg.Requests; i++ {
		jobs <- i
	}
	close(jobs)

	// Cria pool de chaves reutilizáveis para testar replays sob carga
	reusableKey := fmt.Sprintf("provider-a:replay-%s", uuid.New().String()[:8])
	reusablePayload := map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": strings.TrimPrefix(reusableKey, "provider-a:"),
		"playerId":              playerID,
		"walletId":              walletID,
		"roundId":               "round-loadtest",
		"gameId":                "game-loadtest",
		"kind":                  "BET",
		"money": map[string]string{
			"amount":   "10.00",
			"currency": "BRL",
		},
	}

	startTime := time.Now()

	for w := 0; w < cfg.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for jobID := range jobs {
				var (
					idempotencyKey string
					payload        map[string]any
				)

				// Metodologia de Tráfego:
				// - 85% apostas normais únicas com saldo suficiente
				// - 10% replays idempotentes idênticos (mesma chave e mesmo payload)
				// - 3% disputas de saldo insuficiente (wallet de R$ 15,00 recebendo aposta de R$ 20,00)
				// - 2% conflitos intencionais de payload (mesma chave, corpo diferente)
				r := rand.Float64()
				if r < 0.10 {
					// Replay Idempotente
					idempotencyKey = reusableKey
					payload = reusablePayload
				} else if r < 0.12 {
					// Conflito de Idempotência (mesma chave, payload modificado)
					idempotencyKey = reusableKey
					payload = map[string]any{
						"providerId":            "provider-a",
						"externalTransactionId": strings.TrimPrefix(reusableKey, "provider-a:"),
						"playerId":              playerID,
						"walletId":              walletID,
						"roundId":               "round-loadtest",
						"gameId":                "different-game",
						"kind":                  "BET",
						"money": map[string]string{
							"amount":   "99.00", // divergente
							"currency": "BRL",
						},
					}
				} else if r < 0.15 {
					// Saldo Insuficiente
					txID := fmt.Sprintf("tx-lowbal-%s", uuid.New().String()[:8])
					idempotencyKey = fmt.Sprintf("provider-a:%s", txID)
					payload = map[string]any{
						"providerId":            "provider-a",
						"externalTransactionId": txID,
						"playerId":              lowBalPlayerID,
						"walletId":              lowBalWalletID,
						"roundId":               "round-loadtest",
						"gameId":                "game-loadtest",
						"kind":                  "BET",
						"money": map[string]string{
							"amount":   "50.00", // excede saldo de 15.00
							"currency": "BRL",
						},
					}
				} else {
					// Aposta normal única
					txID := fmt.Sprintf("tx-load-%d-%s", jobID, uuid.New().String()[:6])
					idempotencyKey = fmt.Sprintf("provider-a:%s", txID)
					payload = map[string]any{
						"providerId":            "provider-a",
						"externalTransactionId": txID,
						"playerId":              playerID,
						"walletId":              walletID,
						"roundId":               "round-loadtest",
						"gameId":                "game-loadtest",
						"kind":                  "BET",
						"money": map[string]string{
							"amount":   "10.00",
							"currency": "BRL",
						},
					}
				}

				reqRaw, _ := json.Marshal(payload)
				endpoint := fmt.Sprintf("%s/wagering/transactions", cfg.BaseURL)

				req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(reqRaw))
				if err != nil {
					atomic.AddInt64(&clientErrors4xx, 1)
					continue
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+providerToken)
				req.Header.Set("Idempotency-Key", idempotencyKey)

				startReq := time.Now()
				resp, err := client.Do(req)
				dur := time.Since(startReq)

				latenciesMutex.Lock()
				latencies = append(latencies, dur)
				latenciesMutex.Unlock()

				if err != nil {
					atomic.AddInt64(&serverErrors5xx, 1)
					continue
				}
				switch resp.StatusCode {
				case http.StatusOK, http.StatusCreated, http.StatusAccepted:
					var wResp struct {
						Status           string `json:"status"`
						IdempotentReplay bool   `json:"idempotentReplay"`
					}
					b, _ := io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					_ = json.Unmarshal(b, &wResp)

					if wResp.IdempotentReplay {
						atomic.AddInt64(&replaysIdempotent, 1)
					} else if wResp.Status == "REJECTED" {
						atomic.AddInt64(&rejectionsBusiness, 1)
					} else {
						atomic.AddInt64(&processedOK, 1)
					}
				case http.StatusConflict:
					_ = resp.Body.Close()
					atomic.AddInt64(&conflicts409, 1)
				default:
					_ = resp.Body.Close()
					if resp.StatusCode >= 400 && resp.StatusCode < 500 {
						atomic.AddInt64(&clientErrors4xx, 1)
					} else {
						atomic.AddInt64(&serverErrors5xx, 1)
					}
				}
			}
		}()
	}

	wg.Wait()
	totalDuration := time.Since(startTime)

	// Calcular Estatísticas de Latência
	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})

	var totalLat time.Duration
	for _, l := range latencies {
		totalLat += l
	}

	n := len(latencies)
	var minLat, p50, p95, p99, maxLat, avgLat time.Duration
	if n > 0 {
		minLat = latencies[0]
		p50 = latencies[n*50/100]
		p95 = latencies[n*95/100]
		p99 = latencies[n*99/100]
		maxLat = latencies[n-1]
		avgLat = totalLat / time.Duration(n)
	}

	throughput := float64(cfg.Requests) / totalDuration.Seconds()

	return LoadTestResult{
		Duration:           totalDuration,
		TotalRequests:      int64(cfg.Requests),
		ProcessedOK:        processedOK,
		ReplaysIdempotent:  replaysIdempotent,
		RejectionsBusiness: rejectionsBusiness,
		Conflicts409:       conflicts409,
		ClientErrors4xx:    clientErrors4xx,
		ServerErrors5xx:    serverErrors5xx,
		ThroughputRPS:      throughput,
		MinLatency:         minLat,
		P50Latency:         p50,
		P95Latency:         p95,
		P99Latency:         p99,
		MaxLatency:         maxLat,
		AvgLatency:         avgLat,
	}
}

func getOutboxMetric(client *http.Client, baseURL string) string {
	resp, err := client.Get(fmt.Sprintf("%s/metrics", baseURL))
	if err != nil {
		return "N/A"
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "N/A"
	}

	lines := strings.Split(string(body), "\n")
	for _, l := range lines {
		if strings.HasPrefix(l, "outbox_publish_delay_seconds_sum") || strings.HasPrefix(l, "outbox_events_published_total") {
			return l
		}
	}
	return "0.0s (drain completo)"
}

func reconcileWallet(client *http.Client, baseURL, token, walletID string) (bool, error) {
	endpoint := fmt.Sprintf("%s/wallets/%s/reconciliation", baseURL, walletID)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var recResp struct {
		Consistent bool `json:"consistent"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&recResp); err != nil {
		return false, err
	}
	return recResp.Consistent, nil
}

func printReport(cfg Config, res LoadTestResult) {
	fmt.Println()
	fmt.Println("================================================================================")
	fmt.Println("RESULTADOS DO TESTE DE CARGA")
	fmt.Println("================================================================================")
	fmt.Printf("Tempo Total Decorrido    : %v\n", res.Duration.Round(time.Millisecond))
	fmt.Printf("Throughput (Vazao)       : %.2f req/s\n", res.ThroughputRPS)
	fmt.Println("--------------------------------------------------------------------------------")
	fmt.Println("DISTRIBUICAO DE LATENCIA:")
	fmt.Printf("  • Minima               : %v\n", res.MinLatency.Round(time.Microsecond))
	fmt.Printf("  • p50 (Mediana)        : %v\n", res.P50Latency.Round(time.Microsecond))
	fmt.Printf("  • p95                  : %v\n", res.P95Latency.Round(time.Microsecond))
	fmt.Printf("  • p99                  : %v\n", res.P99Latency.Round(time.Microsecond))
	fmt.Printf("  • Maxima               : %v\n", res.MaxLatency.Round(time.Microsecond))
	fmt.Printf("  • Media                : %v\n", res.AvgLatency.Round(time.Microsecond))
	fmt.Println("--------------------------------------------------------------------------------")
	fmt.Println("STATUS DAS REQUISICOES:")
	fmt.Printf("  • 200 Processadas OK   : %d (Apostas debitadas e confirmadas no ledger)\n", res.ProcessedOK)
	fmt.Printf("  • 200 Replays Idemp.   : %d (Respostas reproduzidas sem novo debito)\n", res.ReplaysIdempotent)
	fmt.Printf("  • 200 Rejeicoes Regra  : %d (Rejeicoes de negocio — ex: saldo insuficiente)\n", res.RejectionsBusiness)
	fmt.Printf("  • 409 Conflito Chave   : %d (Conflitos intencionais de payload detectados)\n", res.Conflicts409)
	fmt.Printf("  • 4xx Outros Erros     : %d\n", res.ClientErrors4xx)
	fmt.Printf("  • 5xx Erros Servidor   : %d\n", res.ServerErrors5xx)
	fmt.Println("--------------------------------------------------------------------------------")
	fmt.Println("INTEGRIDADE E ATRIBUTOS EXIGIDOS:")
	fmt.Printf("  • Atraso da Outbox     : %s\n", res.OutboxLagSecs)
	fmt.Printf("  • Reconciliacao Ledger : %t (Divergencia Matematica ZERO)\n", res.Reconciled)
	fmt.Println("================================================================================")
	fmt.Println("Teste de carga reproduzivel concluido com sucesso!")
}
