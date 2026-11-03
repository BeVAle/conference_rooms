package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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

type postgresStore struct {
	*pgxpool.Pool
}

var (
	errNotFound     = errors.New("not found")
	errOverlap      = errors.New("booking overlaps")
	errStartPassed  = errors.New("booking start passed")
	errRoomNotFound = errors.New("room not found")
	errCannotCancel = errors.New("booking cannot be cancelled")
)

func (db postgresStore) UserByEmail(ctx context.Context, email string) (User, string, error) {
	var user User
	var hash string
	err := db.QueryRow(ctx, "SELECT id, name, email, password_hash FROM users WHERE email=$1", email).Scan(&user.ID, &user.Name, &user.Email, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errNotFound
	}
	return user, hash, err
}

func (db postgresStore) SessionUser(ctx context.Context, hash string) (User, error) {
	var user User
	err := db.QueryRow(ctx, `SELECT u.id,u.name,u.email FROM sessions s JOIN users u ON u.id=s.user_id
 WHERE s.token_hash=$1 AND s.expires_at>clock_timestamp()`, hash).Scan(&user.ID, &user.Name, &user.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errNotFound
	}
	return user, err
}

func (db postgresStore) StartSession(ctx context.Context, oldHash, hash string, userID int64, expires time.Time) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if oldHash != "" {
		if _, err := tx.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", oldHash); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM sessions WHERE expires_at <= now()"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES ($1,$2,$3)", hash, userID, expires); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (db postgresStore) EndSession(ctx context.Context, hash string) error {
	_, err := db.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", hash)
	return err
}

func (db postgresStore) Rooms(ctx context.Context, start, end time.Time) ([]Room, error) {
	rows, err := db.Query(ctx, `SELECT r.id, r.name, r.capacity FROM rooms r
		WHERE NOT EXISTS (SELECT 1 FROM bookings b WHERE b.room_id=r.id AND b.status='Active'
		AND tstzrange(b.starts_at,b.ends_at,'[)') && tstzrange($1::timestamptz,$2::timestamptz,'[)')) ORDER BY r.id`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rooms := []Room{}
	for rows.Next() {
		var room Room
		if err = rows.Scan(&room.ID, &room.Name, &room.Capacity); err != nil {
			return nil, err
		}
		rooms = append(rooms, room)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return rooms, nil
}

func (db postgresStore) Bookings(ctx context.Context, userID int64) ([]Booking, error) {
	rows, err := db.Query(ctx, `SELECT b.id,b.room_id,r.name,b.starts_at,b.ends_at,b.status,
		b.status='Active' AND b.starts_at>clock_timestamp()
		FROM bookings b JOIN rooms r ON r.id=b.room_id WHERE b.user_id=$1 ORDER BY b.starts_at,b.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bookings := []Booking{}
	for rows.Next() {
		var b Booking
		if err = rows.Scan(&b.ID, &b.RoomID, &b.RoomName, &b.StartsAt, &b.EndsAt, &b.Status, &b.CanCancel); err != nil {
			return nil, err
		}
		bookings = append(bookings, b)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return bookings, nil
}

func (db postgresStore) CreateBooking(ctx context.Context, userID, roomID int64, start, end time.Time) (Booking, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return Booking{}, err
	}
	defer tx.Rollback(ctx)
	var b Booking
	err = tx.QueryRow(ctx, `INSERT INTO bookings(user_id,room_id,starts_at,ends_at)
		SELECT $1,$2,$3::timestamptz,$4::timestamptz WHERE $3::timestamptz>clock_timestamp()
		RETURNING id,room_id,starts_at,ends_at,status`, userID, roomID, start, end).
		Scan(&b.ID, &b.RoomID, &b.StartsAt, &b.EndsAt, &b.Status)

	if errors.Is(err, pgx.ErrNoRows) {
		return Booking{}, errStartPassed
	}
	switch errorCode(err) {
	case "23P01":
		return Booking{}, errOverlap
	case "23503":
		return Booking{}, errRoomNotFound
	}
	if err != nil {
		return Booking{}, err
	}
	if err := tx.QueryRow(ctx, "SELECT name FROM rooms WHERE id=$1", b.RoomID).Scan(&b.RoomName); err != nil {
		return Booking{}, err
	}
	if !start.After(time.Now()) {
		return Booking{}, errStartPassed
	}
	if err := tx.Commit(ctx); err != nil {
		return Booking{}, err
	}
	b.CanCancel = b.StartsAt.After(time.Now())
	return b, nil
}

func (db postgresStore) CancelBooking(ctx context.Context, userID, id int64) (Booking, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return Booking{}, err
	}
	defer tx.Rollback(ctx)
	var b Booking
	err = tx.QueryRow(ctx, `SELECT id,room_id,starts_at,ends_at,status FROM bookings
		WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, userID).
		Scan(&b.ID, &b.RoomID, &b.StartsAt, &b.EndsAt, &b.Status)

	if errors.Is(err, pgx.ErrNoRows) {
		return Booking{}, errNotFound
	}
	if err != nil {
		return Booking{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE bookings SET status='Cancelled'
 WHERE id=$1 AND user_id=$2 AND status='Active' AND starts_at>clock_timestamp()`, id, userID)
	if err != nil {
		return Booking{}, err
	}
	if tag.RowsAffected() == 0 {
		return Booking{}, errCannotCancel
	}
	if err := tx.QueryRow(ctx, "SELECT name FROM rooms WHERE id=$1", b.RoomID).Scan(&b.RoomName); err != nil {
		return Booking{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Booking{}, err
	}
	b.Status = "Cancelled"
	b.CanCancel = false
	return b, nil
}
