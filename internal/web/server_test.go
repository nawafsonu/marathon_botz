package web

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marathon/internal/auth"
	"marathon/internal/race"
)

func TestStateEndpointReturnsLiveSummary(t *testing.T) {
	svc := testService(t)
	handler := NewServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.Code, res.Body.String())
	}
	var body race.Snapshot
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if body.Event.Name != "Kochi Marathon 2026" {
		t.Fatalf("event name = %q", body.Event.Name)
	}
	if body.Summary.TotalParticipants != 2 || body.Summary.Active != 2 {
		t.Fatalf("unexpected summary: %+v", body.Summary)
	}
}

func TestRegisterParticipantEndpointCreatesBib(t *testing.T) {
	svc := race.NewService(testEvent(), testCheckpoints(), nil, 10*time.Minute)
	handler := NewServer(svc)

	payload := []byte(`{"name":"Priya Raman","phoneNumber":"+91 99999 11111","notes":"10K upgrade"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/participants", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", res.Code, res.Body.String())
	}
	var participant race.Participant
	if err := json.NewDecoder(res.Body).Decode(&participant); err != nil {
		t.Fatalf("decode participant: %v", err)
	}
	if participant.BibNumber != "BIB-001" {
		t.Fatalf("bib = %s, want BIB-001", participant.BibNumber)
	}
}

func TestCheckpointEndpointRejectsOutOfOrderEntry(t *testing.T) {
	svc := race.NewService(testEvent(), testCheckpoints(), nil, 10*time.Minute)
	participant, err := svc.RegisterParticipant("Priya Raman", "+91 99999 11111", "")
	if err != nil {
		t.Fatalf("register participant: %v", err)
	}
	handler := NewServer(svc)

	payload := []byte(`{"bibNumber":"` + participant.BibNumber + `","checkpointId":"cp2","volunteerId":"vol-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/checkpoint-logs", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "previous checkpoint") {
		t.Fatalf("body did not explain validation error: %s", res.Body.String())
	}
}

func TestCheckpointEndpointAcceptsParticipantIDWithoutBib(t *testing.T) {
	svc := race.NewService(testEvent(), testCheckpoints(), nil, 10*time.Minute)
	participant, err := svc.RegisterParticipant("Priya Raman", "+91 99999 11111", "")
	if err != nil {
		t.Fatalf("register participant: %v", err)
	}
	handler := NewServer(svc)

	payload := []byte(`{"participantId":"` + participant.ID + `","checkpointId":"start","volunteerId":"vol-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/checkpoint-logs", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", res.Code, res.Body.String())
	}
	var log race.CheckpointLog
	if err := json.NewDecoder(res.Body).Decode(&log); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if log.Participant.BibNumber != participant.BibNumber {
		t.Fatalf("logged bib = %s, want %s", log.Participant.BibNumber, participant.BibNumber)
	}
}

func TestCheckpointEndpointPrefersTypedBibOverSelectedRunner(t *testing.T) {
	svc := race.NewService(testEvent(), testCheckpoints(), nil, 10*time.Minute)
	selected, err := svc.RegisterParticipant("Priya Raman", "+91 99999 11111", "")
	if err != nil {
		t.Fatalf("register selected participant: %v", err)
	}
	other, err := svc.RegisterParticipant("Arjun Nair", "+91 99999 22222", "")
	if err != nil {
		t.Fatalf("register other participant: %v", err)
	}
	handler := NewServer(svc)

	payload := []byte(`{"participantId":"` + selected.ID + `","bibNumber":"002","checkpointId":"start","volunteerId":"vol-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/checkpoint-logs", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", res.Code, res.Body.String())
	}
	var log race.CheckpointLog
	if err := json.NewDecoder(res.Body).Decode(&log); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if log.Participant.BibNumber != other.BibNumber {
		t.Fatalf("logged bib = %s, want typed chest number %s", log.Participant.BibNumber, other.BibNumber)
	}
}

func TestRaceAnalysisRequiresConfiguredGroqClient(t *testing.T) {
	svc := testService(t)
	handler := NewServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/analysis/race", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "Groq analysis is not configured") {
		t.Fatalf("body did not explain missing Groq config: %s", res.Body.String())
	}
}

func TestDeleteParticipantEndpointRemovesRunnerProfile(t *testing.T) {
	svc := testService(t)
	handler := NewServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/participants/BIB-001/delete", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.Code, res.Body.String())
	}
	if _, err := svc.RunnerProfile("BIB-001"); !errors.Is(err, race.ErrInvalidBib) {
		t.Fatalf("runner profile error = %v, want ErrInvalidBib", err)
	}
	for _, log := range svc.RecentLogs(20) {
		if log.Participant.BibNumber == "BIB-001" {
			t.Fatalf("deleted runner log remains: %+v", log)
		}
	}
}

