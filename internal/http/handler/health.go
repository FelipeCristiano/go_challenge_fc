package handler

import (
	"encoding/json"
	"net/http"

	"github.com/felipecristiano/desafio/internal/application/port"
)

type HealthHandler struct {
	pool port.DBTX
}

func NewHealthHandler(pool port.DBTX) *HealthHandler {
	return &HealthHandler{pool: pool}
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

	// Testa conectividade real com PostgreSQL
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

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":   "UP",
		"database": "READY",
	})
}
