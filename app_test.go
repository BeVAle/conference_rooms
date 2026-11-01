package main

import (
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

func TestPassword(t *testing.T) {
	const hash = "$argon2id$v=19$m=65536,t=3,p=2$4LroozmH65DuZZPCvVc6Ug$Ku4gckbJm9Q+ILRYdzVVeR3wSXAlegnv8Zlvjv0KuP0"
	if !verifyPassword("conference123", hash) || verifyPassword("wrong", hash) || verifyPassword("", "malformed") {
		t.Fatal("password verification failed")
	}
}