func TestDeleteMarathonEndpointRemovesProjectFromStoreAndRegistry(t *testing.T) {
	svc := race.NewService(testEvent(), testCheckpoints(), nil, 10*time.Minute)
	store := &memoryProjectStore{}
	handler := NewServer(svc, WithProjectStore(store))

	req := httptest.NewRequest(http.MethodPost, "/api/events/event-1/delete", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.Code, res.Body.String())
	}
	if len(store.deleted) != 1 || store.deleted[0] != "event-1" {
		t.Fatalf("deleted ids = %+v, want event-1", store.deleted)
	}
	stateReq := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	stateRes := httptest.NewRecorder()
	handler.ServeHTTP(stateRes, stateReq)
	if stateRes.Code != http.StatusNotFound {
		t.Fatalf("state status = %d, want 404 after deleting only marathon", stateRes.Code)
	}
}

func TestRunnerCertificatePageIncludesMarathonAndRunnerData(t *testing.T) {
	svc := race.NewService(testEvent(), testCheckpoints(), nil, 10*time.Minute)
	finished := mustRegisterWeb(t, svc, "Certificate Runner")
	start := time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC)
	for _, step := range []struct {
		checkpoint string
		offset     time.Duration
	}{
		{"start", 0},
		{"cp1", 22 * time.Minute},
		{"cp2", 50 * time.Minute},
		{"finish", 95 * time.Minute},
	} {
		if _, err := svc.RecordCheckpoint(finished.BibNumber, step.checkpoint, "vol-1", start.Add(step.offset)); err != nil {
			t.Fatalf("record %s: %v", step.checkpoint, err)
		}
	}
	handler := NewServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/runners/"+finished.BibNumber+"/certificate", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, required := range []string{"Certificate of Completion", "Kochi Marathon 2026", "Certificate Runner", finished.BibNumber, "Race Time", "1h 35m 00s", "Finish Time"} {
		if !strings.Contains(body, required) {
			t.Fatalf("certificate missing %q in body: %s", required, body)
		}
	}
}

func TestRunnerProfileLinksToCertificate(t *testing.T) {
	svc := testService(t)
	handler := NewServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/runners/BIB-001", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "/runners/BIB-001/certificate") {
		t.Fatalf("runner profile should link to certificate page: %s", res.Body.String())
	}
}

func TestRunnerProfileRendersSpecificAIAnalysisPanel(t *testing.T) {
	svc := testService(t)
	handler := NewServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/runners/BIB-001", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, required := range []string{"Runner AI Analysis", "analysis-data-grid", "/api/analysis/runners/BIB-001", "Latest", "Segments"} {
		if !strings.Contains(body, required) {
			t.Fatalf("runner profile missing AI panel marker %q: %s", required, body)
		}
	}
}

func TestFinalCSVExportContainsRankedRunners(t *testing.T) {
	svc := testService(t)
	handler := NewServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/reports/final.csv", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.Code, res.Body.String())
	}
	reader := csv.NewReader(strings.NewReader(res.Body.String()))
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	if got := strings.Join(rows[0], ","); got != "Rank,Bib,Name,Status,Latest Checkpoint,Finish Time,Race Time,Gap" {
		t.Fatalf("csv header = %q", got)
	}
	if rows[1][1] != "BIB-002" {
		t.Fatalf("first ranked bib = %s, want BIB-002", rows[1][1])
	}
}

