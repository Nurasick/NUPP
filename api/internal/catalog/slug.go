// Package catalog serves the public, read-only catalog of approved materials:
// courses → offerings → assessments → materials → files.
//
// Layout of this package:
//   - queries.sql      SQL source; sqlc generates ./catalogdb from it
//   - handler.go       HTTP handlers (parse request → query → write JSON)
//   - views.go         JSON response shapes and the conversions into them
//   - slug.go          URL slug helper
package catalog

import (
	"regexp"
	"strings"
)

// nonSlugChars matches every run of characters that may not appear in a slug.
// Compiling the regexp once at package level avoids recompiling it per call.
var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

// validSlug matches exactly the slugs the database accepts (same pattern as
// the courses.slug CHECK constraint in the migration).
var validSlug = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Slugify turns text like "CSCI 151" into a URL-safe slug like "csci-151":
// lower-case ASCII letters and digits separated by single hyphens.
//
// Anything else (spaces, punctuation, and also non-Latin letters) becomes a
// separator, so text with no ASCII letters or digits yields "". Callers must
// handle that case.
func Slugify(text string) string {
	slug := nonSlugChars.ReplaceAllString(strings.ToLower(text), "-")
	return strings.Trim(slug, "-")
}
