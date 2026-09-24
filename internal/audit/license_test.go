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
	}

	for _, tc := range cases {
		if got := ClassifyLicense(tc.spdx); got != tc.want {
			t.Errorf("ClassifyLicense(%q) = %q, want %q", tc.spdx, got, tc.want)
		}
	}
}
