package mongo_store

import (
	"context"

	"github.com/chokey2nv/forge-ai/services/control-plane/internal/controlplane/domain"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type ProjectRepo struct{ coll *mongo.Collection }

func NewProjectRepo(db *mongo.Database) *ProjectRepo {
	r := &ProjectRepo{coll: db.Collection("projects")}
	// A repository belongs to one project at most.
	_, _ = r.coll.Indexes().CreateMany(context.Background(), []mongo.IndexModel{
		{Keys: bson.D{bson.E{Key: "repos.path", Value: 1}}, Options: options.Index().SetUnique(true).SetSparse(true)},
		{Keys: bson.D{bson.E{Key: "members.userId", Value: 1}}},
		{Keys: bson.D{bson.E{Key: "name", Value: 1}}, Options: options.Index().SetUnique(true)},
	})
	return r
}

func (r *ProjectRepo) Create(ctx context.Context, p *domain.Project) error {
	if p.ID == "" {
		p.ID = "prj-" + uuidShort()
	}
	if p.Repos == nil {
		p.Repos = []domain.ProjectRepo{}
	}
	if p.Members == nil {
		p.Members = []domain.ProjectMember{}
	}
	_, err := r.coll.InsertOne(ctx, p)
	return err
}

func (r *ProjectRepo) Get(ctx context.Context, id string) (*domain.Project, error) {
	var p domain.Project
	return &p, r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&p)
}

func (r *ProjectRepo) find(ctx context.Context, filter bson.M) ([]*domain.Project, error) {
	cur, err := r.coll.Find(ctx, filter, options.Find().SetSort(bson.D{bson.E{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	out := []*domain.Project{}
	return out, cur.All(ctx, &out)
}

func (r *ProjectRepo) List(ctx context.Context) ([]*domain.Project, error) {
	return r.find(ctx, bson.M{})
}

func (r *ProjectRepo) ListForUser(ctx context.Context, userID string) ([]*domain.Project, error) {
	return r.find(ctx, bson.M{"members.userId": userID})
}

func (r *ProjectRepo) ByRepo(ctx context.Context, path string) (*domain.Project, error) {
	var p domain.Project
	return &p, r.coll.FindOne(ctx, bson.M{"repos.path": path}).Decode(&p)
}

func (r *ProjectRepo) AddRepo(ctx context.Context, id string, repo domain.ProjectRepo) error {
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": id, "repos.path": bson.M{"$ne": repo.Path}}, bson.M{"$push": bson.M{"repos": repo}})
	if err == nil && res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

func (r *ProjectRepo) RemoveRepo(ctx context.Context, id, path string) error {
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$pull": bson.M{"repos": bson.M{"path": path}}})
	return err
}

func (r *ProjectRepo) SetMember(ctx context.Context, id string, m domain.ProjectMember) error {
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": id, "members.userId": m.UserID}, bson.M{"$set": bson.M{"members.$.role": m.Role}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 1 {
		return nil
	}
	_, err = r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$push": bson.M{"members": m}})
	return err
}

func (r *ProjectRepo) RemoveMember(ctx context.Context, id, userID string) error {
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$pull": bson.M{"members": bson.M{"userId": userID}}})
	return err
}
