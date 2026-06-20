package race

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGenerateCheckpoints(t *testing.T) {
	noIntermediate := GenerateCheckpoints(10, 0)
	if len(noIntermediate) != 2 {
		t.Fatalf("count 0 = %d checkpoints, want 2 (Start, Finish)", len(noIntermediate))
	}
	if noIntermediate[0].ID != "start" || noIntermediate[1].ID != "finish" {
		t.Fatalf("unexpected ids: %s, %s", noIntermediate[0].ID, noIntermediate[1].ID)
	}
	if noIntermediate[1].DistanceKM != 10 {
		t.Fatalf("finish distance = %v, want 10", noIntermediate[1].DistanceKM)
	}

	course := GenerateCheckpoints(20, 3)
	wantNames := []string{"Start", "CP1", "CP2", "CP3", "Finish"}
	if len(course) != len(wantNames) {
		t.Fatalf("count 3 = %d checkpoints, want %d", len(course), len(wantNames))
	}
	for i, cp := range course {
		if cp.Name != wantNames[i] {
			t.Fatalf("checkpoint %d name = %q, want %q", i, cp.Name, wantNames[i])
		}
		if cp.Sequence != i+1 {
			t.Fatalf("checkpoint %d sequence = %d, want %d", i, cp.Sequence, i+1)
		}
	}
	// Evenly spaced every 5 km across a 20 km course.
	for i, want := range []float64{0, 5, 10, 15, 20} {
		if course[i].DistanceKM != want {
			t.Fatalf("checkpoint %d distance = %v, want %v", i, course[i].DistanceKM, want)
		}
	}
}

