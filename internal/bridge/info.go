// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/muster-io/muster/internal/bridge/gen"
	"github.com/muster-io/muster/internal/clock"
	"github.com/muster-io/muster/internal/logging"
	"github.com/muster-io/muster/internal/outbound"
)

// InfoMaxBytes is the largest info Muster reads (C-22.FR-2); a larger one does not match the protocol.
const InfoMaxBytes = 64 << 10

// DefaultInfoMaxAge is bridge.info_max_age: a call that depends on info reads it again first when Muster's copy is
// older.
const DefaultInfoMaxAge = 10 * time.Minute

// The check codes of a Bridge Connection check (ConnectionCheckStep.code) that the protocol names.
const (
	CodeTokenInvalid            = "token_invalid"
	CodeProtocolVersionUnknown  = "protocol_version_unknown"
	CodeProtocolMismatch        = "protocol_mismatch"
	CodeEventDeliveryNotOffered = "event_delivery_not_offered"
)

// MessageTokenInvalid is the message of an info the bridge refused with 401.
const MessageTokenInvalid = "The bridge refused the token." //nolint:gosec // G101: a message, not a credential

const (
	messageVersion    = "The bridge speaks protocol version %s; this Muster speaks " + ProtocolVersion + "."
	messageMismatch   = "The bridge's answer does not match the protocol: "
	messageNotOffered = "The bridge does not offer %s event delivery."
	// maxVersionRunes is how much of an unknown protocol_version a message shows.
	maxVersionRunes = 32
)

// Info is the info of a bridge: the fields Muster reads, and Raw, the whole answer as the bridge sent it, unknown
// fields included, which is what Muster stores.
type Info struct {
	gen.Info
	Raw json.RawMessage `json:"-"`
}

// Features are the names of the optional features the info declares, sorted.
func (i Info) Features() []string {
	c := i.Capabilities
	present := map[string]bool{
		"buttons":         c.Buttons != nil,
		"thread_messages": c.ThreadMessages != nil,
		"direct_messages": c.DirectMessages != nil,
		"private_answers": c.PrivateAnswers != nil && len(*c.PrivateAnswers) > 0,
		"native_mentions": c.NativeMentions != nil && *c.NativeMentions,
		"quiet_messages":  c.QuietMessages != nil && *c.QuietMessages,
		"status_color":    c.StatusColor != nil && *c.StatusColor,
		"delete":          c.Delete != nil,
		"deletion_events": c.DeletionEvents != nil && *c.DeletionEvents,
	}
	out := []string{}
	for _, name := range slices.Sorted(maps.Keys(present)) {
		if present[name] {
			out = append(out, name)
		}
	}
	return out
}

// InfoError is an info that Muster cannot use: its check code and its message, in English. version is the unknown
// protocol_version the message shows, so that a client masks it before it is cut.
type InfoError struct {
	Code    string
	Message string
	version string
}

func (e *InfoError) Error() string { return e.Message }

func mismatch(format string, args ...any) *InfoError {
	return &InfoError{Code: CodeProtocolMismatch, Message: messageMismatch + fmt.Sprintf(format, args...)}
}

func versionUnknown(version string) *InfoError {
	return &InfoError{Code: CodeProtocolVersionUnknown, Message: fmt.Sprintf(messageVersion, cut(version)),
		version: version}
}

