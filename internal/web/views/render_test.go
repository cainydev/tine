package views

import (
	"context"
	"strings"
	"testing"
)

func TestOnlyAcceptedKindsRendered(t *testing.T) {
	var sb strings.Builder
	err := InstanceDetail(
		Nav{UserSlug: "john", SignedIn: true},
		Instance{
			ID: "abc", DisplayName: "Shop", IntegrationSlug: "shopware",
			IntegrationName: "Shopware 6", Version: "1.0.0",
			CredentialKinds: []string{"oauth2"},
		},
		nil, "",
	).Render(context.Background(), &sb)
	if err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	for _, bad := range []string{"bearer token", "header value", "username and password", "no authentication"} {
		if strings.Contains(out, ">"+bad+"<") {
			t.Errorf("rendered an option for %q that shopware does not accept", bad)
		}
	}
	if !strings.Contains(out, `name="kind" value="oauth2"`) {
		t.Error("expected a hidden kind field set to oauth2")
	}
	if !strings.Contains(out, "oauth_client_id") {
		t.Error("expected the oauth client id field")
	}
}

// An integration that needs no credential shows no form at all.
func TestNoCredentialIntegrationShowsNoForm(t *testing.T) {
	var sb strings.Builder
	err := InstanceDetail(
		Nav{UserSlug: "john", SignedIn: true},
		Instance{
			ID: "abc", DisplayName: "DB", IntegrationSlug: "deutsche-bahn",
			IntegrationName: "Deutsche Bahn", Version: "1.0.0",
			CredentialKinds: []string{"none"},
		},
		nil, "",
	).Render(context.Background(), &sb)
	if err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	if strings.Contains(out, "save credential") {
		t.Error("rendered a save button for an integration that takes no credential")
	}
	if !strings.Contains(out, "needs no credential") {
		t.Error("expected an explanation that no credential is needed")
	}
}
