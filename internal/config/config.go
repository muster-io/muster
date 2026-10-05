// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

// Package config reads the bootstrap settings of C-02.FR-1 and FR-2 from the environment (ADR-0010): only MUSTER_*
// variables, read with caarlos0/env and checked with go-playground/validator. A secret can also come from its *_FILE
// variable. An invalid or missing value is an error that names the variable; nothing falls back silently, and an
// error never carries a secret.
package config

import (
	"cmp"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/caarlos0/env/v11"
	"github.com/go-playground/validator/v10"

	"github.com/muster-io/muster/internal/logging"
)

// The defaults of defaults.md: a variable that is not set takes its default; one that is set, even empty, does not.
const (
	DefaultDatabasePort    = "5432"
	DefaultDatabaseSSLMode = "prefer"
	DefaultListenApp       = ":8080"
	DefaultListenIngest    = ":8081"
	DefaultListenInternal  = ":8082"
	DefaultLogLevel        = "info"
	DefaultRunbookBaseURL  = "https://muster-io.github.io/muster"
)

const prefix = "MUSTER_"

// Config is the validated bootstrap configuration.
type Config struct {
	// Database is the main connection, for every query; Session the session-capable connection, for advisory locks
	// and LISTEN.
	Database Database
	Session  Database

	// SecretKeys is the value of MUSTER_SECRET_KEYS or the content of MUSTER_SECRET_KEYS_FILE, and SecretKeysSource
	// the variable that was set, empty when neither was; internal/keyring reads and checks them.
	SecretKeys       logging.Secret
	SecretKeysSource string
	PublicURL        *url.URL
	IngestURL        *url.URL

	ListenApp      string
	ListenIngest   string
	ListenInternal string

	LogLevel       logging.Level
	MigrateOnStart bool
	TrustedProxies []netip.Prefix
	RunbookBaseURL *url.URL

	BootstrapAdminEmail    string
	BootstrapAdminPassword logging.Secret

	// Conflicts are the settings given twice, where one form wins; the caller logs them once the logger exists.
	Conflicts []Conflict
}

// Database is one database connection.
type Database struct {
	// URL is a postgres:// URL with the password; it is never logged.
	URL logging.Secret
	// SSLMode is the sslmode in effect, prefer when the URL names none.
	SSLMode string
}

// Conflict is a setting given both as a URL and as fields: Used wins and Ignored are not read.
type Conflict struct {
	Used    string
	Ignored []string
}

// raw holds the variables as they are written. Fields tagged secret are never repeated in an error.
type raw struct {
	DatabaseURL          string `env:"MUSTER_DATABASE_URL" validate:"omitempty,pgurl" secret:"true"`
	DatabaseHost         string `env:"MUSTER_DATABASE_HOST"`
	DatabasePort         string `env:"MUSTER_DATABASE_PORT" validate:"portnum"`
	DatabaseName         string `env:"MUSTER_DATABASE_NAME"`
	DatabaseUser         string `env:"MUSTER_DATABASE_USER"`
	DatabasePassword     string `env:"MUSTER_DATABASE_PASSWORD" secret:"true"`
	DatabasePasswordFile string `env:"MUSTER_DATABASE_PASSWORD_FILE"`
	DatabaseSSLMode      string `env:"MUSTER_DATABASE_SSLMODE" validate:"oneof=disable allow prefer require verify-ca verify-full"`

	SessionURL  string `env:"MUSTER_DATABASE_SESSION_URL" validate:"omitempty,pgurl" secret:"true"`
	SessionHost string `env:"MUSTER_DATABASE_SESSION_HOST"`
	SessionPort string `env:"MUSTER_DATABASE_SESSION_PORT" validate:"omitempty,portnum"`

	SecretKeys     string `env:"MUSTER_SECRET_KEYS" secret:"true"`
	SecretKeysFile string `env:"MUSTER_SECRET_KEYS_FILE"`

	PublicURL string `env:"MUSTER_PUBLIC_URL" validate:"required,httpurl"`
	IngestURL string `env:"MUSTER_INGEST_URL" validate:"omitempty,httpurl"`

	ListenApp      string `env:"MUSTER_LISTEN_APP" validate:"listen"`
	ListenIngest   string `env:"MUSTER_LISTEN_INGEST" validate:"listen"`
	ListenInternal string `env:"MUSTER_LISTEN_INTERNAL" validate:"listen"`

	LogLevel       string   `env:"MUSTER_LOG_LEVEL" validate:"oneof=info warn error"`
	MigrateOnStart string   `env:"MUSTER_MIGRATE_ON_START" validate:"boolean"`
	TrustedProxies []string `env:"MUSTER_TRUSTED_PROXIES" envSeparator:"," validate:"dive,cidr"`
	RunbookBaseURL string   `env:"MUSTER_RUNBOOK_BASE_URL" validate:"httpurl"`

	BootstrapAdminEmail        string `env:"MUSTER_BOOTSTRAP_ADMIN_EMAIL" validate:"omitempty,email"`
	BootstrapAdminPassword     string `env:"MUSTER_BOOTSTRAP_ADMIN_PASSWORD" secret:"true"`
	BootstrapAdminPasswordFile string `env:"MUSTER_BOOTSTRAP_ADMIN_PASSWORD_FILE"`
}

