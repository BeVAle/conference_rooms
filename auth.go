package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

type User struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

const sessionCookie = "conference_session"

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=65536,t=3,p=2" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func tokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func cookie(value string, expires time.Time) *http.Cookie {
	return &http.Cookie{Name: sessionCookie, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expires}
}

func clearCookie(w http.ResponseWriter) {
	c := cookie("", time.Unix(1, 0))
	c.MaxAge = -1
	http.SetCookie(w, c)
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	fields := map[string]string{}
	if input.Email == "" || !strings.Contains(input.Email, "@") || len(input.Email) > 254 {
		fields["email"] = "Введите электронную почту."
	}
	if input.Password == "" || len(input.Password) > 256 {
		fields["password"] = "Введите пароль длиной до 256 символов."
	}
	if len(fields) > 0 {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "Проверьте поля.", Fields: fields})
		return
	}
	user, hash, err := a.db.UserByEmail(r.Context(), input.Email)
	if err != nil && !errors.Is(err, errNotFound) {
		a.dbError(w, r, err)
		return
	}
	if errors.Is(err, errNotFound) || !verifyPassword(input.Password, hash) {
		audit(r, "login", "invalid_credentials")
		fail(w, http.StatusUnauthorized, "Почта или пароль неверны.")
		return
	}
	token := rand.Text() + rand.Text()
	expires := a.now().Add(8 * time.Hour)
	var oldHash string
	if old, err := r.Cookie(sessionCookie); err == nil {
		oldHash = tokenHash(old.Value)
	}
	if err := a.db.StartSession(r.Context(), oldHash, tokenHash(token), user.ID, expires); err != nil {
		a.dbError(w, r, err)
		return
	}
	http.SetCookie(w, cookie(token, expires))
	audit(r, "login", "success")
	writeJSON(w, http.StatusOK, user)
}

func (a *app) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			clearCookie(w)
			fail(w, http.StatusUnauthorized, "Войдите в систему.")
			return
		}
		user, err := a.db.SessionUser(r.Context(), tokenHash(c.Value))
		if errors.Is(err, errNotFound) {
			clearCookie(w)
			fail(w, http.StatusUnauthorized, "Сессия истекла. Войдите снова.")
			return
		}
		if err != nil {
			a.dbError(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
	}
}

func (a *app) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, r.Context().Value(userKey))
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := a.db.EndSession(r.Context(), tokenHash(c.Value)); err != nil {
			a.dbError(w, r, err)
			return
		}
	}
	clearCookie(w)
	audit(r, "logout", "success")
	writeJSON(w, http.StatusOK, map[string]string{"message": "Вы вышли из системы."})
}
