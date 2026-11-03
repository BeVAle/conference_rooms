package main

import (
	"errors"
	"net/http"
	"strconv"
	"time"
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
	start, end, fields := validateInterval(r.URL.Query().Get("starts_at"), r.URL.Query().Get("ends_at"), a.now())
	if len(fields) != 0 {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "Проверьте время встречи.", Fields: fields})
		return
	}
	rooms, err := a.db.Rooms(r.Context(), start, end)
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rooms)
}

func (a *app) bookings(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(User)
	bookings, err := a.db.Bookings(r.Context(), user.ID)
	if err != nil {
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
	start, end, fields := validateInterval(input.StartsAt, input.EndsAt, a.now())
	if input.RoomID < 1 {
		fields["room_id"] = "Выберите комнату."
	}
	if len(fields) != 0 {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "Проверьте данные брони.", Fields: fields})
		return
	}
	user := r.Context().Value(userKey).(User)
	b, err := a.db.CreateBooking(r.Context(), user.ID, input.RoomID, start, end)
	switch {
	case errors.Is(err, errStartPassed):
		writeJSON(w, http.StatusBadRequest, apiError{Error: "Выберите новое время.", Fields: map[string]string{"starts_at": "Время начала уже наступило."}})
		return
	case errors.Is(err, errOverlap):
		audit(r, "create_booking", "conflict")
		fail(w, http.StatusConflict, conflictMessage)
		return
	case errors.Is(err, errRoomNotFound):
		fail(w, http.StatusBadRequest, "Комната не найдена.")
		return
	case err != nil:
		a.dbError(w, r, err)
		return
	}
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
	b, err := a.db.CancelBooking(r.Context(), user.ID, id)
	switch {
	case errors.Is(err, errNotFound):
		audit(r, "cancel_booking", "not_found")
		fail(w, http.StatusNotFound, "Бронь не найдена.")
		return
	case errors.Is(err, errCannotCancel):
		audit(r, "cancel_booking", "unavailable")
		fail(w, http.StatusConflict, "Бронь уже отменена или встреча началась. Обновите список.")
		return
	case err != nil:
		a.dbError(w, r, err)
		return
	}
	audit(r, "cancel_booking", "success")
	writeJSON(w, http.StatusOK, b)
}
