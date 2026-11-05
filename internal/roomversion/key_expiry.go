package roomversion

// IgnoresKeyExpiry reports whether a room version ignores the expiry that a signing
// key document carries.
//
// Room versions 1 to 4 predate the idea of a server key that goes stale: their event
// authorisation rules say nothing about it, and the Server-Server API states that
// `valid_until_ts` MUST be ignored for those versions. Enforcing it there would reject
// events in an old room for a reason its own rules never had — so a server that
// enforces it everywhere is not more secure, it is simply unable to read those rooms.
func IgnoresKeyExpiry(rules Rules) bool {
	switch rules.ID {
	case "1", "2", "3", "4", "V1.0":
		return true
	default:
		return false
	}
}
