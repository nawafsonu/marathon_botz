package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagerCreatesDefaultAdminAndFourVolunteers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logincred.txt")

	manager, err := NewManager(path)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	admin, ok := manager.Authenticate("admin", "admin2026")
	if !ok {
		t.Fatal("default admin could not authenticate")
	}
	if admin.Role != RoleAdmin {
		t.Fatalf("admin role = %s, want %s", admin.Role, RoleAdmin)
	}
	if got := len(manager.Volunteers()); got != 4 {
		t.Fatalf("volunteer count = %d, want 4", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("credential file was not created: %v", err)
	}
}

func TestManagerAddsAndDeletesVolunteers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logincred.txt")
	manager, err := NewManager(path)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	created, err := manager.AddVolunteer(" FinishDesk ", " finish2026 ")
	if err != nil {
		t.Fatalf("add volunteer: %v", err)
	}
	if created.Username != "finishdesk" || created.Role != RoleVolunteer {
		t.Fatalf("created volunteer = %+v", created)
	}
	if _, ok := manager.Authenticate("finishdesk", "finish2026"); !ok {
		t.Fatal("created volunteer could not authenticate")
	}
	if err := manager.DeleteVolunteer("finishdesk"); err != nil {
		t.Fatalf("delete volunteer: %v", err)
	}
	if _, ok := manager.Authenticate("finishdesk", "finish2026"); ok {
		t.Fatal("deleted volunteer still authenticated")
	}
}
