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

const legacySnapshotDocumentID = "marathon-tracker-current-state"

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
	state, found, err := s.LoadByID(ctx, "koch-2026")
	if err != nil || found {
		return state, found, err
	}
	return s.LoadByID(ctx, legacySnapshotDocumentID)
}

func (s *Store) LoadByID(ctx context.Context, id string) (race.State, bool, error) {
	var document snapshotDocument
	err := s.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&document)
	if err != nil {
		if err == mongodriver.ErrNoDocuments {
			return race.State{}, false, nil
		}
		return race.State{}, false, err
	}
	return document.State, true, nil
}

func (s *Store) LoadAll(ctx context.Context) ([]race.State, error) {
	cursor, err := s.collection.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var states []race.State
	for cursor.Next(ctx) {
		var document snapshotDocument
		if err := cursor.Decode(&document); err != nil {
			return nil, err
		}
		if document.State.Event.ID == "" {
			continue
		}
		states = append(states, document.State)
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}
	return states, nil
}

func (s *Store) Save(ctx context.Context, state race.State) error {
	id := state.Event.ID
	if id == "" {
		id = legacySnapshotDocumentID
	}
	document := snapshotDocument{
		ID:        id,
		State:     state,
		UpdatedAt: time.Now().UTC(),
	}
	_, err := s.collection.ReplaceOne(
		ctx,
		bson.M{"_id": id},
		document,
		options.Replace().SetUpsert(true),
	)
	return err
}

func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.collection.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (s *Store) Disconnect(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Disconnect(ctx)
}
