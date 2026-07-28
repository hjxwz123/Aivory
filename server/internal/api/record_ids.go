package api

import "strings"

// validPreparedRecordID accepts the same prefix + 48-bit lowercase-hex shape
// minted by store.GenID. IDs supplied by admin create APIs become public URL
// path segments, so keep their grammar narrow and stable.
func validPreparedRecordID(value, prefix string) bool {
	value = strings.TrimSpace(value)
	wantPrefix := prefix + "_"
	if len(value) != len(wantPrefix)+12 || !strings.HasPrefix(value, wantPrefix) {
		return false
	}
	for _, ch := range value[len(wantPrefix):] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}
