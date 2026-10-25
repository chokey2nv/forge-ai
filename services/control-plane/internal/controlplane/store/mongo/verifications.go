package mongo_store

import (
	"context"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type VerificationRepo struct{ coll *mongo.Collection }

func NewVerificationRepo(db *mongo.Database) *VerificationRepo {
	r := &VerificationRepo{coll: db.Collection("verificationRuns")}
	_, _ = r.coll.Indexes().CreateOne(context.Background(), mongo.IndexModel{
		Keys: bson.D{bson.E{Key: "runId", Value: 1}, bson.E{Key: "attempt", Value: -1}},
	})
	return r
}

func (r *VerificationRepo) Create(ctx context.Context, vr *domain.VerificationRun) error {
	_, err := r.coll.InsertOne(ctx, vr)
	return err
}

func (r *VerificationRepo) LatestForRun(ctx context.Context, runID string) (*domain.VerificationRun, error) {
	var vr domain.VerificationRun
	err := r.coll.FindOne(ctx, bson.M{"runId": runID},
		options.FindOne().SetSort(bson.D{bson.E{Key: "attempt", Value: -1}}),
	).Decode(&vr)
	if err != nil {
		return nil, err
	}
	return &vr, nil
}
