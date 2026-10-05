// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors

package web_test

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/muster-io/muster/web"
)

// requiredEnv makes a missing build fail the test instead of skipping it, for checks that run after `make build`.
const requiredEnv = "WEB_DIST_REQUIRED"

func TestEmbeddedIndex(t *testing.T) {
	index, err := fs.ReadFile(web.Dist(), "index.html")
	if errors.Is(err, fs.ErrNotExist) {
		if os.Getenv(requiredEnv) == "1" {
			t.Fatalf("index.html is not embedded (%s=1): run make build first", requiredEnv)
		}
		t.Skipf("index.html is not embedded: the SPA is not built; run make build, or set %s=1 to fail", requiredEnv)
	}
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(index), `<div id="root"`) {
		t.Errorf("index.html is not the SPA shell, it has no root element:\n%s", index)
	}
}
