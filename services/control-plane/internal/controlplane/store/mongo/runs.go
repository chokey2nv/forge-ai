package mongo_store

import (
	"context"
	"fmt"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type RunRepo struct{ coll *mongo.Collection }

func NewRunRepo(db *mongo.Database) *RunRepo {
	r := &RunRepo{coll: db.Collection("workflowRuns")}
	_, _ = r.coll.Indexes().CreateOne(context.Background(), mongo.IndexModel{
		Keys: bson.D{bson.E{Key: "lastHeartbeat", Value: 1}},
	})
	return r
}

func (r *RunRepo) Create(ctx context.Context, run *domain.WorkflowRun) error {
	_, err := r.coll.InsertOne(ctx, run)
	return err
}

func (r *RunRepo) Get(ctx context.Context, id string) (*domain.WorkflowRun, error) {
	var run domain.WorkflowRun
	if err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *RunRepo) UpdateStatus(ctx context.Context, id string, status domain.RunStatus, reason string, at time.Time) error {
	set := bson.M{"status": status, "updatedAt": at}
	if reason != "" {
		set["blockedReason"] = reason
	}
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": set})
	return err
}

func (r *RunRepo) SaveCheckpoint(ctx context.Context, id string, cp *domain.Checkpoint, at time.Time) error {
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{
		"lastCheckpoint": cp,
		"lastHeartbeat":  at,
		"stepCount":      cp.StepCount,
		"updatedAt":      at,
	}})
	return err
}

func (r *RunRepo) MarkStale(ctx context.Context, before time.Time) ([]*domain.WorkflowRun, error) {
	// Find runs that are RUNNING with heartbeat older than cutoff.
	cur, err := r.coll.Find(ctx, bson.M{
		"status":        domain.RunRunning,
		"lastHeartbeat": bson.M{"$lt": before},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var candidates []*domain.WorkflowRun
	if err := cur.All(ctx, &candidates); err != nil {
		return nil, err
	}

	// Transition each atomically (filter includes status=RUNNING so we don't race).
	var transitioned []*domain.WorkflowRun
	now := time.Now().UTC()
	for _, run := range candidates {
		res, err := r.coll.UpdateOne(ctx,
			bson.M{"_id": run.ID, "status": domain.RunRunning},
			bson.M{"$set": bson.M{"status": domain.RunResumable, "updatedAt": now}},
		)
		if err == nil && res.ModifiedCount == 1 {
			run.Status = domain.RunResumable
			transitioned = append(transitioned, run)
		}
	}
	return transitioned, nil
}
func (r *RunRepo) UpdateWorkspace(ctx context.Context, id, worktreePath, containerName string, at time.Time) error {
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{
		"worktreePath":  worktreePath,
		"containerName": containerName,
		"updatedAt":     at,
	}})
	return err
}

func (r *RunRepo) UpdateVerification(
	ctx context.Context,
	id string,
	attempt int,
	passed bool,
	status domain.RunStatus,
	reason string,
	at time.Time,
) error {
	set := bson.M{
		"status":              status,
		"verificationAttempt": attempt,
		"verificationPassed":  passed,
		"updatedAt":           at,
	}
	if reason != "" {
		set["blockedReason"] = reason
	}
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": set})
	return err
}

func (r *RunRepo) SavePlan(ctx context.Context, id string, plan *domain.Plan, at time.Time) error {
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{
		"plan": plan, "updatedAt": at,
	}})
	return err
}

func (r *RunRepo) SaveReviewVerdict(ctx context.Context, id, verdict string, at time.Time) error {
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{
		"reviewerVerdict": verdict, "updatedAt": at,
	}})
	return err
}

func uuidShort() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
