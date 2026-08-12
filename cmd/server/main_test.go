package main

import (
	"testing"

	"github.com/Kusk24/jtrax-backend/internal/db"
)

func TestSeedConfig(t *testing.T) {
	const remote = "libsql://jtrax-demo.turso.io?authToken=secret"

	cases := []struct {
		name     string
		dsn      string
		flag     string
		password string
		wantRun  bool
		wantPw   string
		wantErr  bool
	}{
		{
			name:    "local database seeds itself with the dev password",
			dsn:     "jtrax.db",
			wantRun: true,
			wantPw:  db.DevPassword,
		},
		{
			name:    "remote database does not seed without the opt-in flag",
			dsn:     remote,
			wantRun: false,
		},
		{
			name:    "remote database refuses the published dev password",
			dsn:     remote,
			flag:    "1",
			wantErr: true,
		},
		{
			name:     "remote database seeds with an explicit password",
			dsn:      remote,
			flag:     "1",
			password: "a-real-secret",
			wantRun:  true,
			wantPw:   "a-real-secret",
		},
		{
			name:     "local database honours an explicit password too",
			dsn:      "jtrax.db",
			password: "a-real-secret",
			wantRun:  true,
			wantPw:   "a-real-secret",
		},
		{
			name:    "https dsn counts as remote",
			dsn:     "https://jtrax-demo.turso.io",
			wantRun: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run, pw, err := seedConfig(c.dsn, c.flag, c.password)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if run != c.wantRun {
				t.Errorf("run = %v, want %v", run, c.wantRun)
			}
			if pw != c.wantPw {
				t.Errorf("password = %q, want %q", pw, c.wantPw)
			}
		})
	}
}

func TestRedactHidesTursoToken(t *testing.T) {
	got := db.Redact("libsql://jtrax-demo.turso.io?authToken=super-secret")
	if got != "libsql://jtrax-demo.turso.io?<redacted>" {
		t.Errorf("Redact = %q, still leaks the token", got)
	}
}
