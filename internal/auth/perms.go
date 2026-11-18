package auth

import (
	"sort"

	"github.com/emirhanusta/kymo/internal/model"
)

// Permission strings reported by GET /auth/me.
const (
	PermWorkloadsManage = "workloads:manage"
	PermLogCapture      = "logcapture:manage"
	PermSourcesManage   = "sources:manage"
	PermChannelsManage  = "channels:manage"
	PermUsersManage     = "users:manage"
	PermTokensManageAll = "tokens:manage_all"
	PermAuditRead       = "audit:read"
	PermOwner           = "owner"
)

// permMinRole maps each permission to the lowest role that holds it.
var permMinRole = map[string]model.Role{
	PermWorkloadsManage: model.RoleOperator,
	PermLogCapture:      model.RoleOperator,
	PermSourcesManage:   model.RoleAdmin,
	PermChannelsManage:  model.RoleAdmin,
	PermUsersManage:     model.RoleAdmin,
	PermTokensManageAll: model.RoleAdmin,
	PermAuditRead:       model.RoleAdmin,
	PermOwner:           model.RoleOwner,
}

// Can reports whether role holds the permission.
func Can(role model.Role, perm string) bool {
	min, ok := permMinRole[perm]
	return ok && role.AtLeast(min)
}

// Permissions lists the permissions of role, sorted.
func Permissions(role model.Role) []string {
	out := []string{}
	for p := range permMinRole {
		if Can(role, p) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
