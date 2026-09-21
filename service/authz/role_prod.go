package authz

import (
	"slices"

	"github.com/QuantumNous/new-api/common"
)

// BuiltInRoleMod is prod's moderator (common.RoleModUser). Upstream maps only
// admin and root into authz, so without this a moderator resolves to no role and
// Can refuses every permission, including grants made to them explicitly.
const BuiltInRoleMod = "mod"

// modDefaultPermissions is what every moderator holds without an explicit grant.
var modDefaultPermissions = []Permission{AuditRead}

func init() {
	builtInRoles = append(builtInRoles, RoleSpec{
		Key:         BuiltInRoleMod,
		Name:        "Moderator",
		Description: "Built-in moderator authorization role",
		BuiltIn:     true,
		Sort:        20,
	})

	upstreamResolve := resolveSubjectRoles
	resolveSubjectRoles = func(userID int, systemRole int) []string {
		if systemRole >= common.RoleModUser && systemRole < common.RoleAdminUser {
			return []string{BuiltInRoleMod}
		}
		return upstreamResolve(userID, systemRole)
	}

	for i := range registry {
		for j := range registry[i].Actions {
			action := &registry[i].Actions[j]
			granted := slices.Contains(modDefaultPermissions, Permission{Resource: registry[i].Resource, Action: action.Action})
			if granted && !slices.Contains(action.DefaultRoles, BuiltInRoleMod) {
				action.DefaultRoles = append(action.DefaultRoles, BuiltInRoleMod)
			}
		}
	}
}