// ParseInfo reads the answer of getInfo (C-22.FR-2). It refuses an answer larger than InfoMaxBytes before it parses
// it, and one that Muster could not store: not UTF-8, or with an escape that PostgreSQL's jsonb refuses. It reads
// protocol_version before it validates the rest, so that a bridge of another version fails with its version named
// rather than with a schema error. The error is an *InfoError.
func ParseInfo(body []byte) (Info, error) {
	if len(body) > InfoMaxBytes {
		return Info{}, mismatch("info is larger than 64 KiB")
	}
	if !utf8.Valid(body) {
		return Info{}, mismatch("info is not valid UTF-8")
	}
	top, ok := object(body)
	if !ok {
		return Info{}, mismatch("info is not a JSON object")
	}
	if problem := unstorableEscape(body); problem != "" {
		return Info{}, mismatch("info contains %s", problem)
	}
	raw, ok := top["protocol_version"]
	if !ok {
		return Info{}, mismatch("protocol_version is missing")
	}
	var version string
	if isNull(raw) || json.Unmarshal(raw, &version) != nil {
		return Info{}, mismatch("protocol_version is not a string")
	}
	if version != ProtocolVersion {
		return Info{}, versionUnknown(version)
	}
	if problem := infoShape.check("", top); problem != "" {
		return Info{}, mismatch("%s", problem)
	}
	var i Info
	if err := json.Unmarshal(body, &i.Info); err != nil {
		if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok && te.Field != "" {
			return Info{}, mismatch("%s has the wrong type", te.Field)
		}
		return Info{}, mismatch("info is not valid JSON")
	}
	if problem := validateInfo(i.Info); problem != "" {
		return Info{}, mismatch("%s", problem)
	}
	i.Raw = append(json.RawMessage(nil), body...)
	return i, nil
}

// DecodeInfo reads a stored copy of info, which ParseInfo accepted when it was read.
func DecodeInfo(raw []byte) (Info, error) {
	var i Info
	if err := json.Unmarshal(raw, &i.Info); err != nil {
		return Info{}, fmt.Errorf("decode the stored info: %w", err)
	}
	i.Raw = append(json.RawMessage(nil), raw...)
	return i, nil
}

// cut is an unknown protocol_version as a message shows it: valid UTF-8, at most maxVersionRunes runes.
func cut(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if utf8.RuneCountInString(s) <= maxVersionRunes {
		return s
	}
	return string([]rune(s)[:maxVersionRunes]) + "…"
}

// unstorableEscape is the first escape in a string of the valid JSON body that jsonb refuses — \u0000 and a UTF-16
// surrogate that is not paired — "" when there is none.
func unstorableEscape(body []byte) string {
	const lone = "an unpaired UTF-16 surrogate escape"
	in := false
	for i := 0; i < len(body); i++ {
		switch {
		case body[i] == '"':
			in = !in
		case !in || body[i] != '\\':
		case body[i+1] != 'u':
			i++
		default:
			r := hex4(body[i+2 : i+6])
			i += 5
			switch {
			case r == 0:
				return `the escape \u0000`
			case r >= 0xDC00 && r <= 0xDFFF:
				return lone
			case r >= 0xD800 && r <= 0xDBFF:
				if i+6 >= len(body) || body[i+1] != '\\' || body[i+2] != 'u' {
					return lone
				}
				if low := hex4(body[i+3 : i+7]); low < 0xDC00 || low > 0xDFFF {
					return lone
				}
				i += 6
			}
		}
	}
	return ""
}

// hex4 is the code unit of the four hex digits of a \u escape.
func hex4(b []byte) uint64 {
	n, _ := strconv.ParseUint(string(b), 16, 16)
	return n
}

func object(raw []byte) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if isNull(raw) || json.Unmarshal(raw, &m) != nil {
		return nil, false
	}
	return m, true
}

func isNull(raw []byte) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// shape is what Go's decoding of an object of info does not check: its required members, and the members that may not
// be null, because a null decodes as a missing member.
type shape struct {
	required []string
	optional []string
	nullable []string
	nested   map[string]shape
}

