package updates

import (
	"regexp"
	"strconv"
	"strings"
)

// version is a tag such as 1.27, v2.3.4 or 17-alpine: a prefix ("v" or
// none), numbers, and a suffix. Tags of the same shape compare.
type version struct {
	prefix, suffix string
	nums           []int
}

var versionRe = regexp.MustCompile(`^(v?)(\d+(?:\.\d+){0,3})(.*)$`)

func parseVersion(tag string) (version, bool) {
	m := versionRe.FindStringSubmatch(tag)
	if m == nil {
		return version{}, false
	}
	v := version{prefix: m[1], suffix: m[3]}
	// A suffix must start a new part: 1.2rc1 or 20231105 are not versions.
	if v.suffix != "" && v.suffix[0] != '-' && v.suffix[0] != '_' {
		return version{}, false
	}
	for _, p := range strings.Split(m[2], ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return version{}, false
		}
		v.nums = append(v.nums, n)
	}
	// A date-like first number (20231105) is a build, not a version.
	if v.nums[0] >= 1000 {
		return version{}, false
	}
	return v, true
}

func (v version) sameShape(o version) bool {
	return v.prefix == o.prefix && v.suffix == o.suffix && len(v.nums) == len(o.nums)
}

func (v version) less(o version) bool {
	for i := range v.nums {
		if v.nums[i] != o.nums[i] {
			return v.nums[i] < o.nums[i]
		}
	}
	return false
}

// newer finds, among tags, the latest one of the same shape as current
// within its major version (minor), and the latest one overall when its
// major is higher (major). Prereleases and variants have another suffix
// and are left out by the shape.
func newer(current string, tags []string) (minor, major string) {
	cur, ok := parseVersion(current)
	if !ok {
		return "", ""
	}
	var bestMinor, bestMajor version
	for _, t := range tags {
		v, ok := parseVersion(t)
		if !ok || !v.sameShape(cur) || !cur.less(v) {
			continue
		}
		if v.nums[0] == cur.nums[0] {
			if minor == "" || bestMinor.less(v) {
				bestMinor, minor = v, t
			}
		} else if major == "" || bestMajor.less(v) {
			bestMajor, major = v, t
		}
	}
	return minor, major
}
