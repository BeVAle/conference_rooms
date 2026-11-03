package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestInterval(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, start, end string
		valid            bool
	}{
		{"minimum", "2026-11-01T11:00:00Z", "2026-11-01T11:30:00Z", true},
		{"maximum", "2026-11-01T11:00:00Z", "2026-11-01T15:00:00Z", true},
		{"office timezone", "2026-11-01T16:00:00+05:00", "2026-11-01T17:00:00+05:00", true},
		{"midnight", "2026-11-01T23:30:00+05:00", "2026-11-02T00:30:00+05:00", true},
		{"past", "2026-11-01T09:00:00Z", "2026-11-01T10:00:00Z", false},
		{"now", "2026-11-01T10:00:00Z", "2026-11-01T11:00:00Z", false},
		{"short", "2026-11-01T11:00:00Z", "2026-11-01T11:29:00Z", false},
		{"long", "2026-11-01T11:00:00Z", "2026-11-01T15:01:00Z", false},
		{"backwards", "2026-11-01T11:00:00Z", "2026-11-01T10:00:00Z", false},
		{"no timezone", "2026-11-01T11:00:00", "2026-11-01T12:00:00", false},
		{"missing", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, _, fields := validateInterval(tc.start, tc.end, now)
			if (len(fields) == 0) != tc.valid {
				t.Fatalf("fields=%v", fields)
			}
			if tc.valid && start.Location() != time.UTC {
				t.Fatal("time must be UTC")
			}
		})
	}
}

func TestBookingLists(t *testing.T) {
	start := fixedTime().Add(time.Hour).Truncate(time.Second)
	end := start.Add(time.Hour)
	query := "?starts_at=" + start.Format(time.RFC3339) + "&ends_at=" + end.Format(time.RFC3339)
	db := stubStore{session: employeeSession(t)}
	callAPI(t, db, "GET", "/api/rooms", "", true, 400)
	db.rooms = func(from, to time.Time) ([]Room, error) {
		if !from.Equal(start) || !to.Equal(end) {
			t.Fatal("wrong room search interval")
		}
		return []Room{{ID: 1, Name: "Переговорная", Capacity: 8}}, nil
	}
	rec := callAPI(t, db, "GET", "/api/rooms"+query, "", true, 200)
	var rooms []Room
	if err := json.Unmarshal(rec.Body.Bytes(), &rooms); err != nil || len(rooms) != 1 || rooms[0].ID != 1 || rooms[0].Capacity != 8 {
		t.Fatalf("wrong rooms response: %s", rec.Body.String())
	}
	db.bookings = func(id int64) ([]Booking, error) {
		if id != employee.ID {
			t.Fatalf("requested bookings of user %d", id)
		}
		return []Booking{{ID: 10, Status: "Active"}}, nil
	}
	rec = callAPI(t, db, "GET", "/api/bookings", "", true, 200)
	var bookings []Booking
	if err := json.Unmarshal(rec.Body.Bytes(), &bookings); err != nil || len(bookings) != 1 || bookings[0].ID != 10 {
		t.Fatalf("wrong bookings response: %s", rec.Body.String())
	}
	db.rooms = func(time.Time, time.Time) ([]Room, error) { return nil, unavailable }
	db.bookings = func(int64) ([]Booking, error) { return nil, unavailable }
	callAPI(t, db, "GET", "/api/rooms"+query, "", true, 503)
	callAPI(t, db, "GET", "/api/bookings", "", true, 503)
}

