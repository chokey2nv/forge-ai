package mongo_store

import (
	"context"
	"crypto/rand"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// LoadOrCreateSecret returns the instance secret called name, creating a
// random 32-byte one the first time. It lives in the "instance" collection
// so it survives restarts (Phase 13.1: the run callback key).
func LoadOrCreateSecret(ctx context.Context, db *mongo.Database, name string) ([]byte, error) {
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return nil, err
	}
	var doc struct {
		Key []byte `bson:"key"`
	}
	err := db.Collection("instance").FindOneAndUpdate(ctx,
		bson.M{"_id": name},
		bson.M{"$setOnInsert": bson.M{"key": fresh}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	).Decode(&doc)
	return doc.Key, err
}
