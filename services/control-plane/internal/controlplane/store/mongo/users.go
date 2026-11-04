package mongo_store

import (
	"context"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type UserRepo struct{ coll *mongo.Collection }

func NewUserRepo(db *mongo.Database) *UserRepo {
	r := &UserRepo{coll: db.Collection("users")}
	_, _ = r.coll.Indexes().CreateOne(context.Background(), mongo.IndexModel{
		Keys: bson.D{bson.E{Key: "name", Value: 1}}, Options: options.Index().SetUnique(true),
	})
	return r
}

func (r *UserRepo) Create(ctx context.Context, u *domain.User) error {
	if u.ID == "" {
		u.ID = "usr-" + uuidShort()
	}
	_, err := r.coll.InsertOne(ctx, u)
	return err
}

func (r *UserRepo) Get(ctx context.Context, id string) (*domain.User, error) {
	var u domain.User
	return &u, r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&u)
}

func (r *UserRepo) GetByName(ctx context.Context, name string) (*domain.User, error) {
	var u domain.User
	return &u, r.coll.FindOne(ctx, bson.M{"name": name}).Decode(&u)
}

func (r *UserRepo) List(ctx context.Context) ([]*domain.User, error) {
	cur, err := r.coll.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{bson.E{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	out := []*domain.User{}
	return out, cur.All(ctx, &out)
}

func (r *UserRepo) Count(ctx context.Context) (int64, error) {
	return r.coll.CountDocuments(ctx, bson.M{})
}

func (r *UserRepo) SetRole(ctx context.Context, id string, role domain.Role, admin bool) error {
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"role": role, "instanceAdmin": admin}})
	if err == nil && res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

type SessionRepo struct{ coll *mongo.Collection }

func NewSessionRepo(db *mongo.Database) *SessionRepo {
	r := &SessionRepo{coll: db.Collection("sessions")}
	// Mongo removes expired sessions itself.
	_, _ = r.coll.Indexes().CreateOne(context.Background(), mongo.IndexModel{
		Keys: bson.D{bson.E{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0),
	})
	return r
}

func (r *SessionRepo) Create(ctx context.Context, s *domain.Session) error {
	_, err := r.coll.InsertOne(ctx, s)
	return err
}

func (r *SessionRepo) Get(ctx context.Context, hash string) (*domain.Session, error) {
	var s domain.Session
	return &s, r.coll.FindOne(ctx, bson.M{"_id": hash}).Decode(&s)
}

func (r *SessionRepo) Touch(ctx context.Context, hash string, seen, expires time.Time) error {
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": hash}, bson.M{"$set": bson.M{"lastSeenAt": seen, "expiresAt": expires}})
	return err
}

func (r *SessionRepo) Delete(ctx context.Context, hash string) error {
	_, err := r.coll.DeleteOne(ctx, bson.M{"_id": hash})
	return err
}

type TokenRepo struct{ coll *mongo.Collection }

func NewTokenRepo(db *mongo.Database) *TokenRepo {
	r := &TokenRepo{coll: db.Collection("apiTokens")}
	_, _ = r.coll.Indexes().CreateOne(context.Background(), mongo.IndexModel{
		Keys: bson.D{bson.E{Key: "hash", Value: 1}}, Options: options.Index().SetUnique(true),
	})
	return r
}

func (r *TokenRepo) Create(ctx context.Context, t *domain.APIToken) error {
	if t.ID == "" {
		t.ID = "tok-" + uuidShort()
	}
	_, err := r.coll.InsertOne(ctx, t)
	return err
}

func (r *TokenRepo) GetByHash(ctx context.Context, hash string) (*domain.APIToken, error) {
	var t domain.APIToken
	return &t, r.coll.FindOne(ctx, bson.M{"hash": hash}).Decode(&t)
}

func (r *TokenRepo) ListByUser(ctx context.Context, userID string) ([]*domain.APIToken, error) {
	cur, err := r.coll.Find(ctx, bson.M{"userId": userID}, options.Find().SetSort(bson.D{bson.E{Key: "createdAt", Value: -1}}))
	if err != nil {
		return nil, err
	}
	out := []*domain.APIToken{}
	return out, cur.All(ctx, &out)
}

func (r *TokenRepo) Touch(ctx context.Context, id string, at time.Time) error {
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"lastUsedAt": at}})
	return err
}

// Revoke marks a user's token revoked; false when it is not theirs or already revoked.
func (r *TokenRepo) Revoke(ctx context.Context, id, userID string, at time.Time) (bool, error) {
	res, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": id, "userId": userID, "revokedAt": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"revokedAt": at}})
	if err != nil {
		return false, err
	}
	return res.ModifiedCount == 1, nil
}
