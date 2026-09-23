package middleware

import (
	"fmt"
	"net/http"

	"github.com/felipecristiano/desafio/internal/infra/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// TracingMiddleware cria e propaga spans OpenTelemetry para cada requisição HTTP.
func TracingMiddleware(next http.Handler) http.Handler {
	tracer := observability.GetTracer()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spanName := fmt.Sprintf("%s %s", r.Method, r.URL.Path)

		ctx, span := tracer.Start(r.Context(), spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.method", r.Method),
				attribute.String("http.url", r.URL.String()),
				attribute.String("http.user_agent", r.UserAgent()),
			),
		)
		defer span.End()

		corrID := GetCorrelationID(ctx)
		if corrID.String() != "" {
			span.SetAttributes(attribute.String("app.correlation_id", corrID.String()))
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
