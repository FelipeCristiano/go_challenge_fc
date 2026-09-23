package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

const (
	CorrelationIDHeader = "X-Correlation-ID"
)

type correlationKey string

const CorrelationContextKey correlationKey = "correlation_id"

func CorrelationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corrIDStr := r.Header.Get(CorrelationIDHeader)
		corrID, err := uuid.Parse(corrIDStr)
		if err != nil || corrID == uuid.Nil {
			corrID = uuid.New()
		}

		ctx := context.WithValue(r.Context(), CorrelationContextKey, corrID)
		w.Header().Set(CorrelationIDHeader, corrID.String())

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		corrID, _ := r.Context().Value(CorrelationContextKey).(uuid.UUID)

		next.ServeHTTP(w, r)

		slog.Info("http request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"correlationId", corrID.String(),
			"durationMs", time.Since(start).Milliseconds(),
		)
	})
}

func GetCorrelationID(ctx context.Context) uuid.UUID {
	if corrID, ok := ctx.Value(CorrelationContextKey).(uuid.UUID); ok && corrID != uuid.Nil {
		return corrID
	}
	return uuid.New()
}
