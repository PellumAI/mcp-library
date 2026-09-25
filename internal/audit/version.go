package audit

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// compareVersion orders two versions of one package the way its ecosystem
// does: semver for npm and Go (Go's leading "v" and "+incompatible" are
// tolerated), a PEP 440 subset for PyPI. It returns -1, 0 or 1, and ok is
// false when either version does not parse or the ecosystem has no
// ordering here — the caller must then not pretend to know which is newer.
func compareVersion(ecosystem, a, b string) (int, bool) {
	switch ecosystem {
	case "npm", "Go":
		va, okA := parseSemver(a)
		vb, okB := parseSemver(b)
		if !okA || !okB {
			return 0, false
		}
		return compareSemver(va, vb), true
	case "PyPI":
		va, okA := parsePEP440(a)
		vb, okB := parsePEP440(b)
		if !okA || !okB {
			return 0, false
		}
		return comparePEP440(va, vb), true
	default:
		return 0, false
	}
}

type semver struct {
	core [3]int
	pre  []string
}

func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i] // build metadata never takes part in ordering
	}
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var v semver
	for i, p := range parts {
		n, ok := atoiDigits(p)
		if !ok {
			return semver{}, false
		}
		v.core[i] = n
	}
	if hasPre {
		if pre == "" {
			return semver{}, false
		}
		v.pre = strings.Split(pre, ".")
	}
	return v, true
}

func compareSemver(a, b semver) int {
	for i := range a.core {
		if c := cmpInt(a.core[i], b.core[i]); c != 0 {
			return c
		}
	}
	// A pre-release sorts before its release.
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		na, numA := atoiDigits(a.pre[i])
		nb, numB := atoiDigits(b.pre[i])
		var c int
		switch {
		case numA && numB:
			c = cmpInt(na, nb)
		case numA:
			c = -1 // numeric identifiers sort before alphanumeric ones
		case numB:
			c = 1
		default:
			c = strings.Compare(a.pre[i], b.pre[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmpInt(len(a.pre), len(b.pre))
}

// pep440 is a version reduced to PEP 440's sort key: epoch, release, then
// pre, post and dev, each already mapped so a plain integer compare orders
// them (an absent pre sorts after any pre-release, a dev release of a final
// version before its pre-releases).
type pep440 struct {
	epoch     int
	release   []int
	preRank   int
	preNum    int
	post, dev int
}

var pep440RE = regexp.MustCompile(`^v?(?:(\d+)!)?(\d+(?:\.\d+)*)` +
	`(?:[-_.]?(a|b|c|rc|alpha|beta|pre|preview)[-_.]?(\d*))?` +
	`(?:(?:[-_.]?(post|rev|r)[-_.]?(\d*))|-(\d+))?` +
	`(?:[-_.]?(dev)[-_.]?(\d*))?` +
	`(?:\+[a-z0-9._-]+)?$`)

func parsePEP440(s string) (pep440, bool) {
	m := pep440RE.FindStringSubmatch(strings.ToLower(strings.TrimSpace(s)))
	if m == nil {
		return pep440{}, false
	}
	var v pep440
	if m[1] != "" {
		v.epoch, _ = strconv.Atoi(m[1])
	}
	for _, p := range strings.Split(m[2], ".") {
		n, _ := strconv.Atoi(p)
		v.release = append(v.release, n)
	}

	hasPre, hasPost, hasDev := m[3] != "", m[5] != "" || m[7] != "", m[8] != ""
	switch {
	case hasPre:
		switch m[3] {
		case "a", "alpha":
			v.preRank = 0
		case "b", "beta":
			v.preRank = 1
		default: // c, rc, pre, preview
			v.preRank = 2
		}
		v.preNum, _ = strconv.Atoi(m[4])
	case !hasPost && hasDev:
		v.preRank = math.MinInt // 1.0.dev1 < 1.0a1
	default:
		v.preRank = math.MaxInt
	}
	switch {
	case m[7] != "":
		v.post, _ = strconv.Atoi(m[7])
	case hasPost:
		v.post, _ = strconv.Atoi(m[6])
	default:
		v.post = math.MinInt
	}
	if hasDev {
		v.dev, _ = strconv.Atoi(m[9])
	} else {
		v.dev = math.MaxInt
	}
	return v, true
}

func comparePEP440(a, b pep440) int {
	if c := cmpInt(a.epoch, b.epoch); c != 0 {
		return c
	}
	for i := 0; i < len(a.release) || i < len(b.release); i++ {
		var x, y int // 1.0 == 1.0.0: missing segments are zero
		if i < len(a.release) {
			x = a.release[i]
		}
		if i < len(b.release) {
			y = b.release[i]
		}
		if c := cmpInt(x, y); c != 0 {
			return c
		}
	}
	for _, pair := range [][2]int{{a.preRank, b.preRank}, {a.preNum, b.preNum}, {a.post, b.post}, {a.dev, b.dev}} {
		if c := cmpInt(pair[0], pair[1]); c != 0 {
			return c
		}
	}
	return 0
}

// atoiDigits parses s only if it is a non-empty run of ASCII digits, so a
// sign or a space never passes for a version number.
func atoiDigits(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
