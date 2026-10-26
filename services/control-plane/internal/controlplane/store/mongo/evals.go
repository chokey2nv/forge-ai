package mongo_store

import (
	"context"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type EvalResultRepo struct{ coll *mongo.Collection }

func NewEvalResultRepo(db *mongo.Database) *EvalResultRepo {
	r := &EvalResultRepo{coll: db.Collection("evalResults")}
	_, _ = r.coll.Indexes().CreateOne(context.Background(), mongo.IndexModel{
		Keys: bson.D{{Key: "model", Value: 1}, {Key: "startedAt", Value: -1}},
	})
	return r
}

func (r *EvalResultRepo) Create(ctx context.Context, e *domain.EvalResult) error {
	if e.ID == "" {
		e.ID = "eval-" + uuidShort()
	}
	_, err := r.coll.InsertOne(ctx, e)
	return err
}

func (r *EvalResultRepo) List(ctx context.Context, model string, limit int) ([]*domain.EvalResult, error) {
	filter := bson.M{}
	if model != "" {
		filter["model"] = model
	}
	opts := options.Find().SetSort(bson.D{{Key: "startedAt", Value: -1}})
	if limit > 0 {
		opts.SetLimit(int64(limit))
	}
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	var out []*domain.EvalResult
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []*domain.EvalResult{}
	}
	return out, nil
}

func (r *EvalResultRepo) Get(ctx context.Context, id string) (*domain.EvalResult, error) {
	var e domain.EvalResult
	if err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&e); err != nil {
		return nil, err
	}
	return &e, nil
}
