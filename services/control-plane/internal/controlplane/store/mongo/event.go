package mongo_store

import (
	"context"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type EventRepo struct{ coll *mongo.Collection }

func NewEventRepo(db *mongo.Database) *EventRepo {
	r := &EventRepo{coll: db.Collection("events")}
	_, _ = r.coll.Indexes().CreateOne(context.Background(), mongo.IndexModel{
		Keys: bson.D{bson.E{Key: "runId", Value: 1}, bson.E{Key: "at", Value: 1}},
	})
	return r
}

func (r *EventRepo) Append(ctx context.Context, e *domain.Event) error {
	if e.ID == "" {
		e.ID = "evt-" + uuidShort()
	}
	_, err := r.coll.InsertOne(ctx, e)
	return err
}

func (r *EventRepo) ListByRun(ctx context.Context, runID string) ([]*domain.Event, error) {
	opts := options.Find().SetSort(bson.D{bson.E{Key: "at", Value: 1}})
	cur, err := r.coll.Find(ctx, bson.M{"runId": runID}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*domain.Event
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []*domain.Event{}
	}
	return out, nil
}
