package slug

import "testing"

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{"simple", "john", true},
		{"with digits", "shop2", true},
		{"hyphenated", "shopware-prod", true},
		{"many hyphens", "a-b-c-d", true},
		{"digits only", "42", true},

		{"empty", "", false},
		{"too short", "a", false},
		{"uppercase", "John", false},
		{"underscore", "john_doe", false},
		{"space", "john doe", false},
		{"leading hyphen", "-john", false},
		{"trailing hyphen", "john-", false},
		{"double hyphen", "john--doe", false},
		{"dot", "john.doe", false},
		{"slash", "john/doe", false},
		{"umlaut", "jöhn", false},
		{"emoji", "john\U0001F600", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := Validate("slug", tc.input)
			if tc.valid && err != nil {
				t.Errorf("Validate(%q) = %v, want nil", tc.input, err)
			}
			if !tc.valid && err == nil {
				t.Errorf("Validate(%q) = nil, want an error", tc.input)
			}
		})
	}
}

func TestValidateLength(t *testing.T) {
	t.Parallel()

	long := make([]byte, MaxLength+1)
	for i := range long {
		long[i] = 'a'
	}
	if err := Validate("slug", string(long)); err == nil {
		t.Error("expected an error for a slug over the length limit")
	}

	ok := string(long[:MaxLength])
	if err := Validate("slug", ok); err != nil {
		t.Errorf("slug at the exact limit rejected: %v", err)
	}
}

// Reserved names are routes tine may serve itself, so a user must not take one.
func TestValidateUserRejectsReserved(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"new", "settings", "login", "api", "static", "mcp"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := ValidateUser(name); err == nil {
				t.Errorf("ValidateUser(%q) = nil, want an error", name)
			}
			if err := Validate("slug", name); err != nil {
				t.Errorf("Validate(%q) = %v, want nil", name, err)
			}
		})
	}
}

func TestSuggest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"email", "john.wagner@example.com", "john-wagner-example-com"},
		{"display name", "John Wagner", "john-wagner"},
		{"already a slug", "john-wagner", "john-wagner"},
		{"uppercase", "JOHN", "john"},
		{"punctuation collapses", "john...wagner", "john-wagner"},
		{"leading and trailing junk", "  john  ", "john"},
		{"nothing usable", "!!!", ""},
		{"too short", "a", ""},
		{"reserved becomes empty", "settings", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := Suggest(tc.input)
			if got != tc.want {
				t.Errorf("Suggest(%q) = %q, want %q", tc.input, got, tc.want)
			}
			if got != "" {
				if err := Validate("slug", got); err != nil {
					t.Errorf("Suggest(%q) produced an invalid slug %q: %v", tc.input, got, err)
				}
			}
		})
	}
}

func TestSuggestTruncatesCleanly(t *testing.T) {
	t.Parallel()

	long := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbb"

	got := Suggest(long)
	if len(got) > MaxLength {
		t.Errorf("Suggest returned %d characters, over the %d limit", len(got), MaxLength)
	}
	if err := Validate("slug", got); err != nil {
		t.Errorf("truncated suggestion is invalid: %v", err)
	}
}
