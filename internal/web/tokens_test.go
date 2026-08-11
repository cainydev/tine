package web

import (
	"testing"
	"time"
)

func TestSince(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		used time.Time
		want string
	}{
		{"seconds ago", now.Add(-10 * time.Second), "just now"},
		{"a minute ago", now.Add(-90 * time.Second), "just now"},
		{"minutes ago", now.Add(-20 * time.Minute), "20 minutes ago"},
		{"an hour ago", now.Add(-90 * time.Minute), "an hour ago"},
		{"hours ago", now.Add(-5 * time.Hour), "5 hours ago"},
		{"yesterday", now.Add(-30 * time.Hour), "yesterday"},
		{"days ago", now.Add(-5 * 24 * time.Hour), "5 days ago"},
		{"falls back to a date", now.Add(-90 * 24 * time.Hour), "2026-05-13"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := since(tc.used, now); got != tc.want {
				t.Errorf("since() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseExpiry(t *testing.T) {
	t.Parallel()

	t.Run("empty means never", func(t *testing.T) {
		t.Parallel()

		got, err := parseExpiry("")
		if err != nil {
			t.Fatalf("parseExpiry: %v", err)
		}
		if !got.IsZero() {
			t.Errorf("expiry = %v, want the zero time", got)
		}
	})

	t.Run("a date stays valid through the end of that day", func(t *testing.T) {
		t.Parallel()

		day := time.Now().Add(48 * time.Hour).Format(expiryLayout)

		got, err := parseExpiry(day)
		if err != nil {
			t.Fatalf("parseExpiry: %v", err)
		}

		parsed, _ := time.ParseInLocation(expiryLayout, day, time.Local)
		if !got.Equal(parsed.Add(24 * time.Hour)) {
			t.Errorf("expiry = %v, want the end of %s", got, day)
		}
	})

	t.Run("rejects a past date", func(t *testing.T) {
		t.Parallel()

		if _, err := parseExpiry("2020-01-01"); err == nil {
			t.Fatal("want an error for a date in the past")
		}
	})

	t.Run("rejects nonsense", func(t *testing.T) {
		t.Parallel()

		if _, err := parseExpiry("not-a-date"); err == nil {
			t.Fatal("want an error for an unparseable date")
		}
	})
}

func TestSelectedInstances(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values []string
		want   int
	}{
		{"nothing selected", nil, 0},
		{"every instance", []string{""}, 0},
		{"one instance", []string{"inst-1"}, 1},
		{"a subset", []string{"inst-1", "inst-2"}, 2},
		{"every instance wins over a subset", []string{"", "inst-1"}, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := selectedInstances(tc.values); len(got) != tc.want {
				t.Errorf("selectedInstances(%v) = %v, want %d entries", tc.values, got, tc.want)
			}
		})
	}
}
