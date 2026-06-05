package auth

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

type Role string

const (
	RoleAdmin     Role = "admin"
	RoleVolunteer Role = "volunteer"
)

type User struct {
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
	Role     Role   `json:"role"`
}

type Manager struct {
	mu    sync.RWMutex
	path  string
	users map[string]User
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

func (m *Manager) SessionPath() string {
	if m == nil || strings.TrimSpace(m.path) == "" {
		return ""
	}
	return m.path + ".sessions"
}

func (m *Manager) Authenticate(username, password string) (User, bool) {
	username = normalizeUsername(username)
	m.mu.RLock()
	defer m.mu.RUnlock()
	user, ok := m.users[username]
	if !ok || user.Password != strings.TrimSpace(password) {
		return User{}, false
	}
	user.Password = ""
	return user, true
}

func (m *Manager) User(username string) (User, bool) {
	username = normalizeUsername(username)
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
	username = normalizeUsername(username)
	password = strings.TrimSpace(password)
	if username == "" || password == "" {
		return User{}, errors.New("volunteer username and password are required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.users[username]; exists {
		return User{}, fmt.Errorf("%s already exists", username)
	}
	user := User{Username: username, Password: password, Role: RoleVolunteer}
	m.users[username] = user
	if err := m.saveLocked(); err != nil {
		delete(m.users, username)
		return User{}, err
	}
	user.Password = ""
	return user, nil
}

func (m *Manager) DeleteVolunteer(username string) error {
	username = normalizeUsername(username)
	m.mu.Lock()
	defer m.mu.Unlock()
	user, exists := m.users[username]
	if !exists || user.Role != RoleVolunteer {
		return errors.New("volunteer was not found")
	}
	delete(m.users, username)
	if err := m.saveLocked(); err != nil {
		m.users[username] = user
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
