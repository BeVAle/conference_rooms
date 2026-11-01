package main

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const conflictMessage = "Комнату уже забронировали на выбранное время. Выберите другую комнату или измените время"

type Room struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Capacity int    `json:"capacity"`
}

type Booking struct {
	ID        int64     `json:"id"`
	RoomID    int64     `json:"room_id"`
	RoomName  string    `json:"room_name"`
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	Status    string    `json:"status"`
	CanCancel bool      `json:"can_cancel"`
}

func validateInterval(startText, endText string, now time.Time) (time.Time, time.Time, map[string]string) {
	fields := map[string]string{}
	start, err := time.Parse(time.RFC3339, startText)
	if err != nil {
		fields["starts_at"] = "Укажите дату и время начала."
	} else if !start.After(now) {
		fields["starts_at"] = "Начало должно быть позже текущего времени."
	}
	end, err := time.Parse(time.RFC3339, endText)
	if err != nil {
		fields["ends_at"] = "Укажите дату и время окончания."
	} else if !start.IsZero() && (end.Sub(start) < 30*time.Minute || end.Sub(start) > 4*time.Hour) {
		fields["ends_at"] = "Длительность должна составлять от 30 минут до 4 часов."
	}
	return start.UTC(), end.UTC(), fields
}

func (a *app) rooms(w http.ResponseWriter, r *http.Request) {
	start, end, fields := validateInterval(r.URL.Query().Get("starts_at"), r.URL.Query().Get("ends_at"), time.Now())
	if len(fields) != 0 {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "Проверьте время встречи.", Fields: fields})
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT r.id, r.name, r.capacity FROM rooms r
		WHERE NOT EXISTS (SELECT 1 FROM bookings b WHERE b.room_id=r.id AND b.status='Active'
		AND tstzrange(b.starts_at,b.ends_at,'[)') && tstzrange($1::timestamptz,$2::timestamptz,'[)')) ORDER BY r.id`, start, end)
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	defer rows.Close()
	rooms := []Room{}
	for rows.Next() {
		var room Room
		if err = rows.Scan(&room.ID, &room.Name, &room.Capacity); err != nil {
			a.dbError(w, r, err)
			return
		}
		rooms = append(rooms, room)
	}
	if err = rows.Err(); err != nil {
		a.dbError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rooms)
}

func (a *app) bookings(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(User)
	rows, err := a.db.Query(r.Context(), `SELECT b.id,b.room_id,r.name,b.starts_at,b.ends_at,b.status,
		b.status='Active' AND b.starts_at>clock_timestamp()
		FROM bookings b JOIN rooms r ON r.id=b.room_id WHERE b.user_id=$1 ORDER BY b.starts_at,b.id`, user.ID)
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	defer rows.Close()
	bookings := []Booking{}
	for rows.Next() {
		var b Booking
		if err = rows.Scan(&b.ID, &b.RoomID, &b.RoomName, &b.StartsAt, &b.EndsAt, &b.Status, &b.CanCancel); err != nil {
			a.dbError(w, r, err)
			return
		}
		bookings = append(bookings, b)
	}
	if err = rows.Err(); err != nil {
		a.dbError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bookings)
}

func (a *app) createBooking(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RoomID   int64  `json:"room_id"`
		StartsAt string `json:"starts_at"`
		EndsAt   string `json:"ends_at"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	start, end, fields := validateInterval(input.StartsAt, input.EndsAt, time.Now())
	if input.RoomID < 1 {
		fields["room_id"] = "Выберите комнату."
	}
	if len(fields) != 0 {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "Проверьте данные брони.", Fields: fields})
		return
	}
	user := r.Context().Value(userKey).(User)
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var b Booking
	err = tx.QueryRow(r.Context(), `INSERT INTO bookings(user_id,room_id,starts_at,ends_at)
		SELECT $1,$2,$3::timestamptz,$4::timestamptz WHERE $3::timestamptz>clock_timestamp()
		RETURNING id,room_id,starts_at,ends_at,status`, user.ID, input.RoomID, start, end).
		Scan(&b.ID, &b.RoomID, &b.StartsAt, &b.EndsAt, &b.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "Выберите новое время.", Fields: map[string]string{"starts_at": "Время начала уже наступило."}})
		return
	}
	if errorCode(err) == "23P01" {
		audit(r, "create_booking", "conflict")
		fail(w, http.StatusConflict, conflictMessage)
		return
	}
	if errorCode(err) == "23503" {
		fail(w, http.StatusBadRequest, "Комната не найдена.")
		return
	}
	if err == nil {
		err = tx.QueryRow(r.Context(), "SELECT name FROM rooms WHERE id=$1", b.RoomID).Scan(&b.RoomName)
	}
	// INSERT мог ждать конкурирующую транзакцию; время проверяется ещё раз перед commit.
	if err == nil && !start.After(time.Now()) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "Выберите новое время.", Fields: map[string]string{"starts_at": "Время начала уже наступило."}})
		return
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	b.CanCancel = b.StartsAt.After(time.Now())
	audit(r, "create_booking", "success")
	writeJSON(w, http.StatusCreated, b)
}

func (a *app) cancelBooking(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		fail(w, http.StatusBadRequest, "Некорректный номер брони.")
		return
	}
	user := r.Context().Value(userKey).(User)
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var b Booking
	// Блокировка строки исключает одновременные отмены и повторную проверку устаревшего статуса.
	err = tx.QueryRow(r.Context(), `SELECT id,room_id,starts_at,ends_at,status FROM bookings
		WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, user.ID).
		Scan(&b.ID, &b.RoomID, &b.StartsAt, &b.EndsAt, &b.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		audit(r, "cancel_booking", "not_found")
		fail(w, http.StatusNotFound, "Бронь не найдена.")
		return
	}
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	tag, err := tx.Exec(r.Context(), `UPDATE bookings SET status='Cancelled'
		WHERE id=$1 AND user_id=$2 AND status='Active' AND starts_at>clock_timestamp()`, id, user.ID)
	if err == nil && tag.RowsAffected() == 0 {
		audit(r, "cancel_booking", "unavailable")
		fail(w, http.StatusConflict, "Бронь уже отменена или встреча началась. Обновите список.")
		return
	}
	if err == nil {
		err = tx.QueryRow(r.Context(), "SELECT name FROM rooms WHERE id=$1", b.RoomID).Scan(&b.RoomName)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	b.Status = "Cancelled"
	b.CanCancel = false
	audit(r, "cancel_booking", "success")
	writeJSON(w, http.StatusOK, b)
}