func TestCreateBooking(t *testing.T) {
	start := fixedTime().Add(time.Hour)
	end := start.Add(time.Hour)
	body, err := json.Marshal(map[string]any{"room_id": 7, "starts_at": start, "ends_at": end})
	if err != nil {
		t.Fatal(err)
	}
	db := stubStore{session: employeeSession(t)}
	callAPI(t, db, "POST", "/api/bookings", "{", true, 400)
	callAPI(t, db, "POST", "/api/bookings", "{}", true, 400)
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"created", nil, 201},
		{"overlap", errOverlap, 409},
		{"past start", errStartPassed, 400},
		{"missing room", errRoomNotFound, 400},
		{"unavailable", unavailable, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db.create = func(userID, roomID int64, from, to time.Time) (Booking, error) {
				if userID != employee.ID || roomID != 7 || !from.Equal(start) || !to.Equal(end) {
					t.Fatal("wrong booking passed to storage")
				}
				return Booking{ID: 10, RoomID: roomID, Status: "Active"}, tc.err
			}
			rec := callAPI(t, db, "POST", "/api/bookings", string(body), true, tc.want)
			if tc.err == nil {
				var booking Booking
				if err := json.Unmarshal(rec.Body.Bytes(), &booking); err != nil || booking.ID != 10 || booking.RoomID != 7 {
					t.Fatalf("wrong booking response: %s", rec.Body.String())
				}
			}
		})
	}
}

func TestCancelBooking(t *testing.T) {
	db := stubStore{session: employeeSession(t)}
	callAPI(t, db, "POST", "/api/bookings/bad/cancel", "{}", true, 400)
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"cancelled", nil, 200},
		{"missing or foreign", errNotFound, 404},
		{"already started or cancelled", errCannotCancel, 409},
		{"unavailable", unavailable, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db.cancel = func(userID, bookingID int64) (Booking, error) {
				if userID != employee.ID || bookingID != 10 {
					t.Fatal("wrong owner or booking passed to cancellation")
				}
				return Booking{ID: bookingID, Status: "Cancelled"}, tc.err
			}
			rec := callAPI(t, db, "POST", "/api/bookings/10/cancel", "{}", true, tc.want)
			if tc.err == nil {
				var booking Booking
				if err := json.Unmarshal(rec.Body.Bytes(), &booking); err != nil || booking.ID != 10 || booking.Status != "Cancelled" {
					t.Fatalf("wrong cancellation response: %s", rec.Body.String())
				}
			}
		})
	}
}

func TestBookingDurationWithDifferentOffsets(t *testing.T) {
	for _, tc := range []struct {
		name, end string
		duration  time.Duration
		valid     bool
	}{
		{"minimum", "2026-11-01T12:30:00+01:00", 30 * time.Minute, true},
		{"just below minimum", "2026-11-01T12:29:59.999999999+01:00", 30*time.Minute - time.Nanosecond, false},
		{"maximum", "2026-11-01T16:00:00+01:00", 4 * time.Hour, true},
		{"just above maximum", "2026-11-01T16:00:00.000000001+01:00", 4*time.Hour + time.Nanosecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, end, fields := validateInterval("2026-11-01T16:00:00+05:00", tc.end, fixedTime())
			wantStart := time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)
			if start != wantStart || end != wantStart.Add(tc.duration) {
				t.Fatalf("unexpected UTC interval: %v to %v", start, end)
			}
			if tc.valid && len(fields) != 0 || !tc.valid && (len(fields) != 1 || fields["ends_at"] == "") {
				t.Fatalf("unexpected validation errors: %v", fields)
			}
		})
	}
}

func TestBookingRejectsClientSuppliedOwner(t *testing.T) {
	db := stubStore{session: employeeSession(t)}
	body := `{"room_id":7,"starts_at":"2026-11-01T11:00:00Z","ends_at":"2026-11-01T12:00:00Z","user_id":999}`
	callAPI(t, db, "POST", "/api/bookings", body, true, 400)
}

func TestExpiredSessionCannotChangeBookings(t *testing.T) {
	for _, path := range []string{"/api/bookings", "/api/bookings/10/cancel"} {
		t.Run(path, func(t *testing.T) {
			db := stubStore{session: func(hash string) (User, error) {
				if hash != tokenHash("test-token") {
					t.Fatal("wrong session checked")
				}
				return User{}, errNotFound
			}}
			body := `{"room_id":7,"starts_at":"2026-11-01T11:00:00Z","ends_at":"2026-11-01T12:00:00Z"}`
			cookies := callAPI(t, db, "POST", path, body, true, 401).Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != sessionCookie || cookies[0].MaxAge >= 0 {
				t.Fatal("expired session cookie was not cleared")
			}
		})
	}
}
