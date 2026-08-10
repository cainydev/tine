// Package slug validates the identifiers that appear in tine's URLs.
package slug

import (
	"fmt"
	"regexp"
	"strings"
)

// Pattern is the one rule for every slug: lowercase letters, digits and
// hyphens, starting and ending with a letter or digit.
var Pattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const (
	// MinLength avoids single-character slugs, which read as typos and leave no
	// room to grow a namespace.
	MinLength = 2

	// MaxLength keeps a full endpoint path comfortably short.
	MaxLength = 48
)

// reserved names cannot be used as a user slug, because they are, or may
// become, top-level routes. Reserving them now costs nothing; taking them back
// later would break every URL a user has configured.
var reserved = map[string]bool{
	"new": true, "edit": true, "delete": true, "settings": true,
	"integrations": true, "instances": true, "account": true,
	"login": true, "logout": true, "register": true, "signin": true,
	"signup": true, "auth": true, "callback": true, "oauth": true,
	"api": true, "admin": true, "static": true, "assets": true,
	"health": true, "healthz": true, "metrics": true, "status": true,
	"docs": true, "help": true, "about": true, "support": true,
	"tine": true, "www": true, "mcp": true, "well-known": true,
}

// Validate reports whether s is a usable slug.
func Validate(kind, s string) error {
	switch {
	case s == "":
		return fmt.Errorf("%s is required", kind)
	case len(s) < MinLength:
		return fmt.Errorf("%s must be at least %d characters", kind, MinLength)
	case len(s) > MaxLength:
		return fmt.Errorf("%s must be at most %d characters", kind, MaxLength)
	case !Pattern.MatchString(s):
		return fmt.Errorf("%s may contain only lowercase letters, digits and hyphens, and must start and end with a letter or digit", kind)
	}
	return nil
}

// ValidateUser reports whether s is a usable user slug.
func ValidateUser(s string) error {
	if err := Validate("username", s); err != nil {
		return err
	}
	if reserved[s] {
		return fmt.Errorf("username %q is reserved", s)
	}
	return nil
}

// Reserved reports whether s is a name tine keeps for itself.
func Reserved(s string) bool {
	return reserved[s]
}

// Suggest converts arbitrary text into a valid slug, for prefilling a form from
// an email address or display name. It returns an empty string when nothing
// usable remains.
func Suggest(text string) string {
	var b strings.Builder
	lastHyphen := true

	for _, r := range strings.ToLower(text) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		case !lastHyphen:
			b.WriteByte('-')
			lastHyphen = true
		}
	}

	out := strings.Trim(b.String(), "-")
	if len(out) > MaxLength {
		out = strings.Trim(out[:MaxLength], "-")
	}
	if len(out) < MinLength || reserved[out] {
		return ""
	}
	return out
}
