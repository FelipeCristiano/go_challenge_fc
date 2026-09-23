package middleware

import "net/http"

// SecurityHeadersMiddleware injeta cabeçalhos HTTP defensivos para proteger contra
// ataques de MIME-sniffing, Clickjacking e XSS conforme recomendações OWASP.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		next.ServeHTTP(w, r)
	})
}