func TestEventSettingsEndpointUpdatesDistanceAndStartTime(t *testing.T) {
	svc := testService(t)
	handler := NewServer(svc)

	payload := []byte(`{"distanceKm":21,"startTime":"2026-01-10T05:30:00Z"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/event-settings", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.Code, res.Body.String())
	}
	if svc.Event().DistanceKM != 21 {
		t.Fatalf("distance = %d, want 21", svc.Event().DistanceKM)
	}
}

func TestStartRaceEndpointMarksSelectedEventActive(t *testing.T) {
	event := testEvent()
	event.Status = race.EventStatusUpcoming
	svc := race.NewService(event, testCheckpoints(), nil, 10*time.Minute)
	handler := NewServer(svc)

	req := httptest.NewRequest(http.MethodPost, "/api/start-race", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.Code, res.Body.String())
	}
	var eventData race.Event
	if err := json.NewDecoder(res.Body).Decode(&eventData); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if eventData.Status != race.EventStatusActive {
		t.Fatalf("event status = %s, want %s", eventData.Status, race.EventStatusActive)
	}
}

func TestAuthRedirectsToLoginAndAllowsAdminVolunteerManagement(t *testing.T) {
	manager, err := auth.NewManager(filepath.Join(t.TempDir(), "logincred.txt"))
	if err != nil {
		t.Fatalf("auth manager: %v", err)
	}
	handler := NewServer(nil, WithAuthManager(manager))

	guestReq := httptest.NewRequest(http.MethodGet, "/", nil)
	guestRes := httptest.NewRecorder()
	handler.ServeHTTP(guestRes, guestReq)
	if guestRes.Code != http.StatusSeeOther || guestRes.Header().Get("Location") != "/login" {
		t.Fatalf("guest status=%d location=%q, want redirect to /login", guestRes.Code, guestRes.Header().Get("Location"))
	}

	form := url.Values{"username": {"admin"}, "password": {"admin2026"}}
	loginReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRes := httptest.NewRecorder()
	handler.ServeHTTP(loginRes, loginReq)
	if loginRes.Code != http.StatusSeeOther {
		t.Fatalf("login status=%d, want 303", loginRes.Code)
	}
	cookies := loginRes.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login did not set a session cookie")
	}

	payload := []byte(`{"username":"cp5","password":"cp52026"}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/volunteers", bytes.NewReader(payload))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.AddCookie(cookies[0])
	createRes := httptest.NewRecorder()
	handler.ServeHTTP(createRes, createReq)
	if createRes.Code != http.StatusCreated {
		t.Fatalf("create volunteer status=%d, want 201; body: %s", createRes.Code, createRes.Body.String())
	}
	if _, ok := manager.Authenticate("cp5", "cp52026"); !ok {
		t.Fatal("created volunteer could not authenticate")
	}
}

func TestVolunteerRacePageHidesAdminSetupControls(t *testing.T) {
	manager, err := auth.NewManager(filepath.Join(t.TempDir(), "logincred.txt"))
	if err != nil {
		t.Fatalf("auth manager: %v", err)
	}
	svc := testService(t)
	handler := NewServer(svc, WithAuthManager(manager))
	cookie := loginCookie(t, handler, "volunteer1", "volunteer12026")

	req := httptest.NewRequest(http.MethodGet, "/race", nil)
	req.AddCookie(cookie)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, forbidden := range []string{"Manage Checkpoints", "start-race-form", "checkpoint-manager-form"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("volunteer race page should not render %q", forbidden)
		}
	}
	for _, required := range []string{"Race Checkpoint Entry", "Add Participant", `name="participantId"`} {
		if !strings.Contains(body, required) {
			t.Fatalf("volunteer race page should render %q", required)
		}
	}
}

