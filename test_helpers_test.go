package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var unavailable = errors.New("database unavailable")

type stubStore struct {
	pingErr  error
	user     func(string) (User, string, error)
	session  func(string) (User, error)
	start    func(string, string, int64, time.Time) error
	end      func(string) error
	rooms    func(time.Time, time.Time) ([]Room, error)
	bookings func(int64) ([]Booking, error)
	create   func(int64, int64, time.Time, time.Time) (Booking, error)
	cancel   func(int64, int64) (Booking, error)
}

func (s stubStore) Ping(context.Context) error { return s.pingErr }
func (s stubStore) UserByEmail(_ context.Context, email string) (User, string, error) {
	return s.user(email)
}
func (s stubStore) SessionUser(_ context.Context, hash string) (User, error) { return s.session(hash) }
func (s stubStore) StartSession(_ context.Context, old, hash string, id int64, expires time.Time) error {
	return s.start(old, hash, id, expires)
}
func (s stubStore) EndSession(_ context.Context, hash string) error { return s.end(hash) }
func (s stubStore) Rooms(_ context.Context, start, end time.Time) ([]Room, error) {
	return s.rooms(start, end)
}
func (s stubStore) Bookings(_ context.Context, id int64) ([]Booking, error) { return s.bookings(id) }
func (s stubStore) CreateBooking(_ context.Context, userID, roomID int64, start, end time.Time) (Booking, error) {
	return s.create(userID, roomID, start, end)
}
func (s stubStore) CancelBooking(_ context.Context, userID, id int64) (Booking, error) {
	return s.cancel(userID, id)
}

var employee = User{ID: 42, Name: "Сотрудник", Email: "user@example.com"}

func employeeSession(t *testing.T) func(string) (User, error) {
	t.Helper()
	return func(hash string) (User, error) {
		if hash != tokenHash("test-token") {
			t.Fatalf("incorrect session key: %q", hash)
		}
		return employee, nil
	}
}

var testTime = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

func fixedTime() time.Time { return testTime }

func callAPI(t *testing.T, db store, method, path, body string, session bool, want int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if session {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-token"})
	}
	rec := httptest.NewRecorder()
	newApp(db, fixedTime).routes().ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("%s %s: got %d, want %d: %s", method, path, rec.Code, want, rec.Body.String())
	}
	return rec
}
