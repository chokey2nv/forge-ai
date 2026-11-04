package domain

import "time"

// User is a person who signs in to Forge (Phase 13.2). Agents are never
// users: they act only through their runs' callback tokens.
type User struct {
	ID            string `bson:"_id" json:"id"`
	Name          string `bson:"name" json:"name"` // unique, used to sign in
	Email         string `bson:"email,omitempty" json:"email,omitempty"`
	PasswordHash  string `bson:"passwordHash" json:"-"`
	InstanceAdmin bool   `bson:"instanceAdmin" json:"instanceAdmin"`
	// Role is what the user may do across the instance (Phase 13.3); 13.4
	// scopes it per project. Instance admins may do everything.
	Role       Role       `bson:"role,omitempty" json:"role"`
	CreatedAt  time.Time  `bson:"createdAt" json:"createdAt"`
	DisabledAt *time.Time `bson:"disabledAt,omitempty" json:"disabledAt,omitempty"`
}

// Session is a signed-in console. Only a hash of its cookie value is kept.
type Session struct {
	Hash       string    `bson:"_id" json:"-"`
	UserID     string    `bson:"userId" json:"userId"`
	CreatedAt  time.Time `bson:"createdAt" json:"createdAt"`
	LastSeenAt time.Time `bson:"lastSeenAt" json:"lastSeenAt"`
	ExpiresAt  time.Time `bson:"expiresAt" json:"expiresAt"`
}

// APIToken lets a script act as its user. Only a hash is kept; the token is
// shown once, when it is created.
type APIToken struct {
	ID         string     `bson:"_id" json:"id"`
	UserID     string     `bson:"userId" json:"userId"`
	Name       string     `bson:"name" json:"name"`
	Hash       string     `bson:"hash" json:"-"`
	CreatedAt  time.Time  `bson:"createdAt" json:"createdAt"`
	LastUsedAt *time.Time `bson:"lastUsedAt,omitempty" json:"lastUsedAt,omitempty"`
	RevokedAt  *time.Time `bson:"revokedAt,omitempty" json:"revokedAt,omitempty"`
}

// Role is a level of trust; each includes the ones before it.
type Role string

const (
	RoleViewer   Role = "viewer"   // read tasks, diffs, threads
	RoleMember   Role = "member"   // also create and start tasks, comment
	RoleReviewer Role = "reviewer" // also decide: approve, reject, request changes, escalations, roll back, resolve threads
)

var roleRank = map[Role]int{RoleViewer: 1, RoleMember: 2, RoleReviewer: 3}

// ValidRole reports whether r names a role.
func ValidRole(r Role) bool { return roleRank[r] > 0 }

// EffectiveRole is the user's role; users from before roles are members.
func (u *User) EffectiveRole() Role {
	if u.InstanceAdmin {
		return RoleReviewer
	}
	if ValidRole(u.Role) {
		return u.Role
	}
	return RoleMember
}

// Can reports whether the user has at least role min.
func (u *User) Can(min Role) bool {
	return u.InstanceAdmin || roleRank[u.EffectiveRole()] >= roleRank[min]
}
