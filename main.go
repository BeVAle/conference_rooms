package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed index.html migrations/*.sql
var files embed.FS

type store interface {
	Ping(context.Context) error
	UserByEmail(context.Context, string) (User, string, error)
	SessionUser(context.Context, string) (User, error)
	StartSession(context.Context, string, string, int64, time.Time) error
	EndSession(context.Context, string) error
	Rooms(context.Context, time.Time, time.Time) ([]Room, error)
	Bookings(context.Context, int64) ([]Booking, error)
	CreateBooking(context.Context, int64, int64, time.Time, time.Time) (Booking, error)
	CancelBooking(context.Context, int64, int64) (Booking, error)
}

type app struct {
	db  store
	now func() time.Time
}

func newApp(db store, now func() time.Time) *app {
	return &app{db: db, now: now}
}

type contextKey int

const (
	requestIDKey contextKey = iota
	userKey
)

type apiError struct {
	Error  string            `json:"error"`
	Fields map[string]string `json:"fields,omitempty"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dsn := env("DATABASE_URL", "postgres://conference:conference@localhost:5432/conference?sslmode=disable")
	if err := run(ctx, dsn, env("LISTEN_ADDR", ":8080")); err != nil {
		slog.Error("server", "result", "failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, dsn, address string) error {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return errors.New("некорректный DATABASE_URL")
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	cfg.ConnConfig.ConnectTimeout = 3 * time.Second
	cfg.MaxConns = 10
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return errors.New("не удалось создать пул БД")
	}
	defer db.Close()
	startupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = db.Ping(startupCtx); err != nil {
		return errors.New("БД недоступна")
	}
	if err = migrate(startupCtx, db); err != nil {
		return err
	}
	cancel()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	return serve(ctx, listener, newApp(postgresStore{db}, time.Now).routes())
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	slog.Info("server", "result", "started", "address", listener.Addr().String())
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		return srv.Shutdown(shutdownCtx)
	}
	return nil
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		page, _ := files.ReadFile("index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if err := a.db.Ping(r.Context()); err != nil {
			a.dbError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", a.logout)
	mux.HandleFunc("GET /api/me", a.authorize(a.me))
	mux.HandleFunc("GET /api/rooms", a.authorize(a.rooms))
	mux.HandleFunc("GET /api/bookings", a.authorize(a.bookings))
	mux.HandleFunc("POST /api/bookings", a.authorize(a.createBooking))
	mux.HandleFunc("POST /api/bookings/{id}/cancel", a.authorize(a.cancelBooking))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		id := rand.Text()
		r = r.WithContext(context.WithValue(ctx, requestIDKey, id))
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Method == http.MethodPost {
			if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
				fail(w, http.StatusForbidden, "Запрос с другого сайта запрещён.")
				return
			}
			if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
				fail(w, http.StatusUnsupportedMediaType, "Ожидается JSON.")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		fail(w, http.StatusBadRequest, "Проверьте формат запроса.")
		return false
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		fail(w, http.StatusBadRequest, "Ожидается один JSON-объект.")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, apiError{Error: message})
}

func audit(r *http.Request, operation, result string) {
	slog.Info(operation, "request_id", r.Context().Value(requestIDKey), "operation", operation, "result", result)
}

func (a *app) dbError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("database", "request_id", r.Context().Value(requestIDKey), "operation", "database", "result", "failed", "type", errorCode(err))
	fail(w, http.StatusServiceUnavailable, "База данных недоступна. Повторите попытку позже.")
}

func env(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}
