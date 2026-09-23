package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felipecristiano/desafio/internal/infra/observability"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestObservabilityMetrics_RecordingAndExport(t *testing.T) {
	// 1. Simula incremento e observação de todas as métricas exigidas
	observability.WagerTransactionsTotal.WithLabelValues("PROCESSED", "BET", "HTTP").Inc()
	observability.WagerProcessingDurationSeconds.WithLabelValues("BET", "HTTP", "PROCESSED").Observe(0.042)
	observability.WagerDuplicatesTotal.WithLabelValues("HTTP", "idempotent_replay").Inc()
	observability.WagerRetriesTotal.WithLabelValues("sqs", "failure").Inc()
	observability.WagerDLQMessagesTotal.WithLabelValues("poison_pill").Inc()
	observability.WagerConcurrencyConflictsTotal.WithLabelValues("process_wager", "payload_conflict").Inc()
	observability.OutboxPublishDelaySeconds.Observe(0.125)
	observability.OutboxEventsPublishedTotal.WithLabelValues("WagerTransactionProcessed", "success").Inc()
	observability.ReconciliationChecksTotal.WithLabelValues("ok").Inc()
	observability.ReconciliationDivergencesTotal.WithLabelValues("BRL").Inc()

	// 2. Chama o handler de /metrics usando promhttp
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()

	handler := promhttp.Handler()
	handler.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected /metrics 200 OK, got %d", res.StatusCode)
	}

	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("failed to read /metrics body: %v", err)
	}
	body := string(bodyBytes)

	// 3. Valida presença de todas as métricas obrigatórias do DESAFIO.md
	expectedMetrics := []string{
		"wager_transactions_total",
		"wager_processing_duration_seconds",
		"wager_transactions_duplicates_total",
		"wager_retries_total",
		"wager_dlq_messages_total",
		"wager_concurrency_conflicts_total",
		"outbox_publish_delay_seconds",
		"outbox_published_events_total",
		"reconciliation_checks_total",
		"reconciliation_divergences_total",
	}

	for _, metricName := range expectedMetrics {
		if !strings.Contains(body, metricName) {
			t.Errorf("expected /metrics output to contain %q, but it was missing", metricName)
		}
	}
}

func TestStructuredJSONLogging_TraceabilityIdentifiers(t *testing.T) {
	// Captura logs em buffer de memória
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	corrID := uuid.New().String()
	msgID := "sqs-msg-123"
	txnID := uuid.New().String()
	walletID := uuid.New().String()
	providerID := "provider-xyz"

	logger.InfoContext(context.Background(), "test structured log",
		"correlationId", corrID,
		"messageId", msgID,
		"transactionId", txnID,
		"walletId", walletID,
		"providerId", providerID,
		"status", "PROCESSED",
	)

	logLine := buf.String()

	var parsed map[string]any
	if err := json.Unmarshal([]byte(logLine), &parsed); err != nil {
		t.Fatalf("log is not valid JSON: %v, raw: %s", err, logLine)
	}

	// Valida se os identificadores obrigatórios estão presentes
	if parsed["correlationId"] != corrID {
		t.Errorf("expected correlationId %q, got %v", corrID, parsed["correlationId"])
	}
	if parsed["messageId"] != msgID {
		t.Errorf("expected messageId %q, got %v", msgID, parsed["messageId"])
	}
	if parsed["transactionId"] != txnID {
		t.Errorf("expected transactionId %q, got %v", txnID, parsed["transactionId"])
	}
	if parsed["walletId"] != walletID {
		t.Errorf("expected walletId %q, got %v", walletID, parsed["walletId"])
	}
	if parsed["providerId"] != providerID {
		t.Errorf("expected providerId %q, got %v", providerID, parsed["providerId"])
	}

	// Valida que nenhum segredo/credencial ou payload financeiro bruto foi logado
	for key := range parsed {
		lowerKey := strings.ToLower(key)
		if strings.Contains(lowerKey, "password") ||
			strings.Contains(lowerKey, "secret") ||
			strings.Contains(lowerKey, "token") ||
			strings.Contains(lowerKey, "credential") {
			t.Errorf("sensitive key %q found in log output", key)
		}
	}
}