var defaults = map[string]string{
	"MUSTER_DATABASE_PORT":    DefaultDatabasePort,
	"MUSTER_DATABASE_SSLMODE": DefaultDatabaseSSLMode,
	"MUSTER_LISTEN_APP":       DefaultListenApp,
	"MUSTER_LISTEN_INGEST":    DefaultListenIngest,
	"MUSTER_LISTEN_INTERNAL":  DefaultListenInternal,
	"MUSTER_LOG_LEVEL":        DefaultLogLevel,
	"MUSTER_MIGRATE_ON_START": "false",
	"MUSTER_RUNBOOK_BASE_URL": DefaultRunbookBaseURL,
}

// databaseFields are the fields that MUSTER_DATABASE_URL replaces.
var databaseFields = []string{
	"MUSTER_DATABASE_HOST", "MUSTER_DATABASE_PORT", "MUSTER_DATABASE_NAME", "MUSTER_DATABASE_USER",
	"MUSTER_DATABASE_PASSWORD", "MUSTER_DATABASE_PASSWORD_FILE", "MUSTER_DATABASE_SSLMODE",
}

var sessionFields = []string{"MUSTER_DATABASE_SESSION_HOST", "MUSTER_DATABASE_SESSION_PORT"}

// secretFile pairs a secret with its *_FILE variable.
type secretFile struct {
	name  string
	field func(*raw) (value, file *string)
}

var secretFiles = []secretFile{
	{"MUSTER_DATABASE_PASSWORD", func(r *raw) (*string, *string) { return &r.DatabasePassword, &r.DatabasePasswordFile }},
	{"MUSTER_SECRET_KEYS", func(r *raw) (*string, *string) { return &r.SecretKeys, &r.SecretKeysFile }},
	{"MUSTER_BOOTSTRAP_ADMIN_PASSWORD", func(r *raw) (*string, *string) {
		return &r.BootstrapAdminPassword, &r.BootstrapAdminPasswordFile
	}},
}

var messages = map[string]string{
	"portnum": "is not a port number",
	"pgurl":   "is not a postgres:// or postgresql:// URL with a host, a user and a database",
	"httpurl": "is not an absolute http:// or https:// URL",
	"listen":  "is not a listen address such as :8080 or 127.0.0.1:8080",
	"boolean": "is not true or false",
	"cidr":    "is not a CIDR network such as 10.0.0.0/8",
	"email":   "is not an email address",
}

// Load reads the configuration from environ, in the form of os.Environ. It reads no variable outside MUSTER_*; the
// error joins one error per invalid variable.
func Load(environ []string) (Config, error) {
	vars := map[string]string{}
	for _, kv := range environ {
		if name, value, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(name, prefix) {
			vars[name] = value
		}
	}
	var errs []error
	for name, value := range defaults {
		if v, ok := vars[name]; ok && v == "" {
			errs = append(errs, fmt.Errorf("%s is set but empty; unset it to use the default", name))
			vars[name] = value // reported once
		}
	}
	if v, ok := vars["MUSTER_INGEST_URL"]; ok && v == "" {
		errs = append(errs, errors.New("MUSTER_INGEST_URL is set but empty; unset it to use MUSTER_PUBLIC_URL"))
	}
	conflicts := findConflicts(vars)
	for name, value := range defaults {
		if _, ok := vars[name]; !ok {
			vars[name] = value
		}
	}

	var r raw
	if err := env.ParseWithOptions(&r, env.Options{Environment: vars}); err != nil {
		return Config{}, fmt.Errorf("read the environment: %w", err)
	}
	for i, p := range r.TrustedProxies {
		r.TrustedProxies[i] = strings.TrimSpace(p)
	}
	unreadable := map[string]bool{}
	for _, s := range secretFiles {
		if s.name == "MUSTER_DATABASE_PASSWORD" && r.DatabaseURL != "" {
			continue // ignored in favour of the URL, so its file is not read
		}
		value, file := s.field(&r)
		if err := readSecretFile(vars, s.name, value, file); err != nil {
			errs = append(errs, err)
			unreadable[s.name] = true
		}
	}
	errs = append(errs, check(newValidator(), &r, vars, unreadable)...)
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	c, err := build(&r, conflicts)
	if err != nil {
		return Config{}, err
	}
	for _, name := range []string{"MUSTER_SECRET_KEYS", "MUSTER_SECRET_KEYS_FILE"} {
		if _, ok := vars[name]; ok {
			c.SecretKeysSource = name
		}
	}
	return c, nil
}

