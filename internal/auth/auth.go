package auth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Role string

const (
	RoleAdmin     Role = "admin"
	RoleVolunteer Role = "volunteer"
	RoleGuest     Role = "guest"
)

type User struct {
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
	Role     Role   `json:"role"`
}

type Manager struct {
	mu         sync.RWMutex
	path       string
	users      map[string]User
	collection *mongodriver.Collection
}

func NewManager(path string) (*Manager, error) {
	manager := &Manager{
		path:  path,
		users: make(map[string]User),
	}
	if err := manager.loadOrCreateDefaults(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) UseMongo(collection *mongodriver.Collection) error {
	m.mu.Lock()
	m.collection = collection
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	count, err := collection.CountDocuments(ctx, bson.M{})
	if err != nil {
		return err
	}

	if count == 0 {
		m.mu.Lock()
		defer m.mu.Unlock()
		defaults := defaultUsers()
		for _, u := range defaults {
			doc := bson.M{
				"_id":      normalizeUsername(u.Username),
				"username": u.Username,
				"password": u.Password,
				"role":     string(u.Role),
			}
			_, err := collection.ReplaceOne(
				ctx,
				bson.M{"_id": doc["_id"]},
				doc,
				options.Replace().SetUpsert(true),
			)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) SessionPath() string {
	if m == nil || strings.TrimSpace(m.path) == "" {
		return ""
	}
	return m.path + ".sessions"
}

func (m *Manager) Authenticate(username, password string) (User, bool) {
	username = normalizeUsername(username)
	password = strings.TrimSpace(password)

	m.mu.RLock()
	col := m.collection
	m.mu.RUnlock()

	if col != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var doc struct {
			Username string `bson:"username"`
			Password string `bson:"password"`
			Role     Role   `bson:"role"`
		}
		err := col.FindOne(ctx, bson.M{"_id": username}).Decode(&doc)
		if err != nil || doc.Password != password {
			return User{}, false
		}
		return User{Username: doc.Username, Role: doc.Role}, true
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	user, ok := m.users[username]
	if !ok || user.Password != password {
		return User{}, false
	}
	user.Password = ""
	return user, true
}

func (m *Manager) User(username string) (User, bool) {
	username = normalizeUsername(username)

	m.mu.RLock()
	col := m.collection
	m.mu.RUnlock()

	if col != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var doc struct {
			Username string `bson:"username"`
			Role     Role   `bson:"role"`
		}
		err := col.FindOne(ctx, bson.M{"_id": username}).Decode(&doc)
		if err != nil {
			return User{}, false
		}
		return User{Username: doc.Username, Role: doc.Role}, true
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	user, ok := m.users[username]
	if !ok {
		return User{}, false
	}
	user.Password = ""
	return user, true
}

func (m *Manager) Volunteers() []User {
	m.mu.RLock()
	col := m.collection
	m.mu.RUnlock()

	if col != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cursor, err := col.Find(ctx, bson.M{"role": string(RoleVolunteer)})
		if err != nil {
			return nil
		}
		defer cursor.Close(ctx)
		var users []User
		for cursor.Next(ctx) {
			var doc struct {
				Username string `bson:"username"`
				Role     Role   `bson:"role"`
			}
			if err := cursor.Decode(&doc); err == nil {
				users = append(users, User{Username: doc.Username, Role: doc.Role})
			}
		}
		sort.Slice(users, func(i, j int) bool {
			return users[i].Username < users[j].Username
		})
		return users
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	users := make([]User, 0)
	for _, user := range m.users {
		if user.Role != RoleVolunteer {
			continue
		}
		user.Password = ""
		users = append(users, user)
	}
	sort.Slice(users, func(i, j int) bool {
		return users[i].Username < users[j].Username
	})
	return users
}

func (m *Manager) AddVolunteer(username, password string) (User, error) {
	usernameNorm := normalizeUsername(username)
	password = strings.TrimSpace(password)
	if usernameNorm == "" || password == "" {
		return User{}, errors.New("volunteer username and password are required")
	}

	m.mu.RLock()
	col := m.collection
	m.mu.RUnlock()

	if col != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		count, err := col.CountDocuments(ctx, bson.M{"_id": usernameNorm})
		if err != nil {
			return User{}, err
		}
		if count > 0 {
			return User{}, fmt.Errorf("%s already exists", username)
		}

		doc := bson.M{
			"_id":      usernameNorm,
			"username": username,
			"password": password,
			"role":     string(RoleVolunteer),
		}
		_, err = col.InsertOne(ctx, doc)
		if err != nil {
			return User{}, err
		}
		return User{Username: username, Role: RoleVolunteer}, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.users[usernameNorm]; exists {
		return User{}, fmt.Errorf("%s already exists", username)
	}
	user := User{Username: username, Password: password, Role: RoleVolunteer}
	m.users[usernameNorm] = user
	if err := m.saveLocked(); err != nil {
		delete(m.users, usernameNorm)
		return User{}, err
	}
	user.Password = ""
	return user, nil
}

func (m *Manager) DeleteVolunteer(username string) error {
	usernameNorm := normalizeUsername(username)

	m.mu.RLock()
	col := m.collection
	m.mu.RUnlock()

	if col != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var doc struct {
			Role Role `bson:"role"`
		}
		err := col.FindOne(ctx, bson.M{"_id": usernameNorm}).Decode(&doc)
		if err != nil {
			return errors.New("volunteer was not found")
		}
		if doc.Role != RoleVolunteer {
			return errors.New("cannot delete non-volunteer user")
		}

		_, err = col.DeleteOne(ctx, bson.M{"_id": usernameNorm})
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	user, exists := m.users[usernameNorm]
	if !exists || user.Role != RoleVolunteer {
		return errors.New("volunteer was not found")
	}
	delete(m.users, usernameNorm)
	if err := m.saveLocked(); err != nil {
		m.users[usernameNorm] = user
		return err
	}
	return nil
}

func (m *Manager) loadOrCreateDefaults() error {
	file, err := os.Open(m.path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		m.users = defaultUsers()
		return m.saveLocked()
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) != 3 {
			continue
		}
		username := normalizeUsername(parts[0])
		password := strings.TrimSpace(parts[1])
		role := Role(strings.TrimSpace(parts[2]))
		if username == "" || password == "" || (role != RoleAdmin && role != RoleVolunteer) {
			continue
		}
		m.users[username] = User{Username: username, Password: password, Role: role}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(m.users) == 0 {
		m.users = defaultUsers()
		return m.saveLocked()
	}
	return nil
}

func (m *Manager) saveLocked() error {
	var builder strings.Builder
	builder.WriteString("# username:password:role\n")
	names := make([]string, 0, len(m.users))
	for username := range m.users {
		names = append(names, username)
	}
	sort.Strings(names)
	for _, username := range names {
		user := m.users[username]
		builder.WriteString(fmt.Sprintf("%s:%s:%s\n", user.Username, user.Password, user.Role))
	}
	return os.WriteFile(m.path, []byte(builder.String()), 0600)
}

func defaultUsers() map[string]User {
	users := map[string]User{
		"admin": {Username: "admin", Password: "admin2026", Role: RoleAdmin},
	}
	for i := 1; i <= 4; i++ {
		username := fmt.Sprintf("volunteer%d", i)
		users[username] = User{Username: username, Password: fmt.Sprintf("volunteer%d2026", i), Role: RoleVolunteer}
	}
	return users
}

func normalizeUsername(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
