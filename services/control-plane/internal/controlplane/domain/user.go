package domain

import "time"

// User is a person who signs in to Forge (Phase 13.2). Agents are never
// users: they act only through their runs' callback tokens.
type User struct {
	ID            string     `bson:"_id" json:"id"`
	Name          string     `bson:"name" json:"name"` // unique, used to sign in
	Email         string     `bson:"email,omitempty" json:"email,omitempty"`
	PasswordHash  string     `bson:"passwordHash" json:"-"`
	InstanceAdmin bool       `bson:"instanceAdmin" json:"instanceAdmin"`
	CreatedAt     time.Time  `bson:"createdAt" json:"createdAt"`
	DisabledAt    *time.Time `bson:"disabledAt,omitempty" json:"disabledAt,omitempty"`
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
