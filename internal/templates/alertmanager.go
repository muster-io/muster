// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package templates

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"
	"unicode"
)

// alertmanagerFuncs are Alertmanager's template functions under their Alertmanager names, re-implemented from their
// documented behaviour, so that existing Alertmanager templates carry over (ADR-0012). toUpper, toLower and join come
// from sprout, which behaves the same, and reReplaceAll is among the bounded functions; safeHtml returns its argument
// unchanged, since alert data arrive already escaped.
func alertmanagerFuncs() template.FuncMap {
	return template.FuncMap{
		"title":     title,
		"trimSpace": strings.TrimSpace,
		"match": func(pattern, s string) (bool, error) {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return false, err
			}
			return re.MatchString(s), nil
		},
		"safeHtml":    func(s string) string { return s },
		"safeUrl":     func(s string) string { return s },
		"urlUnescape": url.QueryUnescape,
		"stringSlice": func(s ...string) []string { return s },
		"date":        func(layout string, t time.Time) string { return t.Format(layout) },
		"tz": func(name string, t time.Time) (time.Time, error) {
			loc, err := time.LoadLocation(name)
			if err != nil {
				return time.Time{}, err
			}
			return t.In(loc), nil
		},
		"humanizeDuration": humanizeDuration,
		"toJson": func(v any) (string, error) {
			b, err := json.Marshal(v)
			return string(b), err
		},
	}
}

// title upper-cases the first letter of every word.
func title(s string) string {
	prev := ' '
	return strings.Map(func(r rune) rune {
		defer func() { prev = r }()
		if unicode.IsSpace(prev) || unicode.IsPunct(prev) && prev != '\'' {
			return unicode.ToTitle(r)
		}
		return r
	}, s)
}

// humanizeDuration writes a number of seconds as days, hours, minutes and seconds ("1d 2h 3m 4s"), or below one
// second with a metric prefix ("12.5ms").
func humanizeDuration(v any) (string, error) {
	f, err := seconds(v)
	if err != nil {
		return "", err
	}
	switch {
	case math.IsNaN(f) || math.IsInf(f, 0):
		return fmt.Sprintf("%.4g", f), nil
	case f == 0:
		return "0s", nil
	case math.Abs(f) >= 1:
		sign := ""
		if f < 0 {
			sign, f = "-", -f
		}
		total := int64(f)
		d, h, m, s := total/86400, total/3600%24, total/60%60, total%60
		switch {
		case d != 0:
			return fmt.Sprintf("%s%dd %dh %dm %ds", sign, d, h, m, s), nil
		case h != 0:
			return fmt.Sprintf("%s%dh %dm %ds", sign, h, m, s), nil
		case m != 0:
			return fmt.Sprintf("%s%dm %ds", sign, m, s), nil
		}
		return fmt.Sprintf("%s%.4gs", sign, f), nil
	}
	prefix := ""
	for _, p := range []string{"m", "u", "n", "p", "f", "a", "z", "y"} {
		if math.Abs(f) >= 1 {
			break
		}
		prefix, f = p, f*1000
	}
	return fmt.Sprintf("%.4g%ss", f, prefix), nil
}

// seconds reads a number of seconds from a number, a numeric string or a time.Duration.
func seconds(v any) (float64, error) {
	switch v := v.(type) {
	case time.Duration:
		return v.Seconds(), nil
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case uint:
		return float64(v), nil
	case uint64:
		return float64(v), nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(v), 64)
	}
	return 0, fmt.Errorf("humanizeDuration: %T is not a number", v)
}

// webhook is an Alertmanager webhook as the template data read it.
type webhook struct {
	Receiver          string            `json:"receiver"`
	Status            string            `json:"status"`
	Alerts            []webhookAlert    `json:"alerts"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
}

type webhookAlert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// ErrNotWebhook is a Stored Snapshot whose body is not an Alertmanager webhook.
var ErrNotWebhook = errors.New("the body is not an alertmanager webhook")

// FromWebhook is the template data of an Alertmanager webhook as Alertmanager sent it, without the Alert Group; the
// values are as received, not yet made safe for a markup.
func FromWebhook(body []byte) (Data, error) {
	var w webhook
	if err := json.Unmarshal(body, &w); err != nil || w.Status == "" {
		return Data{}, ErrNotWebhook
	}
	d := Data{Receiver: w.Receiver, Status: w.Status, GroupLabels: kvOf(w.GroupLabels),
		CommonLabels: kvOf(w.CommonLabels), CommonAnnotations: kvOf(w.CommonAnnotations), ExternalURL: w.ExternalURL,
		Alerts: make(Alerts, 0, len(w.Alerts))}
	for _, a := range w.Alerts {
		d.Alerts = append(d.Alerts, Alert{Status: a.Status, Labels: kvOf(a.Labels), Annotations: kvOf(a.Annotations),
			StartsAt: a.StartsAt.UTC(), EndsAt: a.EndsAt.UTC(), GeneratorURL: a.GeneratorURL,
			Fingerprint: a.Fingerprint})
	}
	return d, nil
}

func kvOf(m map[string]string) KV {
	if m == nil {
		return KV{}
	}
	return KV(m)
}
