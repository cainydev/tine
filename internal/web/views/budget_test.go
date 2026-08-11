package views

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/a-h/templ"
)

// firstWindow is what a server can send before waiting for an acknowledgement:
// ten segments of about 1460 bytes, less TLS record and HPACK overhead.
//
// A page that fits arrives in one round trip. Compressed, because every
// deployment serves these through a proxy that encodes.
const firstWindow = 13_500

// stylesheetPath is the compiled tailwind output the layout links.
const stylesheetPath = "../static/app.css"

// TestPagesFitTheFirstWindow keeps a page and its stylesheet inside one round
// trip. It fails when a new page or a new set of utility classes pushes the
// pair over, which is otherwise only visible in production.
func TestPagesFitTheFirstWindow(t *testing.T) {
	t.Parallel()

	css, err := os.ReadFile(stylesheetPath)
	if err != nil {
		t.Fatalf("read stylesheet: %v", err)
	}
	cssSize := gzipped(t, css)

	nav := Nav{UserSlug: "john", Email: "john@example.com", SignedIn: true}

	// A list page grows with what it lists, so the budget is measured against a
	// heavily populated account rather than a fresh one.
	const listed = 100

	instances := make([]Instance, 0, listed)
	for i := range listed {
		instances = append(instances, Instance{
			ID:              fmt.Sprintf("%016x", i),
			DisplayName:     fmt.Sprintf("Shopware Production %d", i),
			IntegrationSlug: "shopware", IntegrationName: "Shopware 6",
			Version: "1.0.0", Enabled: true, CredentialKind: "oauth2",
			Endpoint: fmt.Sprintf("https://tine.cainy.dev/john/shopware/%016x", i),
		})
	}

	tokens := make([]Token, 0, listed)
	for i := range listed {
		tokens = append(tokens, Token{
			ID: fmt.Sprintf("tok%d", i), Name: fmt.Sprintf("nightly-sync-%d", i), Scoped: true,
			Scopes:  []string{"shopware · Shopware Production", "deutsche-bahn · DB Fahrplan"},
			Created: "created 2026-08-11", LastUsed: "just now", Expires: "2027-01-01",
		})
	}

	integrations := make([]Integration, 0, listed)
	for i := range listed {
		integrations = append(integrations, Integration{
			Slug:    fmt.Sprintf("integration-%d", i),
			Name:    fmt.Sprintf("Integration %d", i),
			Version: "1.0.0", Instances: i,
		})
	}

	pages := []struct {
		name string
		page templ.Component
	}{
		{"instances", InstanceList(nav, instances)},
		{"integrations", IntegrationList(nav, integrations)},
		{"instance detail", InstanceDetail(nav, instances[0], nil, "")},
		{"settings", Settings(nav, "https://tine.cainy.dev")},
		{"tokens", Tokens(nav, tokens, scopes(instances), "", "")},
	}

	for _, tc := range pages {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var body bytes.Buffer
			if err := tc.page.Render(context.Background(), &body); err != nil {
				t.Fatalf("render: %v", err)
			}

			html := gzipped(t, body.Bytes())
			total := html + cssSize

			t.Logf("html %d + css %d = %d of %d bytes", html, cssSize, total, firstWindow)

			if total > firstWindow {
				t.Errorf("page and stylesheet are %d bytes gzipped, over the %d byte first window by %d",
					total, firstWindow, total-firstWindow)
			}
		})
	}
}

// scopes renders the instance selector the token form shows.
func scopes(instances []Instance) []TokenScope {
	out := make([]TokenScope, 0, len(instances))
	for _, in := range instances {
		out = append(out, TokenScope{
			InstanceID: in.ID,
			Label:      in.IntegrationSlug + " · " + in.DisplayName,
		})
	}
	return out
}

func gzipped(t *testing.T, b []byte) int {
	t.Helper()

	var out bytes.Buffer

	w, err := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if err != nil {
		t.Fatalf("new gzip writer: %v", err)
	}
	if _, err := w.Write(b); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return out.Len()
}
