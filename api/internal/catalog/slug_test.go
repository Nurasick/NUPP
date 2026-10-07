package catalog_test

import (
	"testing"

	"github.com/Nurasick/NUPP/api/internal/catalog"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"CSCI 151":            "csci-151",
		"  Math-161 / A  ":    "math-161-a",
		"PHYS162":             "phys162",
		"Midterm 1 questions": "midterm-1-questions",
		"%%%":                 "",
		"Қазақ тілі":          "", // non-Latin text has no ASCII slug
	}
	for in, want := range cases {
		if got := catalog.Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
