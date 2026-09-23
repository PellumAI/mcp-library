package site

import (
	"fmt"
	"io/fs"
	"path/filepath"
)

// DefaultBudgetMiB keeps the assembled site well under the one-gigabyte
// published-site limit GitHub Pages applies.
const DefaultBudgetMiB = 900

// CheckBudget walks the assembled tree and fails when it is over budgetMiB.
// The message names the limit and both ways out, because a guard whose
// message does not say what to do is a guard that gets raised rather than
// obeyed.
func CheckBudget(dir string, budgetMiB int64) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			total += fi.Size()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	gotMiB := total >> 20
	if total > budgetMiB<<20 {
		return total, fmt.Errorf(
			"site: the assembled site is %d MiB and the budget is %d MiB. Pages is the gateway's "+
				"library_source and it has a 1 GiB ceiling. Either retire old versions with "+
				"mcplib index --retire, or move library_source to a host with no ceiling -- the "+
				"blobs are already on GHCR by digest and the contract is four static paths, so "+
				"any object store or CDN in front of it serves them with no gateway change",
			gotMiB, budgetMiB)
	}
	return total, nil
}
