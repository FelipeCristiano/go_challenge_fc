package http

import (
	"github.com/felipecristiano/desafio/internal/http/handler"
	"github.com/felipecristiano/desafio/internal/http/middleware"
	"github.com/felipecristiano/desafio/internal/infra/auth"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func NewRouter(
	validator auth.TokenValidator,
	healthH *handler.HealthHandler,
	walletH *handler.WalletHandler,
	wagerH *handler.WagerHandler,
) *chi.Mux {
	r := chi.NewRouter()

	// Middlewares globais essenciais
	r.Use(chimiddleware.Recoverer)
	r.Use(middleware.CorrelationMiddleware)
	r.Use(middleware.LoggingMiddleware)

	// Métricas Prometheus
	r.Handle("/metrics", promhttp.Handler())

	// Health checks públicos (sem autenticação)
	r.Route("/health", func(r chi.Router) {
		r.Get("/live", healthH.Live)
		r.Get("/ready", healthH.Ready)
	})

	// Rotas protegidas por OAuth 2.0 / OIDC
	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthMiddleware(validator))

		// Operações de Carteira (restritas a serviço interno)
		r.Route("/wallets", func(r chi.Router) {
			r.With(middleware.RequireRole("internal")).Post("/", walletH.OpenWallet)
			r.With(middleware.RequireRole("internal")).Get("/{walletId}", walletH.GetWallet)
			r.With(middleware.RequireRole("internal")).Get("/{walletId}/ledger", walletH.GetLedger)
			r.With(middleware.RequireRole("internal")).Post("/{walletId}/reconciliation", walletH.Reconcile)
		})

		// Operações de Apostas e Transações
		r.Route("/wagering", func(r chi.Router) {
			r.Post("/transactions", wagerH.ProcessWager)
			r.Get("/transactions/{transactionId}", wagerH.GetTransactionByID)
		})

		// Consultas de Provedores
		r.Route("/providers/{providerId}", func(r chi.Router) {
			r.Get("/wagering/transactions/{externalTransactionId}", wagerH.GetTransactionByProviderAndExternalID)
		})
	})

	return r
}