// infoShape is the shape of Info in api/bridge.yaml.
var infoShape = shape{
	required: []string{"protocol_version", "bridge", "messenger", "capabilities", "limits", "user_lookup",
		"event_delivery"},
	optional: []string{"link_url_template"},
	nullable: []string{"link_url_template"},
	nested: map[string]shape{
		"bridge":    {required: []string{"name", "version"}},
		"messenger": {required: []string{"name"}},
		"capabilities": {
			optional: []string{"buttons", "thread_messages", "direct_messages", "private_answers", "native_mentions",
				"quiet_messages", "status_color", "delete", "deletion_events"},
			nested: map[string]shape{
				"buttons":         {required: []string{"max_per_message", "max_per_row", "data_max_bytes", "styles"}},
				"direct_messages": {required: []string{"bot_can_start"}},
				"delete":          {required: []string{"max_age_seconds"}, nullable: []string{"max_age_seconds"}},
			},
		},
		"limits": {
			required: []string{"max_body_length", "edit_max_age_seconds"},
			optional: []string{"send_per_second_per_channel", "send_per_second"},
			nullable: []string{"edit_max_age_seconds"},
		},
	},
}

// check returns the first problem of the object m at path, "" when it has none.
func (s shape) check(path string, m map[string]json.RawMessage) string {
	for _, k := range s.required {
		if _, ok := m[k]; !ok {
			return join(path, k) + " is missing"
		}
	}
	for _, k := range append(slices.Clone(s.required), s.optional...) {
		v, ok := m[k]
		if !ok {
			continue
		}
		if isNull(v) {
			if slices.Contains(s.nullable, k) {
				continue
			}
			return join(path, k) + " is null"
		}
		inner, ok := s.nested[k]
		if !ok {
			continue
		}
		obj, ok := object(v)
		if !ok {
			return join(path, k) + " is not an object"
		}
		if problem := inner.check(join(path, k), obj); problem != "" {
			return problem
		}
	}
	return ""
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

var linkURLTemplateRe = regexp.MustCompile(`^https?://\S*\{token\}\S*$`)

// validateInfo checks the values of a decoded info against api/bridge.yaml and returns the first problem, "" when it
// has none. Unknown members are ignored.
func validateInfo(i gen.Info) string {
	for _, f := range []struct{ path, value string }{{"bridge.name", i.Bridge.Name},
		{"bridge.version", i.Bridge.Version}, {"messenger.name", i.Messenger.Name}} {
		if n := utf8.RuneCountInString(f.value); n < 1 || n > 100 {
			return f.path + " is not 1 to 100 characters long"
		}
	}
	if !i.UserLookup.Valid() {
		return "user_lookup has an unknown value"
	}
	if len(i.EventDelivery) == 0 {
		return "event_delivery is empty"
	}
	if problem := uniqueValid("event_delivery", i.EventDelivery); problem != "" {
		return problem
	}
	if problem := validateLimits(i.Limits); problem != "" {
		return problem
	}
	if t, err := i.LinkUrlTemplate.Get(); err == nil &&
		(utf8.RuneCountInString(t) > 1024 || !linkURLTemplateRe.MatchString(t)) {
		return "link_url_template is not an http or https link with {token} of at most 1024 characters"
	}
	return validateCapabilities(i.Capabilities)
}

func validateLimits(l gen.Limits) string {
	switch {
	case l.MaxBodyLength < 500:
		return "limits.max_body_length is below 500"
	case l.SendPerSecondPerChannel != nil && *l.SendPerSecondPerChannel <= 0:
		return "limits.send_per_second_per_channel is not positive"
	case l.SendPerSecond != nil && *l.SendPerSecond <= 0:
		return "limits.send_per_second is not positive"
	}
	if n, err := l.EditMaxAgeSeconds.Get(); err == nil && n < 300 {
		return "limits.edit_max_age_seconds is below 300"
	}
	return ""
}

func validateCapabilities(c gen.Capabilities) string {
	if b := c.Buttons; b != nil {
		switch {
		case b.MaxPerMessage < 2:
			return "capabilities.buttons.max_per_message is below 2"
		case b.MaxPerRow < 1:
			return "capabilities.buttons.max_per_row is below 1"
		case b.DataMaxBytes < 64:
			return "capabilities.buttons.data_max_bytes is below 64"
		}
		if problem := uniqueValid("capabilities.buttons.styles", b.Styles); problem != "" {
			return problem
		}
	}
	if c.ThreadMessages != nil && !c.ThreadMessages.Valid() {
		return "capabilities.thread_messages has an unknown value"
	}
	if c.PrivateAnswers != nil {
		if problem := uniqueValid("capabilities.private_answers", *c.PrivateAnswers); problem != "" {
			return problem
		}
	}
	if c.Delete != nil {
		if n, err := c.Delete.MaxAgeSeconds.Get(); err == nil && n < 1 {
			return "capabilities.delete.max_age_seconds is below 1"
		}
	}
	return ""
}

// uniqueValid checks that the items of an enum array are known and unique.
func uniqueValid[E interface {
	~string
	Valid() bool
}](path string, items []E) string {
	seen := map[E]bool{}
	for _, e := range items {
		if !e.Valid() {
			return path + " has an unknown value"
		}
		if seen[e] {
			return path + " has a repeated value"
		}
		seen[e] = true
	}
	return ""
}

// GetInfo calls getInfo (C-22.FR-2). A 401 is the code token_invalid with MessageTokenInvalid; an info that Muster
// cannot use is invalid, with the code and the message of its *InfoError.
func (c *Client) GetInfo(ctx context.Context, class outbound.Class) (Info, Result) {
	res, r := c.do(ctx, class, "getInfo", http.MethodGet, "info", nil, nil, nil)
	switch {
	case r.Status == http.StatusUnauthorized:
		r.Code, r.Error = CodeTokenInvalid, MessageTokenInvalid
		return Info{}, r
	case !r.OK() && r.Status >= http.StatusOK && r.Status < http.StatusMultipleChoices:
		return Info{}, c.infoFailed(r.Status, mismatch("info is larger than 64 KiB"))
	case !r.OK():
		return Info{}, r
	}
	i, err := ParseInfo(res.Body)
	if ie, ok := errors.AsType[*InfoError](err); ok {
		return Info{}, c.infoFailed(r.Status, ie)
	}
	return i, r
}

// infoFailed is the Result of an info Muster cannot use; an unknown version, which the message shows, is masked of
// the token and the push secret like any other text of the bridge, before it is cut.
func (c *Client) infoFailed(status int, e *InfoError) Result {
	msg := c.settings.mask(e.Message)
	if e.version != "" {
		msg = versionUnknown(c.settings.mask(e.version)).Message
	}
	return Result{Status: status, Classification: Classification{Class: ClassInvalid}, Code: e.Code,
		Error: outbound.Untrusted(msg)}
}

// EventDeliveryStep checks that the bridge offers the event delivery mode of a Connection (C-22.FR-2); a failure has
// its code and message.
func EventDeliveryStep(i Info, mode string) (ok bool, code, message string) {
	if slices.Contains(i.EventDelivery, gen.InfoEventDelivery(mode)) {
		return true, "", ""
	}
	return false, CodeEventDeliveryNotOffered, fmt.Sprintf(messageNotOffered, mode)
}

// LogInfoRead logs bridge_info_read for the Connection publicID (C-22.FR-24).
func LogInfoRead(ctx context.Context, log *logging.Logger, publicID string, i Info) {
	log.Log(ctx, logging.BridgeInfoRead, logging.F("connection", publicID),
		logging.F("protocol_version", string(i.ProtocolVersion)), logging.F("features", i.Features()))
}

// Stored is Muster's copy of the info of a Connection and when it was read, on the business clock.
type Stored struct {
	Info   Info
	ReadAt time.Time
}

// InfoStore keeps the copy of info of each Bridge Connection; internal/connections implements it. StoredInfo has no
// copy for a Connection whose version is no longer conn's, and StoreInfo stores nothing for one.
type InfoStore interface {
	StoredInfo(ctx context.Context, conn Conn) (Stored, bool, error)
	StoreInfo(ctx context.Context, conn Conn, i Info, at time.Time) error
}

// Conn is a Bridge Connection as InfoCache needs it: its id, its public_id for the log, the version of its
// configuration that its client was built from, and its client.
type Conn struct {
	ID       int64
	PublicID string
	Version  int64
	Client   *Client
}

// kept is a copy in memory, with the version of the Connection it was read for.
type kept struct {
	version int64
	stored  Stored
}

// InfoCache keeps the info of each Bridge Connection for the calls that depend on it (C-22.FR-2): in memory, else
// from the store, read again through the bridge when it is older than its maximum age on the business clock. A copy
// belongs to a version of its Connection, so that a new base URL or token never serves the info of the old one.
type InfoCache struct {
	store    InfoStore
	business clock.Clock
	maxAge   time.Duration
	log      *logging.Logger

	mu     sync.Mutex
	copies map[int64]kept
}

// NewInfoCache returns an InfoCache over store; a maxAge of 0 is DefaultInfoMaxAge.
func NewInfoCache(store InfoStore, business clock.Clock, maxAge time.Duration, log *logging.Logger) *InfoCache {
	if maxAge <= 0 {
		maxAge = DefaultInfoMaxAge
	}
	return &InfoCache{store: store, business: business, maxAge: maxAge, log: log, copies: map[int64]kept{}}
}

// Info is Muster's copy of the info of conn. When there is none, or it is older than the maximum age, it is read again
// through conn's client in class; a fresh read is stored, kept and logged. A failed read returns the stale copy, if
// there is one, with the failed Result; a failure of the store is a transient Result.
func (ic *InfoCache) Info(ctx context.Context, conn Conn, class outbound.Class) (Info, Result) {
	cur, ok, err := ic.copy(ctx, conn)
	if err != nil {
		return Info{}, storeFailed(err)
	}
	if ok && ic.business.Now().Sub(cur.ReadAt) < ic.maxAge {
		return cur.Info, Result{}
	}
	i, r := conn.Client.GetInfo(ctx, class)
	if !r.OK() {
		return cur.Info, r
	}
	if err := ic.keep(ctx, conn, i); err != nil {
		return i, storeFailed(err)
	}
	return i, r
}

// Refresh reads the info of conn again through its client in class, whatever Muster's copy, as a Connection check
// does: a fresh read is stored, kept and logged, and a failed one changes nothing. The error is a failure of the
// store, which leaves the fresh read kept in memory.
func (ic *InfoCache) Refresh(ctx context.Context, conn Conn, class outbound.Class) (Info, Result, error) {
	i, r := conn.Client.GetInfo(ctx, class)
	if !r.OK() {
		return Info{}, r, nil
	}
	return i, r, ic.keep(ctx, conn, i)
}

// keep keeps a fresh read of the info of conn in memory, logs bridge_info_read and stores it.
func (ic *InfoCache) keep(ctx context.Context, conn Conn, i Info) error {
	now := ic.business.Now()
	ic.mu.Lock()
	ic.copies[conn.ID] = kept{version: conn.Version, stored: Stored{Info: i, ReadAt: now}}
	ic.mu.Unlock()
	LogInfoRead(ctx, ic.log, conn.PublicID, i)
	return ic.store.StoreInfo(ctx, conn, i, now)
}

// copy is the copy kept in memory for the version of conn, else the stored one, which is then kept.
func (ic *InfoCache) copy(ctx context.Context, conn Conn) (Stored, bool, error) {
	ic.mu.Lock()
	k, ok := ic.copies[conn.ID]
	ic.mu.Unlock()
	if ok && k.version == conn.Version {
		return k.stored, true, nil
	}
	cur, ok, err := ic.store.StoredInfo(ctx, conn)
	if err != nil {
		return Stored{}, false, err
	}
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if !ok {
		delete(ic.copies, conn.ID)
		return Stored{}, false, nil
	}
	ic.copies[conn.ID] = kept{version: conn.Version, stored: cur}
	return cur, true, nil
}

func storeFailed(err error) Result {
	return Result{Classification: Classification{Class: ClassTransient},
		Error: outbound.Untrusted("Muster's copy of the bridge's info: " + err.Error())}
}
