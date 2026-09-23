package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/felipecristiano/desafio/internal/infra/auth"
)

type contextKey string

const (
	ClaimsContextKey contextKey = "auth_claims"
)

func AuthMiddleware(validator auth.TokenValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, `{"error":"unauthorized","message":"missing authorization header"}`, http.StatusUnauthorized)
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				http.Error(w, `{"error":"unauthorized","message":"invalid authorization format"}`, http.StatusUnauthorized)
				return
			}

			tokenString := parts[1]
			claims, err := validator.ValidateToken(r.Context(), tokenString)
			if err != nil {
				http.Error(w, `{"error":"unauthorized","message":"invalid or expired token"}`, http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), ClaimsContextKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole garante que o token possua um papel específico (ex.: "internal" ou "provider").
func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := r.Context().Value(ClaimsContextKey).(*auth.Claims)
			if !ok || claims == nil {
				http.Error(w, `{"error":"unauthorized","message":"no claims found"}`, http.StatusUnauthorized)
				return
			}

			if !claims.HasRole(role) {
				http.Error(w, `{"error":"forbidden","message":"insufficient permissions"}`, http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func GetClaims(ctx context.Context) (*auth.Claims, bool) {
	claims, ok := ctx.Value(ClaimsContextKey).(*auth.Claims)
	return claims, ok
}
