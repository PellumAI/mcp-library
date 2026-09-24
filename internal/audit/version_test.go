package audit

import "testing"

func TestCompareVersion(t *testing.T) {
	cases := []struct {
		ecosystem, a, b string
		want            int
	}{
		{"npm", "1.2.3", "1.2.3", 0},
		{"npm", "1.2.3", "1.2.10", -1},
		{"npm", "1.10.0", "1.9.9", 1},
		{"npm", "1.0.0-alpha", "1.0.0", -1},
		{"npm", "1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"npm", "1.0.0-alpha.2", "1.0.0-alpha.10", -1},
		{"npm", "1.0.0-rc.1", "1.0.0-beta.11", 1},
		{"npm", "1.0.0+build.5", "1.0.0", 0},
		{"Go", "v1.83.0", "1.83.0-dev", 1},
		{"Go", "v1.82.1", "1.82.2", -1},
		{"Go", "v0.0.0-20240101000000-abcdef123456", "0.1.0", -1},
		{"Go", "v2.3.4+incompatible", "2.3.4", 0},
		{"PyPI", "2.32.3", "2.32.10", -1},
		{"PyPI", "1.0", "1.0.0", 0},
		{"PyPI", "1.0rc1", "1.0", -1},
		{"PyPI", "1.0a1", "1.0b1", -1},
		{"PyPI", "1.0.dev1", "1.0a1", -1},
		{"PyPI", "1.0.post1", "1.0", 1},
		{"PyPI", "1!0.1", "2.0", 1},
	}
	for _, c := range cases {
		got, ok := compareVersion(c.ecosystem, c.a, c.b)
		if !ok {
			t.Errorf("compareVersion(%s, %q, %q): not comparable", c.ecosystem, c.a, c.b)
			continue
		}
		if got != c.want {
			t.Errorf("compareVersion(%s, %q, %q) = %d, want %d", c.ecosystem, c.a, c.b, got, c.want)
		}
		if back, _ := compareVersion(c.ecosystem, c.b, c.a); back != -c.want {
			t.Errorf("compareVersion(%s, %q, %q) = %d, want %d (antisymmetry)", c.ecosystem, c.b, c.a, back, -c.want)
		}
	}
}

func TestCompareVersion_Unparseable(t *testing.T) {
	for _, c := range [][3]string{
		{"npm", "latest", "1.0.0"},
		{"Go", "v1.2", "1.2.0"},
		{"PyPI", "", "1.0"},
		{"RubyGems", "1.0.0", "1.0.0"},
	} {
		if _, ok := compareVersion(c[0], c[1], c[2]); ok {
			t.Errorf("compareVersion(%s, %q, %q): want not comparable", c[0], c[1], c[2])
		}
	}
}
