package mongo_store

import (
	"context"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type SpendRepo struct{ coll *mongo.Collection }

func NewSpendRepo(db *mongo.Database) *SpendRepo {
	r := &SpendRepo{coll: db.Collection("spend")}
	for _, k := range []string{"rootTaskId", "taskId", "projectId"} {
		_, _ = r.coll.Indexes().CreateOne(context.Background(), mongo.IndexModel{Keys: bson.D{bson.E{Key: k, Value: 1}}})
	}
	return r
}

func (r *SpendRepo) Record(ctx context.Context, rec *domain.SpendRecord) error {
	_, err := r.coll.ReplaceOne(ctx, bson.M{"_id": rec.ID}, rec, options.Replace().SetUpsert(true))
	return err
}

func (r *SpendRepo) Total(ctx context.Context, field, value string) (domain.Spend, error) {
	cur, err := r.coll.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{field: value}}},
		{{Key: "$group", Value: bson.M{
			"_id":            nil,
			"inputTokens":    bson.M{"$sum": "$spend.inputTokens"},
			"outputTokens":   bson.M{"$sum": "$spend.outputTokens"},
			"costUsd":        bson.M{"$sum": "$spend.costUsd"},
			"unpricedTokens": bson.M{"$sum": "$spend.unpricedTokens"},
		}}},
	})
	if err != nil {
		return domain.Spend{}, err
	}
	var out []domain.Spend
	if err := cur.All(ctx, &out); err != nil || len(out) == 0 {
		return domain.Spend{}, err
	}
	return out[0], nil
}