func TestVolunteerCannotUseAdminRaceControls(t *testing.T) {
	manager, err := auth.NewManager(filepath.Join(t.TempDir(), "logincred.txt"))
	if err != nil {
		t.Fatalf("auth manager: %v", err)
	}
	svc := testService(t)
	handler := NewServer(svc, WithAuthManager(manager))
	cookie := loginCookie(t, handler, "volunteer1", "volunteer12026")

	for _, item := range []struct {
		name    string
		path    string
		payload string
	}{
		{name: "start race", path: "/api/start-race", payload: `{}`},
		{name: "add checkpoint", path: "/api/checkpoints", payload: `{"name":"CP9","sequence":9,"distanceKm":35}`},
		{name: "import runners", path: "/api/import-runners", payload: `{}`},
	} {
		req := httptest.NewRequest(http.MethodPost, item.path, strings.NewReader(item.payload))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d, want 403; body: %s", item.name, res.Code, res.Body.String())
		}
	}
}

func TestImportParticipantsEndpointUsesMappedColumns(t *testing.T) {
	svc := race.NewService(testEvent(), testCheckpoints(), nil, 10*time.Minute)
	handler := NewServer(svc)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("bibColumn", "Number")
	_ = writer.WriteField("nameColumn", "Runner")
	_ = writer.WriteField("phoneColumn", "Phone")
	part, err := writer.CreateFormFile("file", "runners.csv")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	_, _ = part.Write([]byte("Number,Runner,Phone\n301,Asha Roy,+91 1\n302,Vikram Sen,+91 2\n"))
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/import-runners", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", res.Code, res.Body.String())
	}
	var result race.ImportResult
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Created != 2 || len(result.Errors) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if svc.Participants()[0].BibNumber != "BIB-301" {
		t.Fatalf("first bib = %s", svc.Participants()[0].BibNumber)
	}
}

func TestCheckpointManagementEndpointAddsCheckpoint(t *testing.T) {
	svc := race.NewService(testEvent(), testCheckpoints(), nil, 10*time.Minute)
	handler := NewServer(svc)

	payload := []byte(`{"name":"CP1.5","sequence":3,"distanceKm":7.5}`)
	req := httptest.NewRequest(http.MethodPost, "/api/checkpoints", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", res.Code, res.Body.String())
	}
	if got := svc.Checkpoints()[2].Name; got != "CP1.5" {
		t.Fatalf("inserted checkpoint = %s, want CP1.5", got)
	}
}

func TestRacePageContainsCheckpointEntryAwayFromDashboard(t *testing.T) {
	svc := testService(t)
	handler := NewServer(svc)

	dashboardReq := httptest.NewRequest(http.MethodGet, "/", nil)
	dashboardRes := httptest.NewRecorder()
	handler.ServeHTTP(dashboardRes, dashboardReq)

	raceReq := httptest.NewRequest(http.MethodGet, "/race", nil)
	raceRes := httptest.NewRecorder()
	handler.ServeHTTP(raceRes, raceReq)

	if dashboardRes.Code != http.StatusOK || raceRes.Code != http.StatusOK {
		t.Fatalf("dashboard status=%d race status=%d", dashboardRes.Code, raceRes.Code)
	}
	if strings.Contains(dashboardRes.Body.String(), "Race Checkpoint Entry") {
		t.Fatal("dashboard should not render race checkpoint entry")
	}
	if !strings.Contains(raceRes.Body.String(), "Race Checkpoint Entry") {
		t.Fatal("race page should render race checkpoint entry")
	}
	if !strings.Contains(raceRes.Body.String(), `name="participantId"`) {
		t.Fatal("race page should render a runner selector for checkpoint entry")
	}
	if !strings.Contains(raceRes.Body.String(), `<select name="name"`) {
		t.Fatal("race page should render checkpoint name as a dropdown")
	}
	if !strings.Contains(raceRes.Body.String(), `id="start-race-form"`) {
		t.Fatal("race page should render a start race form")
	}
	if !strings.Contains(raceRes.Body.String(), `Start Race`) {
		t.Fatal("race page should render a start race button")
	}
	if !strings.Contains(raceRes.Body.String(), `id="leaderboard"`) || !strings.Contains(raceRes.Body.String(), `id="leaderboard-body"`) {
		t.Fatal("race page should render the live leaderboard")
	}
}

