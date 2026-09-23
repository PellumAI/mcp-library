package index

import (
	"fmt"
	"slices"
)

// Merge folds newly built versions into a previously published index. Its
// entire job is one rule, and the rule is why this function exists rather than
// a map assignment: a published digest is immutable. If prev already lists
// name@version for os/arch under a different sha256, Merge refuses.
//
// Merge carries forward every package and version prev holds that next does
// not mention, because a normal publish builds only the servers a pull
// request touched. Removing a version is therefore never a side effect of a
// build; it is Retire, a separate act. A version rebuilt to the same digests
// keeps its original published_at and its original manifest bytes.
//
// next's generated_at, library and signing_keys win.
func Merge(prev, next Index) (Index, error) {
	if prev.SchemaVersion > SchemaVersion {
		return Index{}, fmt.Errorf("index: the previous index is schema_version %d, newer than this tool writes, which is %d", prev.SchemaVersion, SchemaVersion)
	}
	out := Index{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   next.GeneratedAt,
		Library:       next.Library,
		SigningKeys:   slices.Clone(next.SigningKeys),
	}
	byName := map[string]*Package{}
	for _, p := range prev.Packages {
		cp := p
		cp.Versions = slices.Clone(p.Versions)
		out.Packages = append(out.Packages, cp)
	}
	for i := range out.Packages {
		byName[out.Packages[i].Name] = &out.Packages[i]
	}
	for _, np := range next.Packages {
		pp, ok := byName[np.Name]
		if !ok {
			out.Packages = append(out.Packages, np)
			byName = reindex(out.Packages)
			continue
		}
		// The human-facing fields follow the newest recipe.
		pp.Title, pp.Description, pp.Homepage, pp.License, pp.Categories = np.Title, np.Description, np.Homepage, np.License, np.Categories
		for _, nv := range np.Versions {
			vi := slices.IndexFunc(pp.Versions, func(v Version) bool { return v.Version == nv.Version })
			if vi < 0 {
				pp.Versions = append(pp.Versions, nv)
				continue
			}
			merged, err := mergeVersion(np.Name, pp.Versions[vi], nv)
			if err != nil {
				return Index{}, err
			}
			pp.Versions[vi] = merged
		}
	}
	Canonicalise(&out)
	return out, nil
}

func reindex(ps []Package) map[string]*Package {
	m := make(map[string]*Package, len(ps))
	for i := range ps {
		m[ps[i].Name] = &ps[i]
	}
	return m
}

func mergeVersion(name string, prev, next Version) (Version, error) {
	out := prev
	out.Blobs = slices.Clone(prev.Blobs)
	for _, nb := range next.Blobs {
		i := slices.IndexFunc(out.Blobs, func(b Blob) bool { return b.OS == nb.OS && b.Arch == nb.Arch })
		if i < 0 {
			out.Blobs = append(out.Blobs, nb)
			continue
		}
		if pb := out.Blobs[i]; pb.SHA256 != nb.SHA256 {
			return Version{}, fmt.Errorf(
				"index: %s@%s %s/%s is already published as sha256:%s and the new build is sha256:%s; "+
					"a published digest is immutable -- publish a new version instead, and see "+
					"docs/REPRODUCIBILITY.md if you did not expect the bytes to change",
				name, prev.Version, nb.OS, nb.Arch, pb.SHA256, nb.SHA256)
		}
	}
	return out, nil
}
