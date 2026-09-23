package port

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX define a interface comum entre *pgxpool.Pool e pgx.Tx para execução de SQL.
type DBTX interface {
	Exec(ctx context.Context, sql string, arguments ...any) (commandTag pgconn.CommandTag, err error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// UnitOfWork define o contrato para gerenciar transações atômicas no banco de dados.
type UnitOfWork interface {
	WithTx(ctx context.Context, fn func(tx DBTX) error) error
}
