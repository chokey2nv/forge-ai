package mongo_store

import (
	"context"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type TaskRepo struct{ coll *mongo.Collection }

func NewTaskRepo(db *mongo.Database) *TaskRepo {
	return &TaskRepo{coll: db.Collection("tasks")}
}

func (r *TaskRepo) Create(ctx context.Context, t *domain.Task) error {
	_, err := r.coll.InsertOne(ctx, t)
	return err
}

func (r *TaskRepo) Get(ctx context.Context, id string) (*domain.Task, error) {
	var t domain.Task
	if err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *TaskRepo) UpdateStatus(ctx context.Context, id string, status domain.TaskStatus, at time.Time) error {
	_, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": id},
		bson.M{"$set": bson.M{"status": status, "updatedAt": at}})
	return err
}