func findConflicts(vars map[string]string) []Conflict {
	var conflicts []Conflict
	for _, c := range []Conflict{
		{Used: "MUSTER_DATABASE_URL", Ignored: databaseFields},
		{Used: "MUSTER_DATABASE_SESSION_URL", Ignored: sessionFields},
	} {
		if _, ok := vars[c.Used]; !ok {
			continue
		}
		var ignored []string
		for _, name := range c.Ignored {
			if _, ok := vars[name]; ok {
				ignored = append(ignored, name)
			}
		}
		if len(ignored) > 0 {
			conflicts = append(conflicts, Conflict{Used: c.Used, Ignored: ignored})
		}
	}
	return conflicts
}

// readSecretFile fills a secret from its *_FILE variable; one trailing line break is dropped.
func readSecretFile(vars map[string]string, name string, value, file *string) error {
	fileVar := name + "_FILE"
	if _, ok := vars[fileVar]; !ok {
		return nil
	}
	if _, ok := vars[name]; ok {
		return fmt.Errorf("%s and %s are both set; set one of them", name, fileVar)
	}
	if *file == "" {
		return fmt.Errorf("%s is set but empty", fileVar)
	}
	b, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("read %s: %w", fileVar, err)
	}
	*value = strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	return nil
}

func newValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	v.RegisterTagNameFunc(func(f reflect.StructField) string {
		name, _, _ := strings.Cut(f.Tag.Get("env"), ",")
		return name
	})
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(v.RegisterValidation("portnum", func(fl validator.FieldLevel) bool { return isPort(fl.Field().String()) }))
	// A database URL names its host, user and database itself, so that the PG* variables pgx would otherwise read
	// never fill them in.
	must(v.RegisterValidation("pgurl", func(fl validator.FieldLevel) bool {
		u, err := url.Parse(fl.Field().String())
		return err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") && u.Hostname() != "" &&
			u.User.Username() != "" && strings.Trim(u.Path, "/") != ""
	}))
	must(v.RegisterValidation("httpurl", func(fl validator.FieldLevel) bool {
		u, err := url.Parse(fl.Field().String())
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
	}))
	must(v.RegisterValidation("listen", func(fl validator.FieldLevel) bool {
		host, port, err := net.SplitHostPort(fl.Field().String())
		if n, perr := strconv.Atoi(port); err != nil || perr != nil || n < 0 || n > 65535 {
			return false // port 0 picks a free port
		}
		if _, err := netip.ParseAddr(host); host == "" || err == nil {
			return true
		}
		return hostnameRe.MatchString(host)
	}))
	return v
}

