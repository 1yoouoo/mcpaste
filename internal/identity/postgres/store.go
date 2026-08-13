package postgres

import (
	"context"

	"github.com/1yoouoo/mcpaste/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

type txStore struct {
	tx pgx.Tx
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) WithinTx(ctx context.Context, fn func(identity.TxStore) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return fn(&txStore{tx: tx})
	})
}
