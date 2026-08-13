package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/1yoouoo/mcpaste/db/migrations"
	"github.com/1yoouoo/mcpaste/internal/database/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type migrateCommand uint8

const (
	commandUp migrateCommand = iota
	commandStatus
	commandVerify
	commandDown
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "mcpaste migrate: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string) error {
	command, err := validateArgs(args)
	if err != nil {
		return err
	}
	databaseURL := getenv("MCPASTE_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("MCPASTE_DATABASE_URL is required")
	}
	available, err := migrate.Load(migrations.Files)
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return errors.New("parse MCPASTE_DATABASE_URL")
	}
	defer pool.Close()
	return migrate.WithLock(ctx, pool, func(conn *pgx.Conn) error {
		switch command {
		case commandUp:
			return migrate.Up(ctx, conn, available)
		case commandStatus:
			status, err := migrate.Status(ctx, conn, available)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(os.Stdout, "applied=%d available=%d\n", len(status.Applied), status.Available)
			return nil
		case commandVerify:
			status, err := migrate.RequireCurrent(ctx, conn, available)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(os.Stdout, "applied=%d available=%d\n", len(status.Applied), status.Available)
			return nil
		case commandDown:
			status, err := migrate.Status(ctx, conn, available)
			if err != nil {
				return err
			}
			if len(status.Applied) == 0 {
				return errors.New("database has no applied migration")
			}
			selected := status.Applied[len(status.Applied)-1]
			_, _ = fmt.Fprintf(os.Stdout, "rolling_back=%06d_%s\n", selected.Version, selected.Name)
			return migrate.DownOne(ctx, conn, available)
		default:
			return errors.New("invalid migration command")
		}
	})
}

func validateArgs(args []string) (migrateCommand, error) {
	if len(args) == 0 {
		return 0, errors.New("usage: mcpaste-migrate up|status|verify|down --steps 1")
	}
	switch args[0] {
	case "up":
		if len(args) != 1 {
			return 0, errors.New("usage: mcpaste-migrate up")
		}
		return commandUp, nil
	case "status":
		if len(args) != 1 {
			return 0, errors.New("usage: mcpaste-migrate status")
		}
		return commandStatus, nil
	case "verify":
		if len(args) != 1 {
			return 0, errors.New("usage: mcpaste-migrate verify")
		}
		return commandVerify, nil
	case "down":
		if len(args) != 3 || args[1] != "--steps" || args[2] != "1" {
			return 0, errors.New("usage: mcpaste-migrate down --steps 1")
		}
		return commandDown, nil
	default:
		return 0, errors.New("usage: mcpaste-migrate up|status|verify|down --steps 1")
	}
}
