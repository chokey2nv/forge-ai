package mongo_store

import (
	"context"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type ReviewRepo struct{ coll *mongo.Collection }

func NewReviewRepo(db *mongo.Database) *ReviewRepo {
	r := &ReviewRepo{coll: db.Collection("reviews")}
	_, _ = r.coll.Indexes().CreateOne(context.Background(), mongo.IndexModel{
		Keys: bson.D{bson.E{Key: "runId", Value: 1}, bson.E{Key: "round", Value: 1}, bson.E{Key: "index", Value: 1}},
	})
	return r
}

// Create upserts by ID, so a duplicate callback for the same reviewer slot
// cannot produce two verdicts.
func (r *ReviewRepo) Create(ctx context.Context, rv *domain.Review) error {
	_, err := r.coll.ReplaceOne(ctx, bson.M{"_id": rv.ID}, rv, options.Replace().SetUpsert(true))
	return err
}

func (r *ReviewRepo) ListByRun(ctx context.Context, runID string) ([]*domain.Review, error) {
	cur, err := r.coll.Find(ctx, bson.M{"runId": runID},
		options.Find().SetSort(bson.D{bson.E{Key: "round", Value: 1}, bson.E{Key: "index", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var out []*domain.Review
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []*domain.Review{}
	}
	return out, nil
}
