package audit

import (
	"fmt"
	"strings"
)

// LicenseClass is how a licence, or an SPDX expression of licences, is
// treated for redistribution: permits it outright, needs a human to review
// it, or refuses it.
type LicenseClass string

const (
	LicensePermits LicenseClass = "permits"
	LicenseReview  LicenseClass = "review"
	LicenseRefuses LicenseClass = "refuses"
)

// licensePermits are SPDX ids, matched case-insensitively, known to permit
// redistribution outright.
var licensePermits = map[string]bool{
	"MIT":          true,
	"APACHE-2.0":   true,
	"BSD-2-CLAUSE": true,
	"BSD-3-CLAUSE": true,
	"ISC":          true,
	"MPL-2.0":      true,
}

// licenseRefusesPrefixes are SPDX id families, matched case-insensitively,
// that refuse redistribution: copyleft licences this library can't satisfy
// by shipping a package tar.
var licenseRefusesPrefixes = []string{"GPL", "AGPL", "SSPL", "BUSL", "FSL"}

// ClassifyLicense classifies a single SPDX licence id, or an SPDX
// expression of them, with standard SPDX precedence: AND binds tighter
// than OR, and parentheses group. `A OR B` takes the best (most
// permissive) operand, since the redistributor may choose the favourable
// term; `A AND B` takes the worst, since both terms bind together and must
// both be honoured. `<id> WITH <exception>` attaches the exception to the
// licence id without changing its class. A malformed expression —
// unbalanced parentheses, a dangling operator, anything the grammar
// doesn't accept — classifies as review rather than erroring, since a
// human still needs to look at the licence field either way.
func ClassifyLicense(spdx string) LicenseClass {
	tokens := tokenizeSPDX(spdx)
	if len(tokens) == 0 {
		return LicenseReview
	}

	p := &spdxParser{tokens: tokens}
	class, err := p.parseExpr()
	if err != nil || p.pos != len(p.tokens) {
		return LicenseReview
	}
	return class
}

// tokenizeSPDX splits an SPDX expression into ids, keywords (OR/AND/WITH)
// and lone "(" / ")" tokens, on whitespace and parenthesis boundaries.
func tokenizeSPDX(expr string) []string {
	var tokens []string
	var cur strings.Builder

	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range expr {
		switch r {
		case '(', ')':
			flush()
			tokens = append(tokens, string(r))
		case ' ', '\t', '\n', '\r':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return tokens
}

// spdxParser is a recursive-descent parser over the grammar:
//
//	orExpr  := andExpr ( "OR" andExpr )*
//	andExpr := term ( "AND" term )*
//	term    := "(" orExpr ")" | id ( "WITH" id )?
type spdxParser struct {
	tokens []string
	pos    int
}

func (p *spdxParser) peek() string {
	if p.pos >= len(p.tokens) {
		return ""
	}
	return p.tokens[p.pos]
}

func (p *spdxParser) next() string {
	t := p.peek()
	p.pos++
	return t
}

func (p *spdxParser) parseExpr() (LicenseClass, error) {
	return p.parseOr()
}

func (p *spdxParser) parseOr() (LicenseClass, error) {
	best, err := p.parseAnd()
	if err != nil {
		return "", err
	}
	for p.peek() == "OR" {
		p.next()
		operand, err := p.parseAnd()
		if err != nil {
			return "", err
		}
		if licenseRank(operand) < licenseRank(best) {
			best = operand
		}
	}
	return best, nil
}

func (p *spdxParser) parseAnd() (LicenseClass, error) {
	worst, err := p.parseTerm()
	if err != nil {
		return "", err
	}
	for p.peek() == "AND" {
		p.next()
		operand, err := p.parseTerm()
		if err != nil {
			return "", err
		}
		if licenseRank(operand) > licenseRank(worst) {
			worst = operand
		}
	}
	return worst, nil
}

func (p *spdxParser) parseTerm() (LicenseClass, error) {
	if p.peek() == "(" {
		p.next()
		class, err := p.parseOr()
		if err != nil {
			return "", err
		}
		if p.peek() != ")" {
			return "", fmt.Errorf("audit: spdx: unbalanced parentheses")
		}
		p.next()
		return class, nil
	}

	id := p.next()
	if id == "" || id == "OR" || id == "AND" || id == "WITH" || id == ")" {
		return "", fmt.Errorf("audit: spdx: expected a licence id, got %q", id)
	}

	if p.peek() == "WITH" {
		p.next()
		exception := p.next()
		if exception == "" || exception == "OR" || exception == "AND" || exception == "WITH" || exception == ")" {
			return "", fmt.Errorf("audit: spdx: expected an exception id after WITH")
		}
		// The exception attaches to id but never changes its class.
	}

	return classifyLicenseID(id), nil
}

func classifyLicenseID(id string) LicenseClass {
	id = strings.TrimSpace(id)
	if id == "" {
		return LicenseReview
	}

	upper := strings.ToUpper(id)
	if upper == "UNLICENSED" {
		return LicenseRefuses
	}
	for _, prefix := range licenseRefusesPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return LicenseRefuses
		}
	}
	if licensePermits[upper] {
		return LicensePermits
	}
	return LicenseReview
}

// licenseRank orders classes from most to least permissive, so OR can take
// the minimum and AND the maximum across operands.
func licenseRank(c LicenseClass) int {
	switch c {
	case LicensePermits:
		return 0
	case LicenseRefuses:
		return 2
	default:
		return 1
	}
}
