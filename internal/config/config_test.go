// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package config_test

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/muster-io/muster/internal/config"
	"github.com/muster-io/muster/internal/logging"
)

const (
	pgURL = "postgres://muster:s3cret-pw@db.example:5432/muster?sslmode=require"
	key   = "a2V5LW9uZQ=="
)

func base() map[string]string {
	return map[string]string{
		"MUSTER_DATABASE_URL": pgURL,
		"MUSTER_SECRET_KEYS":  key,
		"MUSTER_PUBLIC_URL":   "https://muster.example.org",
	}
}

func environ(vars map[string]string) []string {
	out := make([]string, 0, len(vars))
	for k, v := range vars {
		out = append(out, k+"="+v)
	}
	return out
}

func with(vars map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range vars {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func without(vars map[string]string, names ...string) map[string]string {
	out := with(vars)
	for _, n := range names {
		delete(out, n)
	}
	return out
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func load(t *testing.T, vars map[string]string) config.Config {
	t.Helper()
	c, err := config.Load(environ(vars))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func TestDefaults(t *testing.T) {
	c := load(t, base())
	if c.ListenApp != ":8080" || c.ListenIngest != ":8081" || c.ListenInternal != ":8082" {
		t.Errorf("listen addresses %q %q %q", c.ListenApp, c.ListenIngest, c.ListenInternal)
	}
	if c.LogLevel != logging.LevelInfo || c.MigrateOnStart {
		t.Errorf("log level %v, migrate on start %v", c.LogLevel, c.MigrateOnStart)
	}
	if c.IngestURL.String() != "https://muster.example.org" {
		t.Errorf("ingest URL %v, want the public URL", c.IngestURL)
	}
	if c.RunbookBaseURL.String() != config.DefaultRunbookBaseURL {
		t.Errorf("runbook base URL %v", c.RunbookBaseURL)
	}
	if len(c.TrustedProxies) != 0 || len(c.Conflicts) != 0 {
		t.Errorf("trusted proxies %v, conflicts %v", c.TrustedProxies, c.Conflicts)
	}
	if string(c.Database.URL) != pgURL || c.Database.SSLMode != "require" {
		t.Errorf("database %q %q", c.Database.URL, c.Database.SSLMode)
	}
	if c.Session != c.Database {
		t.Errorf("session %+v, want the main connection %+v", c.Session, c.Database)
	}
	if string(c.SecretKeys) != key {
		t.Errorf("secret keys not read")
	}
}

func TestEverySetting(t *testing.T) {
	c := load(t, with(base(),
		"MUSTER_INGEST_URL", "https://ingest.example.org",
		"MUSTER_LISTEN_APP", "127.0.0.1:9080",
		"MUSTER_LISTEN_INGEST", "[::1]:9081",
		"MUSTER_LISTEN_INTERNAL", "localhost:9082",
		"MUSTER_LOG_LEVEL", "warn",
		"MUSTER_MIGRATE_ON_START", "true",
		"MUSTER_TRUSTED_PROXIES", "10.0.0.0/8, 192.168.1.7/24",
		"MUSTER_RUNBOOK_BASE_URL", "https://docs.example.org/muster",
		"MUSTER_BOOTSTRAP_ADMIN_EMAIL", "admin@example.org",
		"MUSTER_BOOTSTRAP_ADMIN_PASSWORD", "admin-password",
	))
	if c.IngestURL.String() != "https://ingest.example.org" || c.ListenApp != "127.0.0.1:9080" ||
		c.ListenIngest != "[::1]:9081" || c.ListenInternal != "localhost:9082" {
		t.Errorf("config %+v", c)
	}
	if c.LogLevel != logging.LevelWarn || !c.MigrateOnStart {
		t.Errorf("log level %v, migrate on start %v", c.LogLevel, c.MigrateOnStart)
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.1.0/24")}
	if !slices.Equal(c.TrustedProxies, want) {
		t.Errorf("trusted proxies %v, want %v", c.TrustedProxies, want)
	}
	if c.RunbookBaseURL.String() != "https://docs.example.org/muster" || c.BootstrapAdminEmail != "admin@example.org" ||
		string(c.BootstrapAdminPassword) != "admin-password" {
		t.Errorf("config %+v", c)
	}
	if c := load(t, with(base(), "MUSTER_LOG_LEVEL", "error")); c.LogLevel != logging.LevelError {
		t.Errorf("log level %v, want ERROR", c.LogLevel)
	}
}

func TestDatabaseFields(t *testing.T) {
	pw := writeFile(t, "pa ss/word\n")
	c := load(t, with(without(base(), "MUSTER_DATABASE_URL"),
		"MUSTER_DATABASE_HOST", "127.0.0.1",
		"MUSTER_DATABASE_PORT", "55432",
		"MUSTER_DATABASE_NAME", "muster",
		"MUSTER_DATABASE_USER", "muster",
		"MUSTER_DATABASE_PASSWORD_FILE", pw,
		"MUSTER_DATABASE_SSLMODE", "disable",
	))
	want := "postgres://muster:pa%20ss%2Fword@127.0.0.1:55432/muster?sslmode=disable"
	if string(c.Database.URL) != want || c.Database.SSLMode != "disable" {
		t.Errorf("database %q (%s), want %q", c.Database.URL, c.Database.SSLMode, want)
	}
	if len(c.Conflicts) != 0 {
		t.Errorf("conflicts %v", c.Conflicts)
	}

	c = load(t, with(without(base(), "MUSTER_DATABASE_URL"),
		"MUSTER_DATABASE_HOST", "db", "MUSTER_DATABASE_NAME", "m", "MUSTER_DATABASE_USER", "u"))
	if string(c.Database.URL) != "postgres://u@db:5432/m?sslmode=prefer" || c.Database.SSLMode != "prefer" {
		t.Errorf("database without a password %q (%s)", c.Database.URL, c.Database.SSLMode)
	}
}

func TestURLWinsOverFields(t *testing.T) {
	c := load(t, with(base(), "MUSTER_DATABASE_HOST", "127.0.0.1", "MUSTER_DATABASE_SSLMODE", "disable"))
	if string(c.Database.URL) != pgURL {
		t.Errorf("database %q, want the URL", c.Database.URL)
	}
	want := []config.Conflict{{
		Used:    "MUSTER_DATABASE_URL",
		Ignored: []string{"MUSTER_DATABASE_HOST", "MUSTER_DATABASE_SSLMODE"},
	}}
	if len(c.Conflicts) != 1 || c.Conflicts[0].Used != want[0].Used ||
		!slices.Equal(c.Conflicts[0].Ignored, want[0].Ignored) {
		t.Errorf("conflicts %+v, want %+v", c.Conflicts, want)
	}
}

func TestSession(t *testing.T) {
	tests := []struct {
		name          string
		vars          map[string]string
		wantURL       string
		wantSSL       string
		wantConflicts int
	}{
		{
			name:    "session URL",
			vars:    with(base(), "MUSTER_DATABASE_SESSION_URL", "postgres://muster:pw@pg-direct:5432/muster"),
			wantURL: "postgres://muster:pw@pg-direct:5432/muster?sslmode=prefer",
			wantSSL: "prefer",
		},
		{
			name:    "session host and port inherit the rest of the URL",
			vars:    with(base(), "MUSTER_DATABASE_SESSION_HOST", "pg-direct", "MUSTER_DATABASE_SESSION_PORT", "5433"),
			wantURL: "postgres://muster:s3cret-pw@pg-direct:5433/muster?sslmode=require",
			wantSSL: "require",
		},
		{
			name:    "session host keeps the port",
			vars:    with(base(), "MUSTER_DATABASE_SESSION_HOST", "pg-direct"),
			wantURL: "postgres://muster:s3cret-pw@pg-direct:5432/muster?sslmode=require",
			wantSSL: "require",
		},
		{
			name: "session port keeps the host",
			vars: with(base(), "MUSTER_DATABASE_SESSION_PORT", "5433",
				"MUSTER_DATABASE_URL", "postgres://u:p@db/muster"),
			wantURL: "postgres://u:p@db:5433/muster?sslmode=prefer",
			wantSSL: "prefer",
		},
		{
			name:    "session host on a URL without a port",
			vars:    with(base(), "MUSTER_DATABASE_SESSION_HOST", "pg-direct", "MUSTER_DATABASE_URL", "postgres://u:p@db/m"),
			wantURL: "postgres://u:p@pg-direct:5432/m?sslmode=prefer",
			wantSSL: "prefer",
		},
		{
			name: "session host on the fields",
			vars: with(without(base(), "MUSTER_DATABASE_URL"), "MUSTER_DATABASE_HOST", "bouncer",
				"MUSTER_DATABASE_PORT", "6432", "MUSTER_DATABASE_NAME", "m", "MUSTER_DATABASE_USER", "u",
				"MUSTER_DATABASE_PASSWORD", "p", "MUSTER_DATABASE_SESSION_HOST", "pg", "MUSTER_DATABASE_SESSION_PORT", "5432"),
			wantURL: "postgres://u:p@pg:5432/m?sslmode=prefer",
			wantSSL: "prefer",
		},
		{
			name: "session URL wins over host",
			vars: with(base(), "MUSTER_DATABASE_SESSION_URL", "postgres://a@b/c?sslmode=verify-full",
				"MUSTER_DATABASE_SESSION_HOST", "ignored"),
			wantURL:       "postgres://a@b:5432/c?sslmode=verify-full",
			wantSSL:       "verify-full",
			wantConflicts: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := load(t, tt.vars)
			if string(c.Session.URL) != tt.wantURL || c.Session.SSLMode != tt.wantSSL {
				t.Errorf("session %q (%s), want %q (%s)", c.Session.URL, c.Session.SSLMode, tt.wantURL, tt.wantSSL)
			}
			if len(c.Conflicts) != tt.wantConflicts {
				t.Errorf("conflicts %+v", c.Conflicts)
			}
		})
	}
}

func TestURLDefaultsAndIgnoredPasswordFile(t *testing.T) {
	c := load(t, with(base(), "MUSTER_DATABASE_URL", "postgresql://u:p@db/muster",
		"MUSTER_DATABASE_PASSWORD_FILE", filepath.Join(t.TempDir(), "missing")))
	if string(c.Database.URL) != "postgresql://u:p@db:5432/muster?sslmode=prefer" || c.Database.SSLMode != "prefer" {
		t.Errorf("database %q (%s)", c.Database.URL, c.Database.SSLMode)
	}
	if len(c.Conflicts) != 1 || c.Conflicts[0].Ignored[0] != "MUSTER_DATABASE_PASSWORD_FILE" {
		t.Errorf("conflicts %+v", c.Conflicts)
	}
	multi := load(t, with(base(), "MUSTER_DATABASE_URL", "postgres://u:p@db1,db2/muster?sslmode=require"))
	if string(multi.Database.URL) != "postgres://u:p@db1,db2/muster?sslmode=require" {
		t.Errorf("a multi-host URL became %q", multi.Database.URL)
	}
}

func TestSecretFiles(t *testing.T) {
	keys := writeFile(t, "a2V5LXR3bw==\r\n")
	admin := writeFile(t, "admin-pw")
	c := load(t, with(without(base(), "MUSTER_SECRET_KEYS"), "MUSTER_SECRET_KEYS_FILE", keys,
		"MUSTER_BOOTSTRAP_ADMIN_PASSWORD_FILE", admin))
	if string(c.SecretKeys) != "a2V5LXR3bw==" || string(c.BootstrapAdminPassword) != "admin-pw" {
		t.Error("the secrets were not read from their files")
	}
	if c.SecretKeysSource != "MUSTER_SECRET_KEYS_FILE" {
		t.Errorf("SecretKeysSource = %q, want MUSTER_SECRET_KEYS_FILE", c.SecretKeysSource)
	}
}

// The Keyring checks the master keys (internal/keyring): the settings only say which variable holds them.
func TestSecretKeysSource(t *testing.T) {
	for _, tc := range []struct {
		vars map[string]string
		want string
	}{
		{base(), "MUSTER_SECRET_KEYS"},
		{with(base(), "MUSTER_SECRET_KEYS", ""), "MUSTER_SECRET_KEYS"},
		{without(base(), "MUSTER_SECRET_KEYS"), ""},
	} {
		if c := load(t, tc.vars); c.SecretKeysSource != tc.want {
			t.Errorf("SecretKeysSource = %q, want %q", c.SecretKeysSource, tc.want)
		}
	}
}

func TestProxyVariablesIgnored(t *testing.T) {
	plain := load(t, base())
	proxied := load(t, with(base(), "HTTP_PROXY", "http://proxy:3128", "HTTPS_PROXY", "http://proxy:3128",
		"NO_PROXY", "*", "PGHOST", "elsewhere"))
	if plain.Database != proxied.Database || plain.Session != proxied.Session ||
		plain.PublicURL.String() != proxied.PublicURL.String() {
		t.Errorf("non-MUSTER variables changed the configuration")
	}
}

func TestErrors(t *testing.T) {
	missingFile := filepath.Join(t.TempDir(), "missing")
	tests := []struct {
		name string
		vars map[string]string
		want []string
	}{
		{
			name: "invalid port",
			vars: with(without(base(), "MUSTER_DATABASE_URL"), "MUSTER_DATABASE_HOST", "h", "MUSTER_DATABASE_NAME", "n",
				"MUSTER_DATABASE_USER", "u", "MUSTER_DATABASE_PORT", "abc"),
			want: []string{`invalid MUSTER_DATABASE_PORT: "abc" is not a port number`},
		},
		{
			name: "invalid port with the URL",
			vars: with(base(), "MUSTER_DATABASE_PORT", "abc"),
			want: []string{`invalid MUSTER_DATABASE_PORT: "abc" is not a port number`},
		},
		{
			name: "no database",
			vars: without(base(), "MUSTER_DATABASE_URL"),
			want: []string{
				"MUSTER_DATABASE_NAME is required unless MUSTER_DATABASE_URL is set",
				"MUSTER_DATABASE_URL or MUSTER_DATABASE_HOST is required",
				"MUSTER_DATABASE_USER is required unless MUSTER_DATABASE_URL is set",
			},
		},
		{
			name: "missing public URL",
			vars: without(base(), "MUSTER_PUBLIC_URL"),
			want: []string{"MUSTER_PUBLIC_URL is required"},
		},
		{
			name: "empty public URL",
			vars: with(base(), "MUSTER_PUBLIC_URL", ""),
			want: []string{"MUSTER_PUBLIC_URL is set but empty"},
		},
		{
			name: "relative public URL",
			vars: with(base(), "MUSTER_PUBLIC_URL", "muster.example.org"),
			want: []string{`invalid MUSTER_PUBLIC_URL: "muster.example.org" is not an absolute http:// or https:// URL`},
		},
		{
			name: "ftp ingest URL",
			vars: with(base(), "MUSTER_INGEST_URL", "ftp://x"),
			want: []string{`invalid MUSTER_INGEST_URL: "ftp://x" is not an absolute http:// or https:// URL`},
		},
		{
			name: "empty ingest URL",
			vars: with(base(), "MUSTER_INGEST_URL", ""),
			want: []string{"MUSTER_INGEST_URL is set but empty; unset it to use MUSTER_PUBLIC_URL"},
		},
		{
			name: "empty listen address",
			vars: with(base(), "MUSTER_LISTEN_APP", ""),
			want: []string{"MUSTER_LISTEN_APP is set but empty; unset it to use the default"},
		},
		{
			name: "invalid listen addresses",
			vars: with(base(), "MUSTER_LISTEN_INGEST", "8081", "MUSTER_LISTEN_INTERNAL", "bad_host!:1"),
			want: []string{
				`invalid MUSTER_LISTEN_INGEST: "8081" is not a listen address such as :8080 or 127.0.0.1:8080`,
				`invalid MUSTER_LISTEN_INTERNAL: "bad_host!:1" is not a listen address such as :8080 or 127.0.0.1:8080`,
			},
		},
		{
			name: "invalid log level, sslmode and boolean",
			vars: with(without(base(), "MUSTER_DATABASE_URL"), "MUSTER_DATABASE_HOST", "h", "MUSTER_DATABASE_NAME", "n",
				"MUSTER_DATABASE_USER", "u", "MUSTER_LOG_LEVEL", "debug", "MUSTER_DATABASE_SSLMODE", "on",
				"MUSTER_MIGRATE_ON_START", "maybe"),
			want: []string{
				`invalid MUSTER_DATABASE_SSLMODE: "on" is not one of disable, allow, prefer, require, verify-ca, verify-full`,
				`invalid MUSTER_LOG_LEVEL: "debug" is not one of info, warn, error`,
				`invalid MUSTER_MIGRATE_ON_START: "maybe" is not true or false`,
			},
		},
		{
			name: "invalid trusted proxy",
			vars: with(base(), "MUSTER_TRUSTED_PROXIES", "10.0.0.0/8,10.0.0.0/33"),
			want: []string{`invalid MUSTER_TRUSTED_PROXIES: "10.0.0.0/33" is not a CIDR network such as 10.0.0.0/8`},
		},
		{
			name: "invalid email",
			vars: with(base(), "MUSTER_BOOTSTRAP_ADMIN_EMAIL", "admin"),
			want: []string{`invalid MUSTER_BOOTSTRAP_ADMIN_EMAIL: "admin" is not an email address`},
		},
		{
			name: "database URL with another scheme hides the value",
			vars: with(base(), "MUSTER_DATABASE_URL", "mysql://u:hidden-pw@h/db"),
			want: []string{"invalid MUSTER_DATABASE_URL: the value is not a postgres:// or postgresql:// URL"},
		},
		{
			name: "database URL without a user or database",
			vars: with(base(), "MUSTER_DATABASE_URL", "postgres://:hidden-pw@db/", "MUSTER_DATABASE_SESSION_URL", "postgres://db"),
			want: []string{
				"invalid MUSTER_DATABASE_SESSION_URL: the value is not a postgres:// or postgresql:// URL with a host, a user and a database",
				"invalid MUSTER_DATABASE_URL: the value is not a postgres:// or postgresql:// URL with a host, a user and a database",
			},
		},
		{
			name: "session URL and port",
			vars: with(base(), "MUSTER_DATABASE_SESSION_URL", "http://u:hidden-pw@h/db", "MUSTER_DATABASE_SESSION_PORT", "0"),
			want: []string{
				`invalid MUSTER_DATABASE_SESSION_PORT: "0" is not a port number`,
				"invalid MUSTER_DATABASE_SESSION_URL: the value is not a postgres:// or postgresql:// URL",
			},
		},
		{
			name: "secret and file both set",
			vars: with(base(), "MUSTER_SECRET_KEYS_FILE", "/run/keys"),
			want: []string{"MUSTER_SECRET_KEYS and MUSTER_SECRET_KEYS_FILE are both set; set one of them"},
		},
		{
			name: "missing secret file",
			vars: with(without(base(), "MUSTER_SECRET_KEYS"), "MUSTER_SECRET_KEYS_FILE", missingFile),
			want: []string{"read MUSTER_SECRET_KEYS_FILE: open " + missingFile},
		},
		{
			name: "empty secret file variable",
			vars: with(base(), "MUSTER_BOOTSTRAP_ADMIN_PASSWORD_FILE", ""),
			want: []string{"MUSTER_BOOTSTRAP_ADMIN_PASSWORD_FILE is set but empty"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Load(environ(tt.vars))
			if err == nil {
				t.Fatal("Load succeeded")
			}
			lines := strings.Split(err.Error(), "\n")
			if len(lines) != len(tt.want) {
				t.Fatalf("errors:\n%v\nwant %d: %q", err, len(tt.want), tt.want)
			}
			for i, want := range tt.want {
				if !strings.HasPrefix(lines[i], want) {
					t.Errorf("error %d is %q, want %q", i, lines[i], want)
				}
			}
			for _, secret := range []string{"hidden-pw", "s3cret-pw", key} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the error carries a secret: %v", err)
				}
			}
		})
	}
}
