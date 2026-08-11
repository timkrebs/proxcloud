package handlers

import "regexp"

// pveIDRe covers PVE identifiers (node, storage, bridge, pool): must start
// alphanumeric, then alphanumerics plus . - _ ; explicitly excludes path
// dots-only names so nothing can smuggle a dot segment into a PVE URL.
var pveIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidPVEID reports whether s is a safe PVE identifier for URL paths and
// composite config values.
func ValidPVEID(s string) bool {
	if s == "." || s == ".." {
		return false
	}
	return pveIDRe.MatchString(s)
}