func TestRegisterParticipantGeneratesSequentialBibs(t *testing.T) {
	svc := NewService(seedEvent(), seedCheckpoints(), nil, 10*time.Minute)

	first, err := svc.RegisterParticipant("  Maya Iyer  ", "  +91 90000 10001  ", "", "")
	if err != nil {
		t.Fatalf("register first participant: %v", err)
	}
	second, err := svc.RegisterParticipant("Arjun Nair", "+91 90000 10002", "", "pacemaker")
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
	runner, err := svc.RegisterParticipant("Maya Iyer", "+91 90000 10001", "", "")
	if err != nil {
		t.Fatalf("register participant: %v", err)
	}
	now := time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC)

	if _, err := svc.RecordCheckpoint("BIB-404", "start", "vol-1", now); err == nil {
		t.Fatal("invalid bib was accepted")
	}
	// Recording a future checkpoint as first scan (skipped start) is allowed under improved logic
	if _, err := svc.RecordCheckpoint(runner.BibNumber, "cp2", "vol-1", now); err != nil {
		t.Fatalf("recording cp2 as first checkpoint (skipped start) should be allowed: %v", err)
	}
	// Recording start afterwards (sequence 1 <= last sequence 3) must be rejected
	if _, err := svc.RecordCheckpoint(runner.BibNumber, "start", "vol-1", now.Add(time.Minute)); err == nil {
		t.Fatal("recording start after cp2 (out of sequence order) should be rejected")
	}
	// Chronological out-of-order (scan at finish with time before CP2 scan) must be rejected
	if _, err := svc.RecordCheckpoint(runner.BibNumber, "finish", "vol-1", now.Add(-time.Minute)); err == nil {
		t.Fatal("chronological out-of-order scan should be rejected")
	}
	// Duplicate checkpoint must be rejected
	if _, err := svc.RecordCheckpoint(runner.BibNumber, "cp2", "vol-1", now.Add(2*time.Minute)); err == nil {
		t.Fatal("duplicate checkpoint scan should be rejected")
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

func TestLeaderboardGapUsesLiveCheckpointTiming(t *testing.T) {
	svc := NewService(seedEvent(), seedCheckpoints(), nil, 10*time.Minute)
	start := time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC)
	leader := mustRegister(t, svc, "Leader")
	sameCheckpoint := mustRegister(t, svc, "Same Checkpoint")
	behind := mustRegister(t, svc, "Behind")

	mustLog(t, svc, leader.BibNumber, "start", start)
	mustLog(t, svc, leader.BibNumber, "cp1", start.Add(20*time.Minute))
	mustLog(t, svc, leader.BibNumber, "cp2", start.Add(45*time.Minute))

	mustLog(t, svc, sameCheckpoint.BibNumber, "start", start)
	mustLog(t, svc, sameCheckpoint.BibNumber, "cp1", start.Add(22*time.Minute))
	mustLog(t, svc, sameCheckpoint.BibNumber, "cp2", start.Add(50*time.Minute))

	mustLog(t, svc, behind.BibNumber, "start", start)
	mustLog(t, svc, behind.BibNumber, "cp1", start.Add(27*time.Minute))

	leaderboard := svc.Leaderboard()
	if got := leaderboard[1].Gap; got != "+5m 00s @ CP2" {
		t.Fatalf("same checkpoint gap = %q, want +5m 00s @ CP2", got)
	}
	if got := leaderboard[2].Gap; got != "+7m 00s @ CP1" {
		t.Fatalf("behind checkpoint gap = %q, want +7m 00s @ CP1", got)
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
	if summary.CourseProgress != 41 {
		t.Fatalf("course progress = %d, want 41", summary.CourseProgress)
	}
}

func TestServicePersistsStateAfterMutation(t *testing.T) {
	svc := NewService(seedEvent(), seedCheckpoints(), nil, 10*time.Minute)
	store := &recordingStore{}
	if err := svc.UseStore(store); err != nil {
		t.Fatalf("use store: %v", err)
	}

	participant, err := svc.RegisterParticipant("Maya Iyer", "+91 90000 10001", "", "")
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
	event.StartTime = time.Date(2026, 1, 10, 1, 0, 0, 0, time.UTC)
	svc := NewService(event, seedCheckpoints(), nil, 10*time.Minute)
	store := &recordingStore{}
	if err := svc.UseStore(store); err != nil {
		t.Fatalf("use store: %v", err)
	}

	before := time.Now().UTC()
	updated, err := svc.StartRace()
	if err != nil {
		t.Fatalf("start race: %v", err)
	}
	after := time.Now().UTC()

	if updated.Status != EventStatusActive {
		t.Fatalf("status = %s, want %s", updated.Status, EventStatusActive)
	}
	if updated.StartTime.Before(before) || updated.StartTime.After(after) {
		t.Fatalf("start time = %s, want actual click time between %s and %s", updated.StartTime, before, after)
	}
	if svc.Event().Status != EventStatusActive {
		t.Fatalf("service status = %s, want %s", svc.Event().Status, EventStatusActive)
	}
	if store.last.Event.Status != EventStatusActive {
		t.Fatalf("persisted status = %s, want %s", store.last.Event.Status, EventStatusActive)
	}
	if !store.last.Event.StartTime.Equal(updated.StartTime) {
		t.Fatalf("persisted start time = %s, want %s", store.last.Event.StartTime, updated.StartTime)
	}
}

func TestStartRaceHonorsScheduledStartTimeWhenStartedEarly(t *testing.T) {
	event := seedEvent()
	event.Status = EventStatusUpcoming
	// Scheduled comfortably in the future, so starting now counts as an early start.
	scheduled := time.Now().UTC().Add(2 * time.Hour)
	event.StartTime = scheduled
	svc := NewService(event, seedCheckpoints(), nil, 10*time.Minute)

	participant, err := svc.RegisterParticipant("Scheduled Runner", "+91 90000 10001", "", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	updated, err := svc.StartRace()
	if err != nil {
		t.Fatalf("start race: %v", err)
	}
	if !updated.StartTime.Equal(scheduled) {
		t.Fatalf("start time = %s, want scheduled %s", updated.StartTime, scheduled)
	}

	// The recorded Start checkpoint should use the scheduled time too.
	profile, err := svc.RunnerProfile(participant.BibNumber)
	if err != nil {
		t.Fatalf("runner profile: %v", err)
	}
	if len(profile.Timeline) != 1 || !profile.Timeline[0].Timestamp.Equal(scheduled) {
		t.Fatalf("start checkpoint timestamp = %v, want scheduled %s", profile.Timeline, scheduled)
	}
}

func TestStartRaceRecordsStartCheckpointForRegisteredRunners(t *testing.T) {
	event := seedEvent()
	event.Status = EventStatusUpcoming
	svc := NewService(event, seedCheckpoints(), nil, 10*time.Minute)
	first, err := svc.RegisterParticipant("First Runner", "+91 90000 10001", "", "")
	if err != nil {
		t.Fatalf("register first: %v", err)
	}
	second, err := svc.RegisterParticipant("Second Runner", "+91 90000 10002", "", "")
	if err != nil {
		t.Fatalf("register second: %v", err)
	}

	if _, err := svc.StartRace(); err != nil {
		t.Fatalf("start race: %v", err)
	}

	for _, participant := range []Participant{first, second} {
		profile, err := svc.RunnerProfile(participant.BibNumber)
		if err != nil {
			t.Fatalf("runner profile %s: %v", participant.BibNumber, err)
		}
		timeline := profile.Timeline
		if len(timeline) != 1 {
			t.Fatalf("%s timeline length = %d, want 1", participant.BibNumber, len(timeline))
		}
		if timeline[0].Checkpoint.ID != "start" {
			t.Fatalf("%s first checkpoint = %s, want start", participant.BibNumber, timeline[0].Checkpoint.ID)
		}
		if timeline[0].Timestamp.IsZero() {
			t.Fatalf("%s start timestamp is zero", participant.BibNumber)
		}
	}
	if _, err := svc.RecordCheckpoint(first.BibNumber, "cp1", "vol-1", time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("record cp1 after start race: %v", err)
	}

	logCount := len(svc.RecentLogs(10))
	if _, err := svc.StartRace(); err != nil {
		t.Fatalf("start race again: %v", err)
	}
	if got := len(svc.RecentLogs(10)); got != logCount {
		t.Fatalf("start race was not idempotent; logs = %d, want %d", got, logCount)
	}
}

func TestDeleteParticipantRemovesProfileAndLogs(t *testing.T) {
	svc := NewService(seedEvent(), seedCheckpoints(), nil, 10*time.Minute)
	store := &recordingStore{}
	if err := svc.UseStore(store); err != nil {
		t.Fatalf("use store: %v", err)
	}
	participant := mustRegister(t, svc, "Delete Runner")
	mustRegister(t, svc, "Keep Runner")
	mustLog(t, svc, participant.BibNumber, "start", time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC))

	if err := svc.DeleteParticipant("001"); err != nil {
		t.Fatalf("delete participant: %v", err)
	}

	if _, err := svc.RunnerProfile(participant.BibNumber); !errors.Is(err, ErrInvalidBib) {
		t.Fatalf("runner profile error = %v, want ErrInvalidBib", err)
	}
	if len(svc.Participants()) != 1 {
		t.Fatalf("participants length = %d, want 1", len(svc.Participants()))
	}
	if len(svc.RecentLogs(10)) != 0 {
		t.Fatalf("logs length = %d, want 0", len(svc.RecentLogs(10)))
	}
	if len(store.last.Participants) != 1 || len(store.last.Logs) != 0 {
		t.Fatalf("persisted state = %+v", store.last)
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
	next, err := svc.RegisterParticipant("Next Runner", "+91 90000 10019", "", "")
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

func TestAddCheckpointCannotPrecedeStart(t *testing.T) {
	svc := NewService(seedEvent(), seedCheckpoints(), nil, 10*time.Minute)

	if _, err := svc.AddCheckpoint("Pre Start", 1, 0); err == nil {
		t.Fatal("checkpoint before Start was accepted")
	}
	if got := svc.Checkpoints()[0].ID; got != "start" {
		t.Fatalf("first checkpoint = %s, want start", got)
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
	participant, err := svc.RegisterParticipant(name, "+91 90000 10000", "", "")
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

func TestCategoryLeaderboardsIndependentRanking(t *testing.T) {
	event := seedEvent()
	event.Categories = []string{"5 KM", "10 KM"}
	svc := NewService(event, seedCheckpoints(), nil, 10*time.Minute)
	start := time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC)

	// Register runners in 5 KM category
	runnerA, err := svc.RegisterParticipant("Runner A (5K)", "+91 90000 10001", "5 KM", "")
	if err != nil {
		t.Fatalf("register Runner A: %v", err)
	}
	runnerB, err := svc.RegisterParticipant("Runner B (5K)", "+91 90000 10002", "5 KM", "")
	if err != nil {
		t.Fatalf("register Runner B: %v", err)
	}

	// Register runners in 10 KM category
	runnerC, err := svc.RegisterParticipant("Runner C (10K)", "+91 90000 10003", "10 KM", "")
	if err != nil {
		t.Fatalf("register Runner C: %v", err)
	}
	runnerD, err := svc.RegisterParticipant("Runner D (10K)", "+91 90000 10004", "10 KM", "")
	if err != nil {
		t.Fatalf("register Runner D: %v", err)
	}

	// Log checkpoints to establish leaders and rankings
	// For 5 KM: Runner A is ahead of Runner B
	mustLog(t, svc, runnerA.BibNumber, "start", start)
	mustLog(t, svc, runnerA.BibNumber, "cp1", start.Add(20*time.Minute))

	mustLog(t, svc, runnerB.BibNumber, "start", start)
	mustLog(t, svc, runnerB.BibNumber, "cp1", start.Add(25*time.Minute))

	// For 10 KM: Runner D is ahead of Runner C (C started CP1 later than D)
	mustLog(t, svc, runnerC.BibNumber, "start", start)
	mustLog(t, svc, runnerC.BibNumber, "cp1", start.Add(30*time.Minute))

	mustLog(t, svc, runnerD.BibNumber, "start", start)
	mustLog(t, svc, runnerD.BibNumber, "cp1", start.Add(28*time.Minute))

	// Get Snapshot & verify CategoryLeaderboards
	snapshot := svc.Snapshot()
	catBoards := snapshot.CategoryLeaderboards
	if len(catBoards) != 2 {
		t.Fatalf("expected 2 category leaderboards, got %d", len(catBoards))
	}

	// Check 5 KM leaderboard
	var board5KM CategoryLeaderboard
	var board10KM CategoryLeaderboard
	for _, cb := range catBoards {
		if cb.Category == "5 KM" {
			board5KM = cb
		} else if cb.Category == "10 KM" {
			board10KM = cb
		}
	}

	if len(board5KM.Entries) != 2 {
		t.Fatalf("expected 2 entries in 5 KM leaderboard, got %d", len(board5KM.Entries))
	}
	if board5KM.Entries[0].BibNumber != runnerA.BibNumber {
		t.Fatalf("expected Runner A to lead 5 KM, got %s", board5KM.Entries[0].BibNumber)
	}
	if board5KM.Entries[0].Rank != 1 || board5KM.Entries[0].Gap != "leader" {
		t.Fatalf("unexpected rank/gap for 5 KM leader: rank=%d gap=%s", board5KM.Entries[0].Rank, board5KM.Entries[0].Gap)
	}
	if board5KM.Entries[1].BibNumber != runnerB.BibNumber {
		t.Fatalf("expected Runner B to be second in 5 KM, got %s", board5KM.Entries[1].BibNumber)
	}
	if board5KM.Entries[1].Rank != 2 || board5KM.Entries[1].Gap != "+5m 00s @ CP1" {
		t.Fatalf("unexpected rank/gap for 5 KM second: rank=%d gap=%s", board5KM.Entries[1].Rank, board5KM.Entries[1].Gap)
	}

	// Check 10 KM leaderboard
	if len(board10KM.Entries) != 2 {
		t.Fatalf("expected 2 entries in 10 KM leaderboard, got %d", len(board10KM.Entries))
	}
	if board10KM.Entries[0].BibNumber != runnerD.BibNumber {
		t.Fatalf("expected Runner D to lead 10 KM, got %s", board10KM.Entries[0].BibNumber)
	}
	if board10KM.Entries[0].Rank != 1 || board10KM.Entries[0].Gap != "leader" {
		t.Fatalf("unexpected rank/gap for 10 KM leader: rank=%d gap=%s", board10KM.Entries[0].Rank, board10KM.Entries[0].Gap)
	}
	if board10KM.Entries[1].BibNumber != runnerC.BibNumber {
		t.Fatalf("expected Runner C to be second in 10 KM, got %s", board10KM.Entries[1].BibNumber)
	}
	if board10KM.Entries[1].Rank != 2 || board10KM.Entries[1].Gap != "+2m 00s @ CP1" {
		t.Fatalf("unexpected rank/gap for 10 KM second: rank=%d gap=%s", board10KM.Entries[1].Rank, board10KM.Entries[1].Gap)
	}
}

func TestLeaderboardRobustRankingAndFallbackGunTime(t *testing.T) {
	event := seedEvent()
	event.StartTime = time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC)
	svc := NewService(event, seedCheckpoints(), nil, 10*time.Minute)
	start := event.StartTime

	// 1. Runner A starts normally and finishes
	runnerA := mustRegister(t, svc, "Runner A")
	mustLog(t, svc, runnerA.BibNumber, "start", start)
	mustLog(t, svc, runnerA.BibNumber, "cp1", start.Add(20*time.Minute))
	mustLog(t, svc, runnerA.BibNumber, "finish", start.Add(50*time.Minute))

	// 2. Runner B misses start mat, but registers cp1 and finish
	runnerB := mustRegister(t, svc, "Runner B")
	// Skips start mat scan
	mustLog(t, svc, runnerB.BibNumber, "cp1", start.Add(22*time.Minute))
	mustLog(t, svc, runnerB.BibNumber, "finish", start.Add(52*time.Minute))

	// 3. Runner C starts, reaches cp1, but DNFs
	runnerC := mustRegister(t, svc, "Runner C")
	mustLog(t, svc, runnerC.BibNumber, "start", start)
	mustLog(t, svc, runnerC.BibNumber, "cp1", start.Add(25*time.Minute))
	if err := svc.MarkDNF(runnerC.BibNumber); err != nil {
		t.Fatalf("mark dnf Runner C: %v", err)
	}

	// 4. Runner D is registered but never starts (DNS)
	runnerD := mustRegister(t, svc, "Runner D")

	// Get leaderboard
	leaderboard := svc.Leaderboard()
	if len(leaderboard) != 4 {
		t.Fatalf("expected 4 leaderboard entries, got %d", len(leaderboard))
	}

	// Order should be: Runner A (finished), Runner B (finished), Runner C (DNF), Runner D (DNS/Registered)
	if leaderboard[0].BibNumber != runnerA.BibNumber {
		t.Fatalf("rank 1 expected Runner A, got %s", leaderboard[0].BibNumber)
	}
	if leaderboard[1].BibNumber != runnerB.BibNumber {
		t.Fatalf("rank 2 expected Runner B, got %s", leaderboard[1].BibNumber)
	}
	if leaderboard[2].BibNumber != runnerC.BibNumber {
		t.Fatalf("rank 3 expected Runner C, got %s", leaderboard[2].BibNumber)
	}
	if leaderboard[3].BibNumber != runnerD.BibNumber {
		t.Fatalf("rank 4 expected Runner D, got %s", leaderboard[3].BibNumber)
	}

	// Verify Runner B finish/race duration fell back to Gun Time
	if leaderboard[1].RaceTime != "52m 00s" {
		t.Fatalf("expected Runner B fallback race time to be 52m 00s, got %s", leaderboard[1].RaceTime)
	}
}
