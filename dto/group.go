package dto

// UserGroupInfo describes a single usable group for the current user.
type UserGroupInfo struct {
	Ratio any    `json:"ratio"`
	Desc  string `json:"desc"`
	// Online reports whether any channel behind the group is enabled right now.
	// A group can be usable and priced yet have every channel auto-disabled, and
	// pinning to one of those fails every request until a channel recovers.
	Online bool `json:"online"`
}
