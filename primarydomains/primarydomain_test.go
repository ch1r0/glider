package primarydomains

import (
	"testing"

	"github.com/nadoo/glider/common/log"
)

func TestPrimaryDomain(t *testing.T) {
	log.F = t.Logf
	knownTlds := []string{"abc", "def"}

	tests := map[string]string{
		"http://www.test.abc": "test.abc",     // Look up by TLD
		"http://www.test.nul": "www.test.nul", // No match
		"http://www.test.com": "test.com",     // Look up by domaintool.com query
	}

	lookup := NewLookup(knownTlds, "")

	for url, expected := range tests {
		domain, _ := lookup.Get(url)

		if domain != expected {
			t.Fatalf("lookup.Get(\"%s\" failed, got %s, expecting %s", url, domain, expected)
		}
	}
}
