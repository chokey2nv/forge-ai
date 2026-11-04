package domain

import (
	"path/filepath"
	"time"
)

// RoleAdmin manages a project: its repositories and members (Phase 13.4).
// It is a project role only; instance admins are User.InstanceAdmin.
const RoleAdmin Role = "admin"

func init() { roleRank[RoleAdmin] = 4 }

// ValidProjectRole reports whether r can be given to a project member.
func ValidProjectRole(r Role) bool { return ValidRole(r) || r == RoleAdmin }

// Project is the unit of tenancy (Phase 13.4): the repositories its tasks
// may work in, and who may do what with them.
type Project struct {
	ID        string          `bson:"_id" json:"id"`
	Name      string          `bson:"name" json:"name"`
	Repos     []ProjectRepo   `bson:"repos" json:"repos"`
	Members   []ProjectMember `bson:"members" json:"members"`
	CreatedAt time.Time       `bson:"createdAt" json:"createdAt"`
}

type ProjectRepo struct {
	Path string `bson:"path" json:"path"` // absolute, on the control plane's machine
	// DefaultTarget is the branch new tasks deliver to unless they say; empty
	// is the repository's checked-out branch.
	DefaultTarget string    `bson:"defaultTarget,omitempty" json:"defaultTarget,omitempty"`
	AddedAt       time.Time `bson:"addedAt" json:"addedAt"`
}

type ProjectMember struct {
	UserID string `bson:"userId" json:"userId"`
	Name   string `bson:"name" json:"name"` // at the time they were added
	Role   Role   `bson:"role" json:"role"`
}

// RoleOf is a user's role in the project; "" when they are not a member.
// Instance admins act as project admins everywhere.
func (p *Project) RoleOf(u *User) Role {
	if u.InstanceAdmin {
		return RoleAdmin
	}
	for _, m := range p.Members {
		if m.UserID == u.ID {
			return m.Role
		}
	}
	return ""
}

// Allows reports whether role r includes min.
func (r Role) Allows(min Role) bool { return roleRank[r] >= roleRank[min] && roleRank[min] > 0 }

// HasRepo reports whether path is one of the project's repositories
// (compared cleaned: "a//b/" is "a/b").
func (p *Project) HasRepo(path string) bool {
	path = filepath.Clean(path)
	for _, r := range p.Repos {
		if filepath.Clean(r.Path) == path {
			return true
		}
	}
	return false
}
