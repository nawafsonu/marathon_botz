package race

import (
	"context"
	"testing"
	"time"
)

func TestRegisterParticipantGeneratesSequentialBibs(t *testing.T) {
	svc := NewService(seedEvent(), seedCheckpoints(), nil, 10*time.Minute)

	first, err := svc.RegisterParticipant("  Maya Iyer  ", "  +91 90000 10001  ", "")
	if err != nil {
		t.Fatalf("register first participant: %v", err)
	}
	second, err := svc.RegisterParticipant("Arjun Nair", "+91 90000 10002", "pacemaker")
	if err != nil {
		t.Fatalf("register second participant: %v", err)
	}

	if first.BibNumber != "BIB-001" {
		t.Fatalf("first bib = %s, want BIB-001", first.BibNumber)
	}
	if second.BibNumber != "BIB-002" {
		t.Fatalf("second bib = %s, want BIB-002", second.BibNumber)
	}
	if first.Name != "Maya Iyer" || first.PhoneNumber != "+91 90000 10001" {
		t.Fatalf("participant was not normalized: %+v", first)
	}
}

func TestRecordCheckpointRejectsInvalidAndOutOfOrderEntries(t *testing.T) {
	svc := NewService(seedEvent(), seedCheckpoints(), nil, 10*time.Minute)
	runner, err := svc.RegisterParticipant("Maya Iyer", "+91 90000 10001", "")
	if err != nil {
		t.Fatalf("register participant: %v", err)
	}
	now := time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC)

	if _, err := svc.RecordCheckpoint("BIB-404", "start", "vol-1", now); err == nil {
		t.Fatal("invalid bib was accepted")
	}
	if _, err := svc.RecordCheckpoint(runner.BibNumber, "cp2", "vol-1", now); err == nil {
		t.Fatal("future checkpoint before previous checkpoint was accepted")
	}
	if _, err := svc.RecordCheckpoint(runner.BibNumber, "start", "vol-1", now); err != nil {
		t.Fatalf("record start: %v", err)
	}
	if _, err := svc.RecordCheckpoint(runner.BibNumber, "start", "vol-1", now.Add(2*time.Minute)); err == nil {
		t.Fatal("duplicate checkpoint within prevention window was accepted")
	}
}

func TestLeaderboardRanksByProgressThenFinishTime(t *testing.T) {
	svc := NewService(seedEvent(), seedCheckpoints(), nil, 10*time.Minute)
	start := time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC)
	arya := mustRegister(t, svc, "Arya Menon")
	dev := mustRegister(t, svc, "Dev Rao")
	nila := mustRegister(t, svc, "Nila Shah")

	mustLog(t, svc, arya.BibNumber, "start", start)
	mustLog(t, svc, arya.BibNumber, "cp1", start.Add(25*time.Minute))
	mustLog(t, svc, arya.BibNumber, "cp2", start.Add(54*time.Minute))
	mustLog(t, svc, arya.BibNumber, "finish", start.Add(90*time.Minute))

	mustLog(t, svc, dev.BibNumber, "start", start)
	mustLog(t, svc, dev.BibNumber, "cp1", start.Add(20*time.Minute))
	mustLog(t, svc, dev.BibNumber, "cp2", start.Add(48*time.Minute))
	mustLog(t, svc, dev.BibNumber, "finish", start.Add(82*time.Minute))

	mustLog(t, svc, nila.BibNumber, "start", start)
	mustLog(t, svc, nila.BibNumber, "cp1", start.Add(19*time.Minute))

	leaderboard := svc.Leaderboard()
	if got, want := leaderboard[0].BibNumber, dev.BibNumber; got != want {
		t.Fatalf("leader = %s, want %s", got, want)
	}
	if got, want := leaderboard[1].BibNumber, arya.BibNumber; got != want {
		t.Fatalf("second = %s, want %s", got, want)
	}
	if got, want := leaderboard[2].BibNumber, nila.BibNumber; got != want {
		t.Fatalf("third = %s, want %s", got, want)
	}
	if leaderboard[0].Gap != "leader" {
		t.Fatalf("leader gap = %q, want leader", leaderboard[0].Gap)
	}
}

func TestSummaryCountsRaceStatuses(t *testing.T) {
	svc := NewService(seedEvent(), seedCheckpoints(), nil, 10*time.Minute)
	start := time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC)
	finished := mustRegister(t, svc, "Finished Runner")
	active := mustRegister(t, svc, "Active Runner")
	dnf := mustRegister(t, svc, "DNF Runner")

	mustLog(t, svc, finished.BibNumber, "start", start)
	mustLog(t, svc, finished.BibNumber, "cp1", start.Add(20*time.Minute))
	mustLog(t, svc, finished.BibNumber, "cp2", start.Add(45*time.Minute))
	mustLog(t, svc, finished.BibNumber, "finish", start.Add(80*time.Minute))
	mustLog(t, svc, active.BibNumber, "start", start)
	if err := svc.MarkDNF(dnf.BibNumber); err != nil {
		t.Fatalf("mark dnf: %v", err)
	}

	summary := svc.Summary()
	if summary.TotalParticipants != 3 || summary.Finished != 1 || summary.Active != 1 || summary.DNF != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if summary.CompletionRate != 33 {
		t.Fatalf("completion rate = %d, want 33", summary.CompletionRate)
	}
}

