package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, origin, contentType string
		db                                      store
		want                                    int
	}{
		{"page", "GET", "/", "", "", nil, 200},
		{"cross-origin", "POST", "/api/login", "http://other.example", "application/json", nil, 403},
		{"wrong content type", "POST", "/api/login", "", "text/plain", nil, 415},
		{"health", "GET", "/health", "", "", stubStore{}, 200},
		{"health unavailable", "GET", "/health", "", "", stubStore{pingErr: unavailable}, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Content-Type", tc.contentType)
			rec := httptest.NewRecorder()
			newApp(tc.db, fixedTime).routes().ServeHTTP(rec, req)
			if rec.Code != tc.want || rec.Header().Get("X-Request-ID") == "" {
				t.Fatalf("unexpected response: %d %s", rec.Code, rec.Body.String())
			}
			if tc.path == "/" && !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
				t.Fatal("index should be served as HTML")
			}
		})
	}
}

func TestInvalidDatabaseConfig(t *testing.T) {
	if err := run(t.Context(), "postgres://%", ":0"); err == nil {
		t.Fatal("invalid database URL was accepted")
	}
}

func TestServerShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, listener, newApp(stubStore{}, fixedTime).routes())
	}()

	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + listener.Addr().String() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status: %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("server did not stop after context cancellation")
	}
}
