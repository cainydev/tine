package main

import (
	"strings"
	"testing"
)

func TestConnectEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cmd      connectCmd
		wantURL  string
		wantName string
		wantErr  string
	}{
		{
			name:     "instance path against a base url",
			cmd:      connectCmd{Instance: "john/shopware/edc1e8b0", BaseURL: "https://tine.cainy.dev"},
			wantURL:  "https://tine.cainy.dev/john/shopware/edc1e8b0",
			wantName: "shopware",
		},
		{
			name:     "leading and trailing slashes are trimmed",
			cmd:      connectCmd{Instance: "/john/shopware/edc1e8b0/", BaseURL: "https://tine.cainy.dev/"},
			wantURL:  "https://tine.cainy.dev/john/shopware/edc1e8b0",
			wantName: "shopware",
		},
		{
			name:     "full url is used as given",
			cmd:      connectCmd{Instance: "https://tine.cainy.dev/john/shopware/edc1e8b0"},
			wantURL:  "https://tine.cainy.dev/john/shopware/edc1e8b0",
			wantName: "shopware",
		},
		{
			name:    "too few segments",
			cmd:     connectCmd{Instance: "john/shopware", BaseURL: "https://tine.cainy.dev"},
			wantErr: "expected user/integration/id",
		},
		{
			name:    "too many segments",
			cmd:     connectCmd{Instance: "a/b/c/d", BaseURL: "https://tine.cainy.dev"},
			wantErr: "expected user/integration/id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gotURL, gotName, err := tc.cmd.endpoint()

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("endpoint() = %q, want an error containing %q", gotURL, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("endpoint(): %v", err)
			}
			if gotURL != tc.wantURL {
				t.Errorf("url = %q, want %q", gotURL, tc.wantURL)
			}
			if gotName != tc.wantName {
				t.Errorf("name = %q, want %q", gotName, tc.wantName)
			}
		})
	}
}

func TestServerName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{"john/shopware/edc1e8b0", "shopware"},
		{"https://tine.cainy.dev/john/deutsche-bahn/abc", "deutsche-bahn"},
		{"/john/shopware/edc1e8b0/", "shopware"},
		{"single", "tine"},
		{"", "tine"},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()

			if got := serverName(tc.in); got != tc.want {
				t.Errorf("serverName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestEndpointPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"full url", "https://tine.cainy.dev/john/shopware/edc1e8b0", "/john/shopware/edc1e8b0"},
		{"with port", "http://localhost:8377/dev/shopware/dev", "/dev/shopware/dev"},
		{"existing query", "https://tine.cainy.dev/john/shopware/x?a=b", "/john/shopware/x"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := endpointPath(tc.in)
			if err != nil {
				t.Fatalf("endpointPath: %v", err)
			}
			if got != tc.want {
				t.Errorf("endpointPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFirstNonEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{"first wins", []string{"a", "b"}, "a"},
		{"skips empty", []string{"", "b"}, "b"},
		{"all empty", []string{"", ""}, ""},
		{"none", nil, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := firstNonEmpty(tc.values...); got != tc.want {
				t.Errorf("firstNonEmpty(%v) = %q, want %q", tc.values, got, tc.want)
			}
		})
	}
}

// TestSubjectForExplicit covers the branch that needs no store: --subject is
// taken as given, so a signed URL can be minted without the local database.
func TestSubjectForExplicit(t *testing.T) {
	t.Parallel()

	c := connectCmd{Subject: "user-123"}

	got, err := c.subjectFor(nil, "/john/shopware/edc1e8b0")
	if err != nil {
		t.Fatalf("subjectFor: %v", err)
	}
	if got != "user-123" {
		t.Errorf("subject = %q, want %q", got, "user-123")
	}
}