var (
	hostnameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`)
	indexRe    = regexp.MustCompile(`\[\d+\]$`)
)

func isPort(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1 && n <= 65535
}

// check validates r; a secret whose file could not be read is not reported again.
func check(v *validator.Validate, r *raw, vars map[string]string, unreadable map[string]bool) []error {
	var errs []error
	if err := v.Struct(r); err != nil {
		var verrs validator.ValidationErrors
		if !errors.As(err, &verrs) {
			return []error{err}
		}
		secrets := secretNames()
		for _, fe := range verrs {
			name := indexRe.ReplaceAllString(fe.Field(), "")
			if unreadable[name] {
				continue
			}
			errs = append(errs, fieldError(name, fe, secrets[name], vars))
		}
	}
	if r.DatabaseURL == "" {
		if r.DatabaseHost == "" {
			errs = append(errs, errors.New("MUSTER_DATABASE_URL or MUSTER_DATABASE_HOST is required"))
		}
		for name, value := range map[string]string{
			"MUSTER_DATABASE_NAME": r.DatabaseName,
			"MUSTER_DATABASE_USER": r.DatabaseUser,
		} {
			if value == "" {
				errs = append(errs, fmt.Errorf("%s is required unless MUSTER_DATABASE_URL is set", name))
			}
		}
	}
	slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
	return errs
}

func fieldError(name string, fe validator.FieldError, secret bool, vars map[string]string) error {
	if fe.Tag() == "required" {
		if _, ok := vars[name]; ok {
			return fmt.Errorf("%s is set but empty", name)
		}
		return fmt.Errorf("%s is required", name)
	}
	msg, ok := messages[fe.Tag()]
	if !ok && fe.Tag() == "oneof" {
		msg = "is not one of " + strings.Join(strings.Fields(fe.Param()), ", ")
	} else if !ok {
		msg = "is not valid (" + fe.Tag() + ")"
	}
	if secret {
		return fmt.Errorf("invalid %s: the value %s", name, msg)
	}
	return fmt.Errorf("invalid %s: %q %s", name, fmt.Sprint(fe.Value()), msg)
}

func secretNames() map[string]bool {
	names := map[string]bool{}
	t := reflect.TypeFor[raw]()
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Tag.Get("secret") == "true" {
			name, _, _ := strings.Cut(f.Tag.Get("env"), ",")
			names[name] = true
		}
	}
	return names
}

func build(r *raw, conflicts []Conflict) (Config, error) {
	c := Config{
		SecretKeys:             logging.Secret(r.SecretKeys),
		ListenApp:              r.ListenApp,
		ListenIngest:           r.ListenIngest,
		ListenInternal:         r.ListenInternal,
		BootstrapAdminEmail:    r.BootstrapAdminEmail,
		BootstrapAdminPassword: logging.Secret(r.BootstrapAdminPassword),
		Conflicts:              conflicts,
	}
	var err error
	if c.PublicURL, err = url.Parse(r.PublicURL); err != nil {
		return Config{}, fmt.Errorf("invalid MUSTER_PUBLIC_URL: %w", err)
	}
	c.IngestURL = c.PublicURL
	if r.IngestURL != "" {
		if c.IngestURL, err = url.Parse(r.IngestURL); err != nil {
			return Config{}, fmt.Errorf("invalid MUSTER_INGEST_URL: %w", err)
		}
	}
	if c.RunbookBaseURL, err = url.Parse(r.RunbookBaseURL); err != nil {
		return Config{}, fmt.Errorf("invalid MUSTER_RUNBOOK_BASE_URL: %w", err)
	}
	c.MigrateOnStart, _ = strconv.ParseBool(r.MigrateOnStart) // checked by the boolean tag
	switch r.LogLevel {
	case "warn":
		c.LogLevel = logging.LevelWarn
	case "error":
		c.LogLevel = logging.LevelError
	default:
		c.LogLevel = logging.LevelInfo
	}
	for _, p := range r.TrustedProxies {
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			return Config{}, fmt.Errorf("invalid MUSTER_TRUSTED_PROXIES: %q is not a CIDR network such as 10.0.0.0/8", p)
		}
		c.TrustedProxies = append(c.TrustedProxies, prefix.Masked())
	}

	main, err := mainDatabase(r)
	if err != nil {
		return Config{}, err
	}
	withSSLMode(main)
	c.Database = Database{URL: logging.Secret(main.String()), SSLMode: sslMode(main)}
	session := main
	if r.SessionURL != "" {
		if session, err = url.Parse(r.SessionURL); err != nil {
			return Config{}, errors.New("invalid MUSTER_DATABASE_SESSION_URL: the value is not a URL")
		}
	} else if r.SessionHost != "" || r.SessionPort != "" {
		copied := *main
		session = &copied
		host := cmp.Or(r.SessionHost, main.Hostname())
		if port := cmp.Or(r.SessionPort, main.Port()); port != "" {
			session.Host = net.JoinHostPort(host, port)
		} else {
			session.Host = host
		}
	}
	withSSLMode(session)
	c.Session = Database{URL: logging.Secret(session.String()), SSLMode: sslMode(session)}
	return c, nil
}

func mainDatabase(r *raw) (*url.URL, error) {
	if r.DatabaseURL != "" {
		u, err := url.Parse(r.DatabaseURL)
		if err != nil {
			return nil, errors.New("invalid MUSTER_DATABASE_URL: the value is not a URL")
		}
		return u, nil
	}
	u := &url.URL{
		Scheme:   "postgres",
		Host:     net.JoinHostPort(r.DatabaseHost, r.DatabasePort),
		Path:     "/" + r.DatabaseName,
		RawQuery: url.Values{"sslmode": {r.DatabaseSSLMode}}.Encode(),
	}
	if r.DatabasePassword != "" {
		u.User = url.UserPassword(r.DatabaseUser, r.DatabasePassword)
	} else {
		u.User = url.User(r.DatabaseUser)
	}
	return u, nil
}

func sslMode(u *url.URL) string {
	return cmp.Or(u.Query().Get("sslmode"), DefaultDatabaseSSLMode)
}

// withSSLMode writes the default port and sslmode into a URL that names none, so that the mode in effect is the one
// reported and PGPORT and PGSSLMODE in the environment change nothing.
func withSSLMode(u *url.URL) {
	if u.Port() == "" && !strings.Contains(u.Host, ",") {
		u.Host = net.JoinHostPort(u.Hostname(), DefaultDatabasePort)
	}
	q := u.Query()
	if q.Get("sslmode") == "" {
		q.Set("sslmode", DefaultDatabaseSSLMode)
		u.RawQuery = q.Encode()
	}
}
