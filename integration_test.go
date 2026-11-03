package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err = admin.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS btree_gist WITH SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	schema := pgx.Identifier{"test_" + rand.Text()}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err = migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	return newApp(postgresStore{db}, time.Now).routes()
}

func TestBookingFlow(t *testing.T) {
	handler := testHandler(t)
	request := func(method, path string, body any, session *http.Cookie, want int) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		if session != nil {
			req.AddCookie(session)
		}
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, resp.Code, want, resp.Body.String())
		}
		return resp
	}
	login := func(email string) *http.Cookie {
		t.Helper()
		resp := request("POST", "/api/login", map[string]string{"email": email, "password": "conference123"}, nil, 200)
		cookies := resp.Result().Cookies()
		if len(cookies) == 0 {
			t.Fatal("login did not create a session")
		}
		return cookies[0]
	}
	request("POST", "/api/login", map[string]string{"email": "missing@example.com", "password": "conference123"}, nil, 401)
	user := login("user@example.com")
	other := login("colleague@example.com")
	start := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Minute)
	roomsPath := func(from, to time.Time) string {
		params := url.Values{"starts_at": {from.Format(time.RFC3339)}, "ends_at": {to.Format(time.RFC3339)}}
		return "/api/rooms?" + params.Encode()
	}
	hasRoom := func(path string, id int64, want bool) {
		t.Helper()
		resp := request("GET", path, nil, user, 200)
		var rooms []Room
		if err := json.Unmarshal(resp.Body.Bytes(), &rooms); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, room := range rooms {
			if room.ID == id {
				found = true
			}
		}
		if found != want {
			t.Fatalf("room %d availability = %v, want %v", id, found, want)
		}
	}
	interval := roomsPath(start, start.Add(time.Hour))
	hasRoom(interval, 1, true)
	request("POST", "/api/bookings", map[string]any{"room_id": 999, "starts_at": start, "ends_at": start.Add(time.Hour)}, user, 400)
	input := map[string]any{"room_id": 1, "starts_at": start.Format(time.RFC3339), "ends_at": start.Add(time.Hour).Format(time.RFC3339)}
	resp := request("POST", "/api/bookings", input, user, 201)
	var booking Booking
	if err := json.Unmarshal(resp.Body.Bytes(), &booking); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/bookings", input, other, 409)
	hasRoom(interval, 1, false)
	hasRoom(roomsPath(start.Add(time.Hour), start.Add(2*time.Hour)), 1, true)

	resp = request("GET", "/api/bookings", nil, other, 200)
	if string(bytes.TrimSpace(resp.Body.Bytes())) != "[]" {
		t.Fatal("foreign booking is visible")
	}
	cancelPath := fmt.Sprintf("/api/bookings/%d/cancel", booking.ID)
	request("POST", cancelPath, map[string]any{}, other, 404)

	request("POST", cancelPath, map[string]any{}, user, 200)
	request("POST", cancelPath, map[string]any{}, user, 409)
	hasRoom(interval, 1, true)
	resp = request("GET", "/api/bookings", nil, user, 200)
	var history []Booking
	if err := json.Unmarshal(resp.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ID != booking.ID || history[0].Status != "Cancelled" {
		t.Fatal("cancelled booking is missing from history")
	}
	request("POST", "/api/bookings", input, other, 201)

	renewed := request("POST", "/api/login", map[string]string{"email": "user@example.com", "password": "conference123"}, user, 200).Result().Cookies()
	if len(renewed) != 1 {
		t.Fatal("login did not replace session")
	}
	request("GET", "/api/me", nil, user, 401)
	request("GET", "/api/me", nil, renewed[0], 200)
	request("POST", "/api/logout", map[string]any{}, renewed[0], 200)
	request("GET", "/api/me", nil, renewed[0], 401)
}
