package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func migrate(ctx context.Context, db *pgxpool.Pool) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return errors.New("не удалось начать миграцию")
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version integer PRIMARY KEY)"); err != nil {
		return errors.New("не удалось создать таблицу миграций")
	}
	var applied bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 1)").Scan(&applied); err != nil {
		return err
	}
	if !applied {
		sql, _ := files.ReadFile("migrations/001_init.sql")
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("миграция 001: SQLSTATE %s", errorCode(err))
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations VALUES (1)"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return "connection"
}
