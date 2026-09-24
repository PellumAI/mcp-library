package audit

import "strings"

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
// expression of them. `A OR B` permits redistribution if either operand
// does, since the redistributor may choose the favourable term; `A AND B`
// takes the worse of the two, since both terms bind together. Parentheses
// are stripped naively before splitting on OR/AND, which is exact for the
// expressions this library's manifests actually carry.
func ClassifyLicense(spdx string) LicenseClass {
	expr := strings.NewReplacer("(", "", ")", "").Replace(spdx)
	return classifyExpr(expr)
}

func classifyExpr(expr string) LicenseClass {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return LicenseReview
	}

	if parts := strings.Split(expr, " OR "); len(parts) > 1 {
		best := LicenseRefuses
		for _, part := range parts {
			if c := classifyExpr(part); licenseRank(c) < licenseRank(best) {
				best = c
			}
		}
		return best
	}

	if parts := strings.Split(expr, " AND "); len(parts) > 1 {
		worst := LicensePermits
		for _, part := range parts {
			if c := classifyExpr(part); licenseRank(c) > licenseRank(worst) {
				worst = c
			}
		}
		return worst
	}

	return classifyLicenseID(expr)
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
