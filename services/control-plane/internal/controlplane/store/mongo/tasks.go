package mongo_store

import (
	"context"
	"time"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
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

func (r *TaskRepo) CompareAndSetStatus(ctx context.Context, id string, from, to domain.TaskStatus, at time.Time) (bool, error) {
	res, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": id, "status": from},
		bson.M{"$set": bson.M{"status": to, "updatedAt": at}})
	if err != nil {
		return false, err
	}
	return res.MatchedCount == 1, nil
}

func (r *TaskRepo) UpdateLatestRun(ctx context.Context, id, runID string, at time.Time) error {
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{
		"latestRunId": runID, "updatedAt": at,
	}})
	return err
}

func (r *TaskRepo) List(ctx context.Context, status domain.TaskStatus, projectIDs []string, limit int) ([]*domain.Task, error) {
	filter := bson.M{}
	if status != "" {
		filter["status"] = status
	}
	if projectIDs != nil {
		filter["projectId"] = bson.M{"$in": projectIDs}
	}
	opts := options.Find().SetSort(bson.D{bson.E{
		Key: "createdAt", Value: -1,
	}})
	if limit > 0 {
		opts.SetLimit(int64(limit))
	}
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	var out []*domain.Task
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []*domain.Task{}
	}
	return out, nil
}

func (r *TaskRepo) ListByStatus(ctx context.Context, status domain.TaskStatus, limit int) ([]*domain.Task, error) {
	filter := bson.M{"status": status}
	opts := options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}})
	if limit > 0 {
		opts.SetLimit(int64(limit))
	}
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	var out []*domain.Task
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []*domain.Task{}
	}
	return out, nil
}

func (r *TaskRepo) ListChildren(ctx context.Context, parentID string) ([]*domain.Task, error) {
	opts := options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}})
	cur, err := r.coll.Find(ctx, bson.M{"parentId": parentID}, opts)
	if err != nil {
		return nil, err
	}
	var out []*domain.Task
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []*domain.Task{}
	}
	return out, nil
}

func (r *TaskRepo) UpdateIntegration(ctx context.Context, id, worktree, branch, baseCommit string, at time.Time) error {
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{
		"integrationWorktree":   worktree,
		"integrationBranch":     branch,
		"integrationBaseCommit": baseCommit,
		"updatedAt":             at,
	}})
	return err
}

func (r *TaskRepo) UpdateDelivery(ctx context.Context, id string, d *domain.Delivery, at time.Time) error {
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"delivery": d, "updatedAt": at}})
	return err
}

func (r *TaskRepo) AddRevision(ctx context.Context, id string, rev domain.Revision, at time.Time) error {
	_, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": id, "revisions.n": bson.M{"$ne": rev.N}},
		bson.M{"$push": bson.M{"revisions": rev}, "$set": bson.M{"updatedAt": at}})
	return err
}

func (r *TaskRepo) AddFollowUp(ctx context.Context, id string, f domain.FollowUp, at time.Time) error {
	_, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": id, "followUps.n": bson.M{"$ne": f.N}},
		bson.M{"$push": bson.M{"followUps": f}, "$set": bson.M{"updatedAt": at}})
	return err
}

func (r *TaskRepo) SetFollowUpOutcome(ctx context.Context, id string, n int, outcome string, at time.Time) error {
	_, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": id, "followUps.n": n},
		bson.M{"$set": bson.M{"followUps.$.outcome": outcome, "updatedAt": at}})
	return err
}

func (r *TaskRepo) UnassignedRepoPaths(ctx context.Context) ([]string, error) {
	var out []string
	res := r.coll.Distinct(ctx, "repoPath", bson.M{"projectId": bson.M{"$exists": false}})
	if err := res.Err(); err != nil {
		return nil, err
	}
	return out, res.Decode(&out)
}

func (r *TaskRepo) AssignProject(ctx context.Context, repoPath, projectID string) (int64, error) {
	res, err := r.coll.UpdateMany(ctx,
		bson.M{"repoPath": repoPath, "projectId": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"projectId": projectID}})
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

func (r *TaskRepo) SetBoardLane(ctx context.Context, id string, lane domain.BoardLane, at time.Time) error {
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"boardLane": lane, "updatedAt": at}})
	return err
}

// SetMaxSteps sets it on a task and its subtasks, which each hold a copy.
func (r *TaskRepo) SetMaxSteps(ctx context.Context, id string, steps int, at time.Time) error {
	res, err := r.coll.UpdateMany(ctx, bson.M{"$or": bson.A{bson.M{"_id": id}, bson.M{"parentId": id}}},
		bson.M{"$set": bson.M{"budget.maxSteps": steps, "updatedAt": at}})
	if err == nil && res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

func (r *TaskRepo) SetSpendLimits(ctx context.Context, id string, l domain.Limits, at time.Time) error {
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{
		"budget.maxTokens": l.MaxTokens, "budget.maxCostUsd": l.MaxCostUSD, "updatedAt": at}})
	if err == nil && res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}
