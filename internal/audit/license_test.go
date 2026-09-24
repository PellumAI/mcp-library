package audit

import "testing"

func TestClassifyLicense(t *testing.T) {
	cases := []struct {
		spdx string
		want LicenseClass
	}{
		{"MIT", LicensePermits},
		{"Apache-2.0", LicensePermits},
		{"BSD-2-Clause", LicensePermits},
		{"BSD-3-Clause", LicensePermits},
		{"ISC", LicensePermits},
		{"MPL-2.0", LicensePermits},
		{"GPL-3.0-only", LicenseRefuses},
		{"AGPL-3.0-or-later", LicenseRefuses},
		{"SSPL-1.0", LicenseRefuses},
		{"BUSL-1.1", LicenseRefuses},
		{"FSL-1.1-MIT", LicenseRefuses},
		{"UNLICENSED", LicenseRefuses},
		{"", LicenseReview},
		{"WTFPL", LicenseReview},
		{"Apache-2.0 OR MIT", LicensePermits},
		{"GPL-3.0-only OR MIT", LicensePermits},
		{"GPL-3.0-only AND MIT", LicenseRefuses},
		{"MIT AND (Apache-2.0 OR BSD-3-Clause)", LicensePermits},
		// AND binds tighter than OR: without the parens this is
		// GPL AND (MIT OR Apache-2.0), which must refuse since the GPL
		// term is unconditional.
		{"GPL-3.0-only AND (MIT OR Apache-2.0)", LicenseRefuses},
		// The parens force the OR to bind first, so this is
		// (permits) AND OFL-1.1 (unrecognised, review) = review.
		{"(BSD-2-Clause OR MIT) AND OFL-1.1", LicenseReview},
		// Unbalanced parentheses: malformed, not an error.
		{"(MIT OR Apache-2.0", LicenseReview},
		// Dangling operator: malformed, not an error.
		{"MIT OR", LicenseReview},
		// WITH attaches an exception to a licence id without changing its
		// class, in either direction.
		{"Apache-2.0 WITH LLVM-exception", LicensePermits},
		{"GPL-3.0-only WITH Classpath-exception-2.0", LicenseRefuses},
	}

	for _, tc := range cases {
		if got := ClassifyLicense(tc.spdx); got != tc.want {
			t.Errorf("ClassifyLicense(%q) = %q, want %q", tc.spdx, got, tc.want)
		}
	}
}
