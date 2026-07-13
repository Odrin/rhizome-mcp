package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Executor is the minimal SQL surface available to a write callback. All
// methods execute on the single connection that owns BEGIN IMMEDIATE.
type Executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Queryer is the read-only SQL surface exposed to connection-scoped checks.
// It intentionally omits ExecContext so callers cannot perform ordinary
// mutations through Read.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type retryPolicy struct {
	delays  []time.Duration
	sleeper Sleeper
}

type timerSleeper struct{}

func (timerSleeper) Sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func newRetryPolicy(policy *RetryPolicy) (retryPolicy, error) {
	if policy == nil {
		return retryPolicy{delays: append([]time.Duration(nil), defaultRetryDelays...), sleeper: timerSleeper{}}, nil
	}
	if policy.Sleeper == nil {
		return retryPolicy{}, errors.New("retry sleeper must not be nil")
	}
	delays := append([]time.Duration(nil), policy.Delays...)
	for _, delay := range delays {
		if delay < 0 {
			return retryPolicy{}, errors.New("retry delay must not be negative")
		}
	}
	return retryPolicy{delays: delays, sleeper: policy.Sleeper}, nil
}

// Write executes fn inside a short BEGIN IMMEDIATE transaction. The complete
// transaction is retried only for SQLite BUSY or LOCKED results.
func (db *DB) Write(ctx context.Context, fn func(context.Context, Executor) error) error {
	if fn == nil {
		return errors.New("SQLite write callback must not be nil")
	}
	for attempt := 0; ; attempt++ {
		err := db.writeOnce(ctx, fn)
		if err == nil {
			return nil
		}
		if !isLockContention(err) || attempt >= len(db.retry.delays) {
			return TranslateError(err)
		}
		if err := db.retry.sleeper.Sleep(ctx, db.retry.delays[attempt]); err != nil {
			return err
		}
	}
}

// Read acquires one configured pooled connection and invokes fn with a
// query-only surface. Connection-local PRAGMAs can therefore be verified on
// the same connection as the other read checks.
func (db *DB) Read(ctx context.Context, fn func(context.Context, Queryer) error) error {
	if db == nil || db.pool == nil {
		return errors.New("SQLite database must not be nil")
	}
	if fn == nil {
		return errors.New("SQLite read callback must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := db.pool.Conn(ctx)
	if err != nil {
		return TranslateError(err)
	}
	defer conn.Close()
	return TranslateError(fn(ctx, conn))
}

func (db *DB) writeOnce(ctx context.Context, fn func(context.Context, Executor) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := db.pool.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	if err := fn(ctx, conn); err != nil {
		return errors.Join(err, rollback(ctx, conn))
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return errors.Join(err, rollback(ctx, conn))
	}
	return nil
}

func rollback(parent context.Context, conn *sql.Conn) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	_, err := conn.ExecContext(ctx, "ROLLBACK")
	return err
}