func TestRacePageCanSwitchRaceAndRegisterRunner(t *testing.T) {
	svc := race.NewService(testEvent(), testCheckpoints(), nil, 10*time.Minute)
	handler := NewServer(svc, WithProjectStore(&memoryProjectStore{}))

	payload := []byte(`{"name":"Mumbai Marathon 2026","location":"Mumbai","distanceKm":21,"startTime":"2026-02-01T05:30:00Z"}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(payload))
	createReq.Header.Set("Content-Type", "application/json")
	createRes := httptest.NewRecorder()
	handler.ServeHTTP(createRes, createReq)
	if createRes.Code != http.StatusCreated {
		t.Fatalf("create event status = %d, want 201; body: %s", createRes.Code, createRes.Body.String())
	}

	raceReq := httptest.NewRequest(http.MethodGet, "/events/mumbai-marathon-2026/race", nil)
	raceRes := httptest.NewRecorder()
	handler.ServeHTTP(raceRes, raceReq)
	if raceRes.Code != http.StatusOK {
		t.Fatalf("race page status = %d, want 200; body: %s", raceRes.Code, raceRes.Body.String())
	}
	body := raceRes.Body.String()
	if !strings.Contains(body, `id="race-selector"`) {
		t.Fatal("race page should render a race selector")
	}
	if !strings.Contains(body, `/events/mumbai-marathon-2026/race`) {
		t.Fatal("race selector should include the selected race page URL")
	}
	if !strings.Contains(body, `id="registration-form"`) {
		t.Fatal("race page should render runner registration")
	}
}

func TestDashboardFastRegistrationCanSelectRace(t *testing.T) {
	svc := race.NewService(testEvent(), testCheckpoints(), nil, 10*time.Minute)
	handler := NewServer(svc, WithProjectStore(&memoryProjectStore{}))

	payload := []byte(`{"name":"Mumbai Marathon 2026","location":"Mumbai","distanceKm":21,"startTime":"2026-02-01T05:30:00Z"}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(payload))
	createReq.Header.Set("Content-Type", "application/json")
	createRes := httptest.NewRecorder()
	handler.ServeHTTP(createRes, createReq)
	if createRes.Code != http.StatusCreated {
		t.Fatalf("create event status = %d, want 201; body: %s", createRes.Code, createRes.Body.String())
	}

	dashboardReq := httptest.NewRequest(http.MethodGet, "/events/mumbai-marathon-2026", nil)
	dashboardRes := httptest.NewRecorder()
	handler.ServeHTTP(dashboardRes, dashboardReq)
	if dashboardRes.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d, want 200; body: %s", dashboardRes.Code, dashboardRes.Body.String())
	}
	body := dashboardRes.Body.String()
	if !strings.Contains(body, `id="registration-race-selector"`) {
		t.Fatal("fast registration should render a race selector")
	}
	if !strings.Contains(body, `/events/mumbai-marathon-2026`) {
		t.Fatal("fast registration race selector should include the selected dashboard URL")
	}
}

