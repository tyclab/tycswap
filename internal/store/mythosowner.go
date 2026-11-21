package store

import "strconv"

// MythosLoginOwner resolves a bound directory independently of the next start's
// selection. Changing a mark or disabling a login does not rebind its sessions.
func MythosLoginOwner(data *SequenceData, digest string) string {
	if data == nil {
		return ""
	}
	owner := ""
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		rec, _ := recordFor(data, num)
		email, hasEmail := rec["email"].(string)
		org, hasOrg := rec["organizationUuid"].(string)
		if !hasEmail || email == "" || !hasOrg || AccountIdentityDigest(email, org) != digest {
			continue
		}
		if owner != "" { // Two slots claim the same login.
			return ""
		}
		owner = num
	}
	return owner
}
