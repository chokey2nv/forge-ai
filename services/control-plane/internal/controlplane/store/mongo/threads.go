package mongo_store

import (
	"context"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type ThreadRepo struct{ coll *mongo.Collection }

func NewThreadRepo(db *mongo.Database) *ThreadRepo {
	r := &ThreadRepo{coll: db.Collection("threads")}
	_, _ = r.coll.Indexes().CreateOne(context.Background(), mongo.IndexModel{
		Keys: bson.D{bson.E{Key: "taskId", Value: 1}, bson.E{Key: "createdAt", Value: 1}},
	})
	return r
}

func (r *ThreadRepo) Create(ctx context.Context, t *domain.Thread) error {
	if t.ID == "" {
		t.ID = "thr-" + uuidShort()
	}
	_, err := r.coll.InsertOne(ctx, t)
	return err
}

func (r *ThreadRepo) Get(ctx context.Context, id string) (*domain.Thread, error) {
	var t domain.Thread
	if err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *ThreadRepo) ListByTask(ctx context.Context, taskID string) ([]*domain.Thread, error) {
	cur, err := r.coll.Find(ctx, bson.M{"taskId": taskID}, options.Find().SetSort(bson.D{bson.E{Key: "createdAt", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var out []*domain.Thread
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []*domain.Thread{}
	}
	return out, nil
}

func (r *ThreadRepo) AddComment(ctx context.Context, id string, c domain.Comment) error {
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$push": bson.M{"comments": c},
		"$set":  bson.M{"updatedAt": c.At},
	})
	if err == nil && res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

func (r *ThreadRepo) SetStatus(ctx context.Context, id string, status domain.ThreadStatus, by string, at time.Time) error {
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$set": bson.M{"status": status, "resolvedBy": by, "updatedAt": at},
	})
	if err == nil && res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}