func TestEachMarathonProjectHasIsolatedParticipants(t *testing.T) {
	svc := race.NewService(testEvent(), testCheckpoints(), nil, 10*time.Minute)
	handler := NewServer(svc, WithProjectStore(&memoryProjectStore{}))

	payload := []byte(`{"name":"Mumbai Marathon 2026","location":"Mumbai","distanceKm":21,"startTime":"2026-02-01T05:30:00Z"}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(payload))
	createReq.Header.Set("Content-Type", "application/json")
	createRes := httptest.NewRecorder()
	handler.ServeHTTP(createRes, createReq)
	if createRes.Code != http.StatusCreated {
		t.Fatalf("create event status = %d, want 201; body: %s", createRes.Code, createRes.Body.String())
	}

	registerReq := httptest.NewRequest(http.MethodPost, "/events/mumbai-marathon-2026/api/participants", bytes.NewReader([]byte(`{"name":"Mumbai Runner","phoneNumber":"+91 1"}`)))
	registerReq.Header.Set("Content-Type", "application/json")
	registerRes := httptest.NewRecorder()
	handler.ServeHTTP(registerRes, registerReq)
	if registerRes.Code != http.StatusCreated {
		t.Fatalf("register in second event status = %d, want 201; body: %s", registerRes.Code, registerRes.Body.String())
	}

	defaultReq := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	defaultRes := httptest.NewRecorder()
	handler.ServeHTTP(defaultRes, defaultReq)
	var defaultState race.Snapshot
	if err := json.NewDecoder(defaultRes.Body).Decode(&defaultState); err != nil {
		t.Fatalf("decode default state: %v", err)
	}
	if defaultState.Summary.TotalParticipants != 0 {
		t.Fatalf("default event participants = %d, want 0", defaultState.Summary.TotalParticipants)
	}

	secondReq := httptest.NewRequest(http.MethodGet, "/events/mumbai-marathon-2026/api/state", nil)
	secondRes := httptest.NewRecorder()
	handler.ServeHTTP(secondRes, secondReq)
	var secondState race.Snapshot
	if err := json.NewDecoder(secondRes.Body).Decode(&secondState); err != nil {
		t.Fatalf("decode second state: %v", err)
	}
	if secondState.Summary.TotalParticipants != 1 {
		t.Fatalf("second event participants = %d, want 1", secondState.Summary.TotalParticipants)
	}
}

func testService(t *testing.T) *race.Service {
	t.Helper()
	svc := race.NewService(testEvent(), testCheckpoints(), nil, 10*time.Minute)
	start := time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC)
	slow, err := svc.RegisterParticipant("Maya Iyer", "+91 90000 10001", "")
	if err != nil {
		t.Fatalf("register slow: %v", err)
	}
	fast, err := svc.RegisterParticipant("Dev Rao", "+91 90000 10002", "")
	if err != nil {
		t.Fatalf("register fast: %v", err)
	}
	for _, step := range []struct {
		bib        string
		checkpoint string
		offset     time.Duration
	}{
		{slow.BibNumber, "start", 0},
		{slow.BibNumber, "cp1", 25 * time.Minute},
		{fast.BibNumber, "start", 0},
		{fast.BibNumber, "cp1", 20 * time.Minute},
	} {
		if _, err := svc.RecordCheckpoint(step.bib, step.checkpoint, "vol-1", start.Add(step.offset)); err != nil {
			t.Fatalf("record %s %s: %v", step.bib, step.checkpoint, err)
		}
	}
	return svc
}

func testEvent() race.Event {
	return race.Event{
		ID:          "event-1",
		Name:        "Kochi Marathon 2026",
		Description: "City marathon command center",
		Date:        time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
		StartTime:   time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC),
		Location:    "Kochi, Kerala",
		DistanceKM:  42,
		Status:      race.EventStatusActive,
	}
}

func testCheckpoints() []race.Checkpoint {
	return []race.Checkpoint{
		{ID: "start", Name: "Start", Sequence: 1, DistanceKM: 0},
		{ID: "cp1", Name: "CP1", Sequence: 2, DistanceKM: 5},
		{ID: "cp2", Name: "CP2", Sequence: 3, DistanceKM: 10},
		{ID: "finish", Name: "Finish", Sequence: 4, DistanceKM: 42},
	}
}

func mustRegisterWeb(t *testing.T, svc *race.Service, name string) race.Participant {
	t.Helper()
	participant, err := svc.RegisterParticipant(name, "+91 90000 10000", "")
	if err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	return participant
}

type memoryProjectStore struct {
	states  []race.State
	deleted []string
}

func (s *memoryProjectStore) Load(_ context.Context) (race.State, bool, error) {
	if len(s.states) == 0 {
		return race.State{}, false, nil
	}
	return s.states[len(s.states)-1], true, nil
}

func (s *memoryProjectStore) Save(_ context.Context, state race.State) error {
	s.states = append(s.states, state)
	return nil
}

func (s *memoryProjectStore) Delete(_ context.Context, id string) error {
	s.deleted = append(s.deleted, id)
	return nil
}

func loginCookie(t *testing.T, handler http.Handler, username string, password string) *http.Cookie {
	t.Helper()
	form := url.Values{"username": {username}, "password": {password}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("login status=%d, want 303; body: %s", res.Code, res.Body.String())
	}
	cookies := res.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login did not set a cookie")
	}
	return cookies[0]
}
