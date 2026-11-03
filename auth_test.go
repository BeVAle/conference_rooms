package main

import (
	"strings"
	"testing"
	"time"
)

const loginBody = `{"email":" USER@example.com ","password":"conference123"}`
const demoHash = "$argon2id$v=19$m=65536,t=3,p=2$4LroozmH65DuZZPCvVc6Ug$Ku4gckbJm9Q+ILRYdzVVeR3wSXAlegnv8Zlvjv0KuP0"

func TestPassword(t *testing.T) {
	if !verifyPassword("conference123", demoHash) || verifyPassword("wrong", demoHash) || verifyPassword("", "malformed") {
		t.Fatal("password verification failed")
	}
}

func TestLogin(t *testing.T) {
	for _, tc := range []struct {
		name, body          string
		lookupErr, writeErr error
		want                int
	}{
		{"malformed JSON", "{", nil, nil, 400},
		{"multiple objects", loginBody + "{}", nil, nil, 400},
		{"missing credentials", "{}", nil, nil, 400},
		{"wrong password", `{"email":"user@example.com","password":"wrong"}`, nil, nil, 401},
		{"unknown account", loginBody, errNotFound, nil, 401},
		{"lookup failed", loginBody, unavailable, nil, 503},
		{"session write failed", loginBody, nil, unavailable, 503},
		{"success", loginBody, nil, nil, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var storedHash string
			db := stubStore{
				user: func(email string) (User, string, error) {
					if tc.want == 400 || email != employee.Email {
						t.Fatalf("unexpected lookup for %q", email)
					}
					return employee, demoHash, tc.lookupErr
				},
				start: func(oldHash, hash string, id int64, expires time.Time) error {
					if tc.want != 200 && tc.writeErr == nil {
						t.Fatal("rejected credentials created a session")
					}
					if oldHash != tokenHash("test-token") || id != employee.ID || !expires.Equal(fixedTime().Add(8*time.Hour)) {
						t.Fatal("incorrect session parameters")
					}
					storedHash = hash
					return tc.writeErr
				},
			}
			rec := callAPI(t, db, "POST", "/api/login", tc.body, true, tc.want)
			cookies := rec.Result().Cookies()
			if tc.want == 200 {
				if len(cookies) != 1 || cookies[0].Name != sessionCookie || !cookies[0].HttpOnly || cookies[0].Value == "" || tokenHash(cookies[0].Value) != storedHash {
					t.Fatal("session cookie does not match persisted token hash")
				}
			} else if len(cookies) != 0 {
				t.Fatal("failed login issued a session cookie")
			}
		})
	}
}

func TestSessions(t *testing.T) {
	callAPI(t, stubStore{}, "GET", "/api/me", "", false, 401)
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"valid", nil, 200},
		{"expired", errNotFound, 401},
		{"unavailable", unavailable, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := stubStore{session: func(hash string) (User, error) {
				return employeeSession(t)(hash)
			}}
			if tc.err != nil {
				db.session = func(string) (User, error) { return User{}, tc.err }
			}
			callAPI(t, db, "GET", "/api/me", "", true, tc.want)
		})
	}
}

func TestLogout(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{{nil, 200}, {unavailable, 503}} {
		db := stubStore{end: func(hash string) error {
			if hash != tokenHash("test-token") {
				t.Fatal("wrong session removed")
			}
			return tc.err
		}}
		cookies := callAPI(t, db, "POST", "/api/logout", "{}", true, tc.want).Result().Cookies()
		if tc.err == nil && (len(cookies) != 1 || cookies[0].MaxAge >= 0) {
			t.Fatal("session cookie was not cleared")
		}
		if tc.err != nil && len(cookies) != 0 {
			t.Fatal("failed logout changed the cookie")
		}
	}
}

func TestPasswordRejectsInvalidStoredHash(t *testing.T) {
	parts := strings.Split(demoHash, "$")
	for _, tc := range []struct {
		name  string
		index int
		value string
	}{
		{"invalid salt encoding", 4, "!"},
		{"short salt", 4, "AA"},
		{"invalid digest encoding", 5, "!"},
		{"short digest", 5, "AA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := append([]string(nil), parts...)
			broken[tc.index] = tc.value
			if verifyPassword("conference123", strings.Join(broken, "$")) {
				t.Fatal("invalid stored hash accepted")
			}
		})
	}
}

func TestFirstLogin(t *testing.T) {
	var savedHash string
	db := stubStore{
		user: func(email string) (User, string, error) {
			if email != employee.Email {
				t.Fatalf("unexpected email: %q", email)
			}
			return employee, demoHash, nil
		},
		start: func(oldHash, hash string, userID int64, expires time.Time) error {
			if oldHash != "" || userID != employee.ID || !expires.Equal(fixedTime().Add(8*time.Hour)) {
				t.Fatal("incorrect first session parameters")
			}
			savedHash = hash
			return nil
		},
	}
	cookies := callAPI(t, db, "POST", "/api/login", loginBody, false, 200).Result().Cookies()
	if len(cookies) != 1 || tokenHash(cookies[0].Value) != savedHash || !cookies[0].Expires.Equal(fixedTime().Add(8*time.Hour)) {
		t.Fatal("first session cookie differs from stored session")
	}
}

func TestLogoutWithoutSession(t *testing.T) {
	cookies := callAPI(t, stubStore{}, "POST", "/api/logout", "{}", false, 200).Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie || cookies[0].MaxAge >= 0 {
		t.Fatal("logout did not clear the session cookie")
	}
}
