package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/felipecristiano/desafio/internal/infra/config"
	"go.uber.org/fx"
)

func main() {
	// Logger JSON estruturado para stdout
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	app := fx.New(
		// Módulo de configuração
		fx.Provide(config.Load),

		// TODO: demais módulos serão adicionados nas próximas fases

		fx.Invoke(func(lc fx.Lifecycle, cfg *config.Config) {
			lc.Append(fx.Hook{
				OnStart: func(ctx context.Context) error {
					slog.Info("application starting",
						"http_port", cfg.HTTP.Port,
						"sqs_queue", cfg.SQS.QueueURL,
						"keycloak_issuer", cfg.Auth.Issuer,
					)
					return nil
				},
				OnStop: func(ctx context.Context) error {
					slog.Info("application stopped")
					return nil
				},
			})
		}),
	)

	app.Run()
}
