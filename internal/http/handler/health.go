package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/felipecristiano/desafio/internal/application/port"
)

// SQSPinger define a interface mínima necessária para verificar conectividade com o SQS.
type SQSPinger interface {
	ListQueues(ctx context.Context, params *awssqs.ListQueuesInput, optFns ...func(*awssqs.Options)) (*awssqs.ListQueuesOutput, error)
}

type HealthHandler struct {
	pool port.DBTX
	sqs  SQSPinger

	mu           sync.RWMutex
	lastSQSCheck time.Time
	lastSQSErr   error
	sqsCacheTTL  time.Duration
}

func NewHealthHandler(pool port.DBTX) *HealthHandler {
	return &HealthHandler{
		pool:        pool,
		sqsCacheTTL: 15 * time.Second,
	}
}

func NewHealthHandlerWithSQS(pool port.DBTX, sqs SQSPinger) *HealthHandler {
	return &HealthHandler{
		pool:        pool,
		sqs:         sqs,
		sqsCacheTTL: 15 * time.Second,
	}
}

func (h *HealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "UP",
	})
}

func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// 1. Testa conectividade real com PostgreSQL
	var one int
	err := h.pool.QueryRow(r.Context(), "SELECT 1").Scan(&one)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "DOWN",
			"database": "UNAVAILABLE",
			"error":    err.Error(),
		})
		return
	}

	// 2. Testa conectividade real com SQS (se configurado, com cache TTL para evitar throttling da AWS)
	if h.sqs != nil {
		h.mu.RLock()
		cachedErr := h.lastSQSErr
		expired := time.Since(h.lastSQSCheck) > h.sqsCacheTTL
		h.mu.RUnlock()

		if expired {
			h.mu.Lock()
			if time.Since(h.lastSQSCheck) > h.sqsCacheTTL {
				maxResults := int32(1)
				checkCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
				_, err := h.sqs.ListQueues(checkCtx, &awssqs.ListQueuesInput{MaxResults: &maxResults})
				cancel()
				h.lastSQSErr = err
				h.lastSQSCheck = time.Now()
				cachedErr = err
			} else {
				cachedErr = h.lastSQSErr
			}
			h.mu.Unlock()
		}

		if cachedErr != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":   "DOWN",
				"database": "READY",
				"sqs":      "UNAVAILABLE",
				"error":    cachedErr.Error(),
			})
			return
		}
	}

	resp := map[string]string{
		"status":   "UP",
		"database": "READY",
	}
	if h.sqs != nil {
		resp["sqs"] = "READY"
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
