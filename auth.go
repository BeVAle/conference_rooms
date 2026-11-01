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

	"github.com/jackc/pgx/v5"
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
	var user User
	var hash string
	err := a.db.QueryRow(r.Context(), "SELECT id, name, email, password_hash FROM users WHERE email=$1", input.Email).Scan(&user.ID, &user.Name, &user.Email, &hash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		a.dbError(w, r, err)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) || !verifyPassword(input.Password, hash) {
		audit(r, "login", "invalid_credentials")
		fail(w, http.StatusUnauthorized, "Почта или пароль неверны.")
		return
	}
	token := rand.Text() + rand.Text()
	expires := time.Now().Add(8 * time.Hour)
	tx, err := a.db.Begin(r.Context())
	if err != nil {
		a.dbError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	// При повторном входе текущая сессия также завершается.
	if old, cookieErr := r.Cookie(sessionCookie); cookieErr == nil {
		if _, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(old.Value)); err != nil {
			a.dbError(w, r, err)
			return
		}
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE expires_at <= now()"); err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES ($1,$2,$3)", tokenHash(token), user.ID, expires)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
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
		var user User
		err = a.db.QueryRow(r.Context(), `SELECT u.id,u.name,u.email FROM sessions s JOIN users u ON u.id=s.user_id
			WHERE s.token_hash=$1 AND s.expires_at>clock_timestamp()`, tokenHash(c.Value)).Scan(&user.ID, &user.Name, &user.Email)
		if errors.Is(err, pgx.ErrNoRows) {
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
		if _, err = a.db.Exec(r.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(c.Value)); err != nil {
			a.dbError(w, r, err)
			return
		}
	}
	clearCookie(w)
	audit(r, "logout", "success")
	writeJSON(w, http.StatusOK, map[string]string{"message": "Вы вышли из системы."})
}
