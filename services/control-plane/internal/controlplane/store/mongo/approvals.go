package mongo_store

import (
	"context"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type ApprovalRepo struct{ coll *mongo.Collection }

func NewApprovalRepo(db *mongo.Database) *ApprovalRepo {
	r := &ApprovalRepo{coll: db.Collection("approvals")}
	_, _ = r.coll.Indexes().CreateOne(context.Background(), mongo.IndexModel{
		Keys: bson.D{bson.E{Key: "taskId", Value: 1}, bson.E{Key: "decidedAt", Value: -1}},
	})
	return r
}

func (r *ApprovalRepo) Create(ctx context.Context, a *domain.Approval) error {
	if a.ID == "" {
		a.ID = "apr-" + uuidShort()
	}
	_, err := r.coll.InsertOne(ctx, a)
	return err
}

func (r *ApprovalRepo) ListByTask(ctx context.Context, taskID string) ([]*domain.Approval, error) {
	opts := options.Find().SetSort(bson.D{bson.E{Key: "decidedAt", Value: -1}})
	cur, err := r.coll.Find(ctx, bson.M{"taskId": taskID}, opts)
	if err != nil {
		return nil, err
	}
	var out []*domain.Approval
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []*domain.Approval{}
	}
	return out, nil
}
