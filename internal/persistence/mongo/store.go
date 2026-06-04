package mongo

import (
	"context"
	"time"

	"marathon/internal/race"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

const snapshotDocumentID = "marathon-tracker-current-state"

type Store struct {
	client     *mongodriver.Client
	collection *mongodriver.Collection
}

type snapshotDocument struct {
	ID        string     `bson:"_id"`
	State     race.State `bson:"state"`
	UpdatedAt time.Time  `bson:"updatedAt"`
}

func Connect(ctx context.Context, uri string, database string) (*Store, error) {
	client, err := mongodriver.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		_ = client.Disconnect(ctx)
		return nil, err
	}
	return &Store{
		client:     client,
		collection: client.Database(database).Collection("race_snapshots"),
	}, nil
}

func (s *Store) Load(ctx context.Context) (race.State, bool, error) {
	var document snapshotDocument
	err := s.collection.FindOne(ctx, bson.M{"_id": snapshotDocumentID}).Decode(&document)
	if err != nil {
		if err == mongodriver.ErrNoDocuments {
			return race.State{}, false, nil
		}
		return race.State{}, false, err
	}
	return document.State, true, nil
}

func (s *Store) Save(ctx context.Context, state race.State) error {
	document := snapshotDocument{
		ID:        snapshotDocumentID,
		State:     state,
		UpdatedAt: time.Now().UTC(),
	}
	_, err := s.collection.ReplaceOne(
		ctx,
		bson.M{"_id": snapshotDocumentID},
		document,
		options.Replace().SetUpsert(true),
	)
	return err
}

func (s *Store) Disconnect(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Disconnect(ctx)
}
