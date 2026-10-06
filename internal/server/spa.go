// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package server

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// ContentSecurityPolicy is the Content Security Policy of every answer of the app listener (C-03.FR-16): everything
// from the app's own origin, images also as data: URIs, no plugins, no base URI, forms posted only to itself and no
// framing.
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; " +
	"frame-ancestors 'none'"

// hstsValue keeps browsers on HTTPS for a year once they saw the app over HTTPS.
const hstsValue = "max-age=31536000"

// App is the handler of the app listener: every path under /api/ goes to api, which answers unknown API paths with a
// problem, and every other path to the SPA in dist. Every answer carries the security headers, with
// Strict-Transport-Security when hsts is set, which it is when MUSTER_PUBLIC_URL is https.
func App(api http.Handler, dist fs.FS, hsts bool) http.Handler {
	spa := SPA(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if hsts {
			h.Set("Strict-Transport-Security", hstsValue)
		}
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			api.ServeHTTP(w, r)
			return
		}
		spa.ServeHTTP(w, r)
	})
}

// SPA serves the single-page application in dist: a file that exists as itself, any other path as index.html, so
// that the SPA's router handles it. A missing file under assets/ answers 404 instead: a page left open across an
// upgrade asks for chunks of the old build, and index.html in their place would fail as a script of the wrong type.
// Without a built index.html it answers 503.
func SPA(dist fs.FS) http.Handler {
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if fi, err := fs.Stat(dist, name); err == nil && !fi.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(name, "assets/") {
				http.NotFound(w, r)
				return
			}
		}
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			http.Error(w, "the web interface is not built", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}
