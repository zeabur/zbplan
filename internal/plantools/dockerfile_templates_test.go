package plantools

import (
	"testing"
)

func TestMatchTemplates(t *testing.T) {
	cases := []struct {
		query   string
		wantTop string // expected name of the best match
	}{
		{"go", "go"},
		{"golang", "go"},
		{"pip", "python-pip"},
		{"uv", "python-uv"},
		{"fastapi", "fastapi"},
		{"bun", "bun"},
		{"nextjs", "nextjs"},
		{"next.js", "nextjs"},
		{"rust", "rust"},
		{"cargo", "rust"},
		{"maven", "java-maven"},
		{"gradle", "java-gradle"},
		{"php", "php"},
		{"ruby", "ruby"},
		{"rails", "ruby"},
		{"deno", "deno"},
		{"nginx", "static"},
		{"nuxt", "nuxt-server"},
		{"generate", "nuxt-static"},
		{"nitro", "nuxt-server"},
	}

	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			got := matchTemplates(tc.query, allDockerfileTemplates, 3)
			if len(got) == 0 {
				t.Fatalf("query %q: got no matches, want top=%s", tc.query, tc.wantTop)
			}
			if got[0].name != tc.wantTop {
				t.Errorf("query %q: top match = %q, want %q", tc.query, got[0].name, tc.wantTop)
			}
		})
	}
}