func TestServicePersistsStateAfterMutation(t *testing.T) {
	svc := NewService(seedEvent(), seedCheckpoints(), nil, 10*time.Minute)
	store := &recordingStore{}
	if err := svc.UseStore(store); err != nil {
		t.Fatalf("use store: %v", err)
	}

	participant, err := svc.RegisterParticipant("Maya Iyer", "+91 90000 10001", "")
	if err != nil {
		t.Fatalf("register participant: %v", err)
	}
	if _, err := svc.RecordCheckpoint(participant.BibNumber, "start", "vol-1", time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("record checkpoint: %v", err)
	}

	if store.saves < 3 {
		t.Fatalf("save count = %d, want at least 3", store.saves)
	}
	if len(store.last.Participants) != 1 || len(store.last.Logs) != 1 {
		t.Fatalf("persisted state = %+v", store.last)
	}
}

func TestUpdateEventSettingsChangesDistanceAndStartTime(t *testing.T) {
	svc := NewService(seedEvent(), seedCheckpoints(), nil, 10*time.Minute)
	start := time.Date(2026, 1, 10, 5, 30, 0, 0, time.UTC)

	updated, err := svc.UpdateEventSettings(21, start)
	if err != nil {
		t.Fatalf("update event settings: %v", err)
	}

	if updated.DistanceKM != 21 {
		t.Fatalf("distance = %d, want 21", updated.DistanceKM)
	}
	if !updated.StartTime.Equal(start) {
		t.Fatalf("start time = %s, want %s", updated.StartTime, start)
	}
}

func TestStartRaceMarksEventActiveAndPersists(t *testing.T) {
	event := seedEvent()
	event.Status = EventStatusUpcoming
	svc := NewService(event, seedCheckpoints(), nil, 10*time.Minute)
	store := &recordingStore{}
	if err := svc.UseStore(store); err != nil {
		t.Fatalf("use store: %v", err)
	}

	updated, err := svc.StartRace()
	if err != nil {
		t.Fatalf("start race: %v", err)
	}

	if updated.Status != EventStatusActive {
		t.Fatalf("status = %s, want %s", updated.Status, EventStatusActive)
	}
	if svc.Event().Status != EventStatusActive {
		t.Fatalf("service status = %s, want %s", svc.Event().Status, EventStatusActive)
	}
	if store.last.Event.Status != EventStatusActive {
		t.Fatalf("persisted status = %s, want %s", store.last.Event.Status, EventStatusActive)
	}
}

func TestImportParticipantsUsesMappedBibAndNameColumns(t *testing.T) {
	svc := NewService(seedEvent(), seedCheckpoints(), nil, 10*time.Minute)
	rows := []ImportParticipant{
		{BibNumber: "17", Name: "Anika Das", PhoneNumber: "+91 90000 10017"},
		{BibNumber: "BIB-018", Name: "Rohan Paul", PhoneNumber: "+91 90000 10018"},
	}

	result := svc.ImportParticipants(rows)

	if result.Created != 2 || len(result.Errors) != 0 {
		t.Fatalf("import result = %+v", result)
	}
	participants := svc.Participants()
	if participants[0].BibNumber != "BIB-017" || participants[1].BibNumber != "BIB-018" {
		t.Fatalf("imported bibs = %s, %s", participants[0].BibNumber, participants[1].BibNumber)
	}
	next, err := svc.RegisterParticipant("Next Runner", "+91 90000 10019", "")
	if err != nil {
		t.Fatalf("register next: %v", err)
	}
	if next.BibNumber != "BIB-019" {
		t.Fatalf("next bib = %s, want BIB-019", next.BibNumber)
	}
}

func TestAddCheckpointInsertsCheckpointBySequence(t *testing.T) {
	svc := NewService(seedEvent(), seedCheckpoints(), nil, 10*time.Minute)

	checkpoint, err := svc.AddCheckpoint("CP1.5", 3, 7.5)
	if err != nil {
		t.Fatalf("add checkpoint: %v", err)
	}

	if checkpoint.ID != "cp15" {
		t.Fatalf("checkpoint id = %s, want cp15", checkpoint.ID)
	}
	checkpoints := svc.Checkpoints()
	if checkpoints[2].Name != "CP1.5" || checkpoints[2].Sequence != 3 {
		t.Fatalf("checkpoint was not inserted at sequence 3: %+v", checkpoints)
	}
	if checkpoints[3].Name != "CP2" || checkpoints[3].Sequence != 4 {
		t.Fatalf("later checkpoints were not shifted: %+v", checkpoints)
	}
}

type recordingStore struct {
	last  State
	saves int
}

func (s *recordingStore) Load(ctx context.Context) (State, bool, error) {
	return State{}, false, nil
}

func (s *recordingStore) Save(ctx context.Context, state State) error {
	s.saves++
	s.last = state
	return nil
}

func seedEvent() Event {
	return Event{
		ID:          "event-1",
		Name:        "Kochi Marathon 2026",
		Description: "City marathon command center",
		Date:        time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC),
		StartTime:   time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC),
		Location:    "Kochi, Kerala",
		DistanceKM:  42,
		Status:      EventStatusActive,
	}
}

func seedCheckpoints() []Checkpoint {
	return []Checkpoint{
		{ID: "start", Name: "Start", Sequence: 1, DistanceKM: 0},
		{ID: "cp1", Name: "CP1", Sequence: 2, DistanceKM: 5},
		{ID: "cp2", Name: "CP2", Sequence: 3, DistanceKM: 10},
		{ID: "finish", Name: "Finish", Sequence: 4, DistanceKM: 42},
	}
}

func mustRegister(t *testing.T, svc *Service, name string) Participant {
	t.Helper()
	participant, err := svc.RegisterParticipant(name, "+91 90000 10000", "")
	if err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	return participant
}

func mustLog(t *testing.T, svc *Service, bib string, checkpoint string, at time.Time) {
	t.Helper()
	if _, err := svc.RecordCheckpoint(bib, checkpoint, "vol-1", at); err != nil {
		t.Fatalf("record %s at %s: %v", bib, checkpoint, err)
	}
}
