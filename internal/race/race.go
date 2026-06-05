package race

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type EventStatus string

const (
	EventStatusUpcoming  EventStatus = "Upcoming"
	EventStatusActive    EventStatus = "Active"
	EventStatusCompleted EventStatus = "Completed"
)

type RaceStatus string

const (
	RaceStatusRegistered RaceStatus = "Registered"
	RaceStatusStarted    RaceStatus = "Started"
	RaceStatusActive     RaceStatus = "Active"
	RaceStatusFinished   RaceStatus = "Finished"
	RaceStatusDNF        RaceStatus = "DNF"
)

var (
	ErrInvalidParticipant = errors.New("participant name and phone number are required")
	ErrInvalidBib         = errors.New("bib number was not found")
	ErrInvalidCheckpoint  = errors.New("checkpoint was not found")
	ErrDuplicateEntry     = errors.New("checkpoint already recorded for this runner")
	ErrOutOfOrderEntry    = errors.New("previous checkpoint must be recorded first")
)

var nonCheckpointIDChars = regexp.MustCompile(`[^a-z0-9]+`)

type Event struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Date        time.Time   `json:"date"`
	StartTime   time.Time   `json:"startTime"`
	Location    string      `json:"location"`
	DistanceKM  int         `json:"distanceKm"`
	Status      EventStatus `json:"status"`
}

type Checkpoint struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Sequence   int     `json:"sequence"`
	DistanceKM float64 `json:"distanceKm"`
}

type Participant struct {
	ID          string     `json:"id"`
	BibNumber   string     `json:"bibNumber"`
	Name        string     `json:"name"`
	PhoneNumber string     `json:"phoneNumber"`
	Notes       string     `json:"notes"`
	Status      RaceStatus `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type CheckpointLog struct {
	ID           string      `json:"id"`
	EventID      string      `json:"eventId"`
	Participant  Participant `json:"participant"`
	Checkpoint   Checkpoint  `json:"checkpoint"`
	Timestamp    time.Time   `json:"timestamp"`
	VolunteerID  string      `json:"volunteerId"`
	DisplayLabel string      `json:"displayLabel"`
}

type LeaderboardEntry struct {
	Rank               int        `json:"rank"`
	BibNumber          string     `json:"bibNumber"`
	RunnerName         string     `json:"runnerName"`
	Status             RaceStatus `json:"status"`
	LatestCheckpoint   string     `json:"latestCheckpoint"`
	LatestSequence     int        `json:"latestSequence"`
	FinishTime         string     `json:"finishTime"`
	RaceTime           string     `json:"raceTime"`
	Gap                string     `json:"gap"`
	PositionDeltaLabel string     `json:"positionDeltaLabel"`
}

type Summary struct {
	TotalParticipants int    `json:"totalParticipants"`
	Finished          int    `json:"finished"`
	Active            int    `json:"active"`
	DNF               int    `json:"dnf"`
	Registered        int    `json:"registered"`
	CompletionRate    int    `json:"completionRate"`
	CourseProgress    int    `json:"courseProgress"`
	AverageFinishTime string `json:"averageFinishTime"`
}

type Segment struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Duration string `json:"duration"`
}

type RunnerProfile struct {
	Participant     Participant        `json:"participant"`
	Summary         LeaderboardEntry   `json:"summary"`
	Timeline        []CheckpointLog    `json:"timeline"`
	Segments        []Segment          `json:"segments"`
	PositionHistory []LeaderboardEntry `json:"positionHistory"`
}

type ImportParticipant struct {
	BibNumber   string `json:"bibNumber"`
	Name        string `json:"name"`
	PhoneNumber string `json:"phoneNumber"`
	Notes       string `json:"notes"`
}

type ImportError struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

type ImportResult struct {
	Created      int           `json:"created"`
	Participants []Participant `json:"participants"`
	Errors       []ImportError `json:"errors"`
}

type Snapshot struct {
	Event        Event              `json:"event"`
	Summary      Summary            `json:"summary"`
	Checkpoints  []Checkpoint       `json:"checkpoints"`
	Leaderboard  []LeaderboardEntry `json:"leaderboard"`
	LiveFeed     []CheckpointLog    `json:"liveFeed"`
	Participants []Participant      `json:"participants"`
}

type State struct {
	Event        Event           `json:"event" bson:"event"`
	Checkpoints  []Checkpoint    `json:"checkpoints" bson:"checkpoints"`
	Participants []Participant   `json:"participants" bson:"participants"`
	Logs         []CheckpointLog `json:"logs" bson:"logs"`
}

type Store interface {
	Load(ctx context.Context) (State, bool, error)
	Save(ctx context.Context, state State) error
}

type Service struct {
	mu               sync.RWMutex
	event            Event
	checkpoints      []Checkpoint
	checkpointByID   map[string]Checkpoint
	participants     []Participant
	participantByBib map[string]int
	logs             []CheckpointLog
	duplicateWindow  time.Duration
	nextParticipant  int
	nextLog          int
	store            Store
}

func NewService(event Event, checkpoints []Checkpoint, participants []Participant, duplicateWindow time.Duration) *Service {
	return NewServiceWithLogs(event, checkpoints, participants, nil, duplicateWindow)
}

func NewServiceWithLogs(event Event, checkpoints []Checkpoint, participants []Participant, logs []CheckpointLog, duplicateWindow time.Duration) *Service {
	ordered := append([]Checkpoint(nil), checkpoints...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Sequence < ordered[j].Sequence
	})

	svc := &Service{
		event:            event,
		checkpoints:      ordered,
		checkpointByID:   make(map[string]Checkpoint, len(ordered)),
		participants:     append([]Participant(nil), participants...),
		participantByBib: make(map[string]int, len(participants)),
		logs:             append([]CheckpointLog(nil), logs...),
		duplicateWindow:  duplicateWindow,
		nextParticipant:  1,
		nextLog:          1,
	}
	for _, checkpoint := range ordered {
		svc.checkpointByID[checkpoint.ID] = checkpoint
	}
	for i, participant := range svc.participants {
		svc.participantByBib[participant.BibNumber] = i
		if n := bibNumber(participant.BibNumber); n >= svc.nextParticipant {
			svc.nextParticipant = n + 1
		}
	}
	for _, log := range svc.logs {
		if n := logNumber(log.ID); n >= svc.nextLog {
			svc.nextLog = n + 1
		}
	}
	return svc
}

func NewServiceFromState(state State, duplicateWindow time.Duration) *Service {
	return NewServiceWithLogs(state.Event, state.Checkpoints, state.Participants, state.Logs, duplicateWindow)
}

func (s *Service) UseStore(store Store) error {
	s.mu.Lock()
	s.store = store
	state := s.stateLocked()
	s.mu.Unlock()
	if store == nil {
		return nil
	}
	return store.Save(context.Background(), state)
}

func (s *Service) Event() Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.event
}

func (s *Service) Checkpoints() []Checkpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Checkpoint(nil), s.checkpoints...)
}

func (s *Service) Participants() []Participant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Participant(nil), s.participants...)
}

func (s *Service) UpdateEventSettings(distanceKM int, startTime time.Time) (Event, error) {
	if distanceKM <= 0 {
		return Event{}, errors.New("distance must be greater than zero")
	}
	if startTime.IsZero() {
		return Event{}, errors.New("start time is required")
	}

	s.mu.Lock()
	s.event.DistanceKM = distanceKM
	s.event.StartTime = startTime.UTC()
	state := s.stateLocked()
	store := s.store
	updated := s.event
	s.mu.Unlock()
	persist(store, state)
	return updated, nil
}

func (s *Service) StartRace() (Event, error) {
	startedAt := time.Now().UTC()
	s.mu.Lock()
	if s.event.Status == EventStatusCompleted {
		s.mu.Unlock()
		return Event{}, errors.New("completed races cannot be started")
	}
	if s.event.Status == EventStatusActive && !s.event.StartTime.IsZero() {
		startedAt = s.event.StartTime.UTC()
	} else {
		s.event.StartTime = startedAt
	}
	s.event.Status = EventStatusActive
	s.recordStartCheckpointForRegisteredParticipantsLocked(startedAt)
	state := s.stateLocked()
	store := s.store
	updated := s.event
	s.mu.Unlock()
	persist(store, state)
	return updated, nil
}

func (s *Service) AddCheckpoint(name string, sequence int, distanceKM float64) (Checkpoint, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Checkpoint{}, errors.New("checkpoint name is required")
	}
	if sequence <= 0 {
		return Checkpoint{}, errors.New("checkpoint sequence must be greater than zero")
	}
	if distanceKM < 0 {
		return Checkpoint{}, errors.New("checkpoint distance cannot be negative")
	}

	s.mu.Lock()
	if len(s.checkpoints) > 0 && sequence <= s.firstCheckpointSequence() {
		s.mu.Unlock()
		return Checkpoint{}, errors.New("custom checkpoints must be after Start")
	}
	id := checkpointID(name)
	if id == "" {
		s.mu.Unlock()
		return Checkpoint{}, errors.New("checkpoint name must include letters or numbers")
	}
	if _, exists := s.checkpointByID[id]; exists {
		s.mu.Unlock()
		return Checkpoint{}, fmt.Errorf("%s already exists", name)
	}
	for i := range s.checkpoints {
		if s.checkpoints[i].Sequence >= sequence {
			s.checkpoints[i].Sequence++
		}
	}
	checkpoint := Checkpoint{ID: id, Name: name, Sequence: sequence, DistanceKM: distanceKM}
	s.checkpoints = append(s.checkpoints, checkpoint)
	sort.Slice(s.checkpoints, func(i, j int) bool {
		return s.checkpoints[i].Sequence < s.checkpoints[j].Sequence
	})
	s.checkpointByID[id] = checkpoint
	for _, item := range s.checkpoints {
		s.checkpointByID[item.ID] = item
	}
	state := s.stateLocked()
	store := s.store
	s.mu.Unlock()
	persist(store, state)
	return checkpoint, nil
}

func (s *Service) RegisterParticipant(name, phone, notes string) (Participant, error) {
	name = strings.TrimSpace(name)
	phone = strings.TrimSpace(phone)
	notes = strings.TrimSpace(notes)
	if name == "" || phone == "" {
		return Participant{}, ErrInvalidParticipant
	}

	s.mu.Lock()

	participant := Participant{
		ID:          fmt.Sprintf("runner-%03d", s.nextParticipant),
		BibNumber:   fmt.Sprintf("BIB-%03d", s.nextParticipant),
		Name:        name,
		PhoneNumber: phone,
		Notes:       notes,
		Status:      RaceStatusRegistered,
		CreatedAt:   time.Now().UTC(),
	}
	s.participants = append(s.participants, participant)
	s.participantByBib[participant.BibNumber] = len(s.participants) - 1
	s.nextParticipant++
	state := s.stateLocked()
	store := s.store
	s.mu.Unlock()
	persist(store, state)
	return participant, nil
}

func (s *Service) DeleteParticipant(bibNumber string) error {
	bibNumber = normalizeBib(bibNumber)
	if bibNumber == "" {
		return ErrInvalidBib
	}

	s.mu.Lock()
	participantIndex, ok := s.participantByBib[bibNumber]
	if !ok {
		s.mu.Unlock()
		return ErrInvalidBib
	}
	s.participants = append(s.participants[:participantIndex], s.participants[participantIndex+1:]...)
	s.participantByBib = make(map[string]int, len(s.participants))
	for i, participant := range s.participants {
		s.participantByBib[participant.BibNumber] = i
	}
	filteredLogs := s.logs[:0]
	for _, log := range s.logs {
		if log.Participant.BibNumber != bibNumber {
			filteredLogs = append(filteredLogs, log)
		}
	}
	s.logs = filteredLogs
	state := s.stateLocked()
	store := s.store
	s.mu.Unlock()
	persist(store, state)
	return nil
}

func (s *Service) ImportParticipants(rows []ImportParticipant) ImportResult {
	result := ImportResult{}
	s.mu.Lock()
	for i, row := range rows {
		participant, err := s.importParticipantLocked(row)
		if err != nil {
			result.Errors = append(result.Errors, ImportError{Row: i + 2, Message: err.Error()})
			continue
		}
		result.Created++
		result.Participants = append(result.Participants, participant)
	}
	state := s.stateLocked()
	store := s.store
	s.mu.Unlock()
	persist(store, state)
	return result
}

func (s *Service) RecordCheckpoint(bibNumber, checkpointID, volunteerID string, at time.Time) (CheckpointLog, error) {
	bibNumber = normalizeBib(bibNumber)
	checkpointID = strings.TrimSpace(checkpointID)
	volunteerID = strings.TrimSpace(volunteerID)
	if at.IsZero() {
		at = time.Now().UTC()
	}

	s.mu.Lock()

	participantIndex, ok := s.participantByBib[bibNumber]
	if !ok {
		s.mu.Unlock()
		return CheckpointLog{}, ErrInvalidBib
	}
	checkpoint, ok := s.checkpointByID[checkpointID]
	if !ok {
		s.mu.Unlock()
		return CheckpointLog{}, ErrInvalidCheckpoint
	}
	if err := s.validateCheckpointLocked(bibNumber, checkpoint, at); err != nil {
		s.mu.Unlock()
		return CheckpointLog{}, err
	}

	participant := s.participants[participantIndex]
	switch {
	case isFinish(checkpoint, s.checkpoints):
		participant.Status = RaceStatusFinished
	case checkpoint.Sequence == s.firstCheckpointSequence():
		participant.Status = RaceStatusStarted
	default:
		participant.Status = RaceStatusActive
	}
	s.participants[participantIndex] = participant

	log := CheckpointLog{
		ID:          fmt.Sprintf("log-%04d", s.nextLog),
		EventID:     s.event.ID,
		Participant: participant,
		Checkpoint:  checkpoint,
		Timestamp:   at.UTC(),
		VolunteerID: volunteerID,
	}
	log.DisplayLabel = fmt.Sprintf("%s reached %s", participant.BibNumber, checkpoint.Name)
	s.logs = append(s.logs, log)
	s.nextLog++
	state := s.stateLocked()
	store := s.store
	s.mu.Unlock()
	persist(store, state)
	return log, nil
}

func (s *Service) recordStartCheckpointForRegisteredParticipantsLocked(at time.Time) {
	startCheckpoint, ok := s.startCheckpointLocked()
	if !ok {
		return
	}
	startAt := at.UTC()

	recorded := make(map[string]bool, len(s.logs))
	for _, log := range s.logs {
		if log.Checkpoint.ID == startCheckpoint.ID {
			recorded[log.Participant.BibNumber] = true
		}
	}

	for i := range s.participants {
		participant := s.participants[i]
		if participant.Status == RaceStatusFinished || recorded[participant.BibNumber] {
			continue
		}
		participant.Status = RaceStatusStarted
		s.participants[i] = participant
		log := CheckpointLog{
			ID:          fmt.Sprintf("log-%04d", s.nextLog),
			EventID:     s.event.ID,
			Participant: participant,
			Checkpoint:  startCheckpoint,
			Timestamp:   startAt,
			VolunteerID: "race-start",
		}
		log.DisplayLabel = fmt.Sprintf("%s reached %s", participant.BibNumber, startCheckpoint.Name)
		s.logs = append(s.logs, log)
		s.nextLog++
	}
}

func (s *Service) importParticipantLocked(row ImportParticipant) (Participant, error) {
	name := strings.TrimSpace(row.Name)
	if name == "" {
		return Participant{}, errors.New("runner name is required")
	}
	bib := normalizeBib(row.BibNumber)
	if bib == "" {
		bib = fmt.Sprintf("BIB-%03d", s.nextParticipant)
	}
	if _, exists := s.participantByBib[bib]; exists {
		return Participant{}, fmt.Errorf("%s already exists", bib)
	}
	participant := Participant{
		ID:          fmt.Sprintf("runner-%03d", s.nextParticipant),
		BibNumber:   bib,
		Name:        name,
		PhoneNumber: strings.TrimSpace(row.PhoneNumber),
		Notes:       strings.TrimSpace(row.Notes),
		Status:      RaceStatusRegistered,
		CreatedAt:   time.Now().UTC(),
	}
	s.participants = append(s.participants, participant)
	s.participantByBib[participant.BibNumber] = len(s.participants) - 1
	if n := bibNumber(participant.BibNumber); n >= s.nextParticipant {
		s.nextParticipant = n + 1
	} else {
		s.nextParticipant++
	}
	return participant, nil
}

func (s *Service) MarkDNF(bibNumber string) error {
	bibNumber = strings.ToUpper(strings.TrimSpace(bibNumber))

	s.mu.Lock()

	idx, ok := s.participantByBib[bibNumber]
	if !ok {
		s.mu.Unlock()
		return ErrInvalidBib
	}
	participant := s.participants[idx]
	participant.Status = RaceStatusDNF
	s.participants[idx] = participant
	state := s.stateLocked()
	store := s.store
	s.mu.Unlock()
	persist(store, state)
	return nil
}

func (s *Service) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot{
		Event:        s.event,
		Summary:      s.summaryLocked(),
		Checkpoints:  append([]Checkpoint(nil), s.checkpoints...),
		Leaderboard:  s.leaderboardLocked(),
		LiveFeed:     s.recentLogsLocked(12),
		Participants: append([]Participant(nil), s.participants...),
	}
}

func (s *Service) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stateLocked()
}

func (s *Service) Summary() Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.summaryLocked()
}

func (s *Service) Leaderboard() []LeaderboardEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.leaderboardLocked()
}

func (s *Service) RecentLogs(limit int) []CheckpointLog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.recentLogsLocked(limit)
}

func (s *Service) RunnerProfile(bibNumber string) (RunnerProfile, error) {
	bibNumber = strings.ToUpper(strings.TrimSpace(bibNumber))

	s.mu.RLock()
	defer s.mu.RUnlock()

	idx, ok := s.participantByBib[bibNumber]
	if !ok {
		return RunnerProfile{}, ErrInvalidBib
	}
	participant := s.participants[idx]
	timeline := s.logsForBibLocked(bibNumber)
	entry := s.leaderboardEntryForBibLocked(bibNumber)
	return RunnerProfile{
		Participant:     participant,
		Summary:         entry,
		Timeline:        timeline,
		Segments:        segmentsFromTimeline(timeline),
		PositionHistory: s.positionHistoryLocked(bibNumber),
	}, nil
}

func (s *Service) validateCheckpointLocked(bibNumber string, checkpoint Checkpoint, at time.Time) error {
	logs := s.logsForBibLocked(bibNumber)
	if len(logs) == 0 {
		if checkpoint.Sequence != s.firstCheckpointSequence() {
			return ErrOutOfOrderEntry
		}
		return nil
	}

	last := logs[len(logs)-1]
	for _, log := range logs {
		if log.Checkpoint.ID == checkpoint.ID {
			if at.UTC().Sub(log.Timestamp.UTC()) <= s.duplicateWindow {
				return ErrDuplicateEntry
			}
			return ErrDuplicateEntry
		}
	}
	if checkpoint.Sequence != last.Checkpoint.Sequence+1 {
		return ErrOutOfOrderEntry
	}
	return nil
}

func (s *Service) summaryLocked() Summary {
	var finished, active, dnf, registered int
	var totalFinishDuration time.Duration
	var finishDurations int
	for _, participant := range s.participants {
		switch participant.Status {
		case RaceStatusFinished:
			finished++
			if duration, ok := s.raceDurationLocked(participant.BibNumber); ok {
				totalFinishDuration += duration
				finishDurations++
			}
		case RaceStatusStarted, RaceStatusActive:
			active++
		case RaceStatusDNF:
			dnf++
		default:
			registered++
		}
	}

	completionRate := 0
	if len(s.participants) > 0 {
		completionRate = int(math.Floor((float64(finished) / float64(len(s.participants))) * 100))
	}
	courseProgress := s.courseProgressLocked()

	average := "—"
	if finishDurations > 0 {
		average = formatDuration(totalFinishDuration / time.Duration(finishDurations))
	}

	return Summary{
		TotalParticipants: len(s.participants),
		Finished:          finished,
		Active:            active,
		DNF:               dnf,
		Registered:        registered,
		CompletionRate:    completionRate,
		CourseProgress:    courseProgress,
		AverageFinishTime: average,
	}
}

func (s *Service) courseProgressLocked() int {
	if len(s.participants) == 0 || len(s.checkpoints) == 0 {
		return 0
	}
	firstSequence := s.firstCheckpointSequence()
	finishSequence := s.checkpoints[len(s.checkpoints)-1].Sequence
	totalSteps := finishSequence - firstSequence + 1
	if totalSteps <= 0 {
		return 0
	}
	var progress float64
	for _, participant := range s.participants {
		logs := s.logsForBibLocked(participant.BibNumber)
		if len(logs) == 0 {
			continue
		}
		latestSequence := logs[len(logs)-1].Checkpoint.Sequence
		completedSteps := latestSequence - firstSequence + 1
		if completedSteps < 0 {
			completedSteps = 0
		}
		if completedSteps > totalSteps {
			completedSteps = totalSteps
		}
		progress += float64(completedSteps) / float64(totalSteps)
	}
	return int(math.Floor((progress / float64(len(s.participants))) * 100))
}

func (s *Service) leaderboardLocked() []LeaderboardEntry {
	entries := make([]LeaderboardEntry, 0, len(s.participants))
	for _, participant := range s.participants {
		entries = append(entries, s.leaderboardEntryLocked(participant))
	}
	sort.SliceStable(entries, func(i, j int) bool {
		left, right := entries[i], entries[j]
		if left.Status == RaceStatusDNF && right.Status != RaceStatusDNF {
			return false
		}
		if right.Status == RaceStatusDNF && left.Status != RaceStatusDNF {
			return true
		}
		if left.LatestSequence != right.LatestSequence {
			return left.LatestSequence > right.LatestSequence
		}
		leftTime := s.latestTimestampForBibLocked(left.BibNumber)
		rightTime := s.latestTimestampForBibLocked(right.BibNumber)
		if !leftTime.Equal(rightTime) {
			if leftTime.IsZero() {
				return false
			}
			if rightTime.IsZero() {
				return true
			}
			return leftTime.Before(rightTime)
		}
		return left.BibNumber < right.BibNumber
	})

	var leaderFinish time.Time
	for i := range entries {
		entries[i].Rank = i + 1
		if i == 0 {
			entries[i].Gap = "leader"
			if finish, ok := s.finishTimestampForBibLocked(entries[i].BibNumber); ok {
				leaderFinish = finish
			}
			continue
		}
		if finish, ok := s.finishTimestampForBibLocked(entries[i].BibNumber); ok && !leaderFinish.IsZero() {
			entries[i].Gap = "+" + formatDuration(finish.Sub(leaderFinish))
		} else if entries[i].LatestSequence == entries[0].LatestSequence {
			entries[i].Gap = "same checkpoint"
		} else {
			entries[i].Gap = fmt.Sprintf("-%d CP", entries[0].LatestSequence-entries[i].LatestSequence)
		}
	}
	return entries
}

func (s *Service) leaderboardEntryForBibLocked(bibNumber string) LeaderboardEntry {
	for _, entry := range s.leaderboardLocked() {
		if entry.BibNumber == bibNumber {
			return entry
		}
	}
	return LeaderboardEntry{}
}

func (s *Service) leaderboardEntryLocked(participant Participant) LeaderboardEntry {
	logs := s.logsForBibLocked(participant.BibNumber)
	latestCheckpoint := "Not started"
	latestSequence := 0
	finishTime := "—"
	raceTime := "—"
	if len(logs) > 0 {
		latest := logs[len(logs)-1]
		latestCheckpoint = latest.Checkpoint.Name
		latestSequence = latest.Checkpoint.Sequence
	}
	if finish, ok := s.finishTimestampForBibLocked(participant.BibNumber); ok {
		finishTime = finish.Format("15:04:05")
		if duration, ok := s.raceDurationLocked(participant.BibNumber); ok {
			raceTime = formatDuration(duration)
		}
	}

	return LeaderboardEntry{
		BibNumber:          participant.BibNumber,
		RunnerName:         participant.Name,
		Status:             participant.Status,
		LatestCheckpoint:   latestCheckpoint,
		LatestSequence:     latestSequence,
		FinishTime:         finishTime,
		RaceTime:           raceTime,
		PositionDeltaLabel: "—",
	}
}

func (s *Service) recentLogsLocked(limit int) []CheckpointLog {
	if limit <= 0 || limit > len(s.logs) {
		limit = len(s.logs)
	}
	recent := make([]CheckpointLog, 0, limit)
	for i := len(s.logs) - 1; i >= 0 && len(recent) < limit; i-- {
		recent = append(recent, s.logs[i])
	}
	return recent
}

func (s *Service) logsForBibLocked(bibNumber string) []CheckpointLog {
	var logs []CheckpointLog
	for _, log := range s.logs {
		if log.Participant.BibNumber == bibNumber {
			logs = append(logs, log)
		}
	}
	sort.Slice(logs, func(i, j int) bool {
		return logs[i].Checkpoint.Sequence < logs[j].Checkpoint.Sequence
	})
	return logs
}

func (s *Service) positionHistoryLocked(bibNumber string) []LeaderboardEntry {
	entry := s.leaderboardEntryForBibLocked(bibNumber)
	if entry.BibNumber == "" {
		return nil
	}
	return []LeaderboardEntry{entry}
}

func (s *Service) latestTimestampForBibLocked(bibNumber string) time.Time {
	logs := s.logsForBibLocked(bibNumber)
	if len(logs) == 0 {
		return time.Time{}
	}
	return logs[len(logs)-1].Timestamp
}

func (s *Service) finishTimestampForBibLocked(bibNumber string) (time.Time, bool) {
	for _, log := range s.logsForBibLocked(bibNumber) {
		if isFinish(log.Checkpoint, s.checkpoints) {
			return log.Timestamp, true
		}
	}
	return time.Time{}, false
}

func (s *Service) raceDurationLocked(bibNumber string) (time.Duration, bool) {
	logs := s.logsForBibLocked(bibNumber)
	if len(logs) == 0 {
		return 0, false
	}
	var start, finish time.Time
	for _, log := range logs {
		if log.Checkpoint.Sequence == s.firstCheckpointSequence() {
			start = log.Timestamp
		}
		if isFinish(log.Checkpoint, s.checkpoints) {
			finish = log.Timestamp
		}
	}
	if start.IsZero() || finish.IsZero() {
		return 0, false
	}
	return finish.Sub(start), true
}

func (s *Service) firstCheckpointSequence() int {
	checkpoint, ok := s.startCheckpointLocked()
	if !ok {
		return 1
	}
	return checkpoint.Sequence
}

func (s *Service) startCheckpointLocked() (Checkpoint, bool) {
	if checkpoint, ok := s.checkpointByID["start"]; ok {
		return checkpoint, true
	}
	if len(s.checkpoints) == 0 {
		return Checkpoint{}, false
	}
	return s.checkpoints[0], true
}

func bibNumber(bib string) int {
	parts := strings.Split(strings.TrimSpace(bib), "-")
	if len(parts) != 2 {
		return 0
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0
	}
	return n
}

func normalizeBib(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	value = strings.TrimPrefix(value, "#")
	value = strings.TrimPrefix(value, "BIB-")
	n, err := strconv.Atoi(value)
	if err != nil {
		return strings.ToUpper(strings.TrimSpace(value))
	}
	return fmt.Sprintf("BIB-%03d", n)
}

func checkpointID(name string) string {
	value := strings.ToLower(strings.TrimSpace(name))
	value = strings.ReplaceAll(value, ".", "")
	value = nonCheckpointIDChars.ReplaceAllString(value, "")
	return value
}

func logNumber(id string) int {
	parts := strings.Split(strings.TrimSpace(id), "-")
	if len(parts) != 2 {
		return 0
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0
	}
	return n
}

func (s *Service) stateLocked() State {
	return State{
		Event:        s.event,
		Checkpoints:  append([]Checkpoint(nil), s.checkpoints...),
		Participants: append([]Participant(nil), s.participants...),
		Logs:         append([]CheckpointLog(nil), s.logs...),
	}
}

func persist(store Store, state State) {
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = store.Save(ctx, state)
}

func isFinish(checkpoint Checkpoint, checkpoints []Checkpoint) bool {
	if len(checkpoints) == 0 {
		return false
	}
	return checkpoint.Sequence == checkpoints[len(checkpoints)-1].Sequence
}

func segmentsFromTimeline(timeline []CheckpointLog) []Segment {
	if len(timeline) < 2 {
		return nil
	}
	segments := make([]Segment, 0, len(timeline)-1)
	for i := 1; i < len(timeline); i++ {
		segments = append(segments, Segment{
			From:     timeline[i-1].Checkpoint.Name,
			To:       timeline[i].Checkpoint.Name,
			Duration: formatDuration(timeline[i].Timestamp.Sub(timeline[i-1].Timestamp)),
		})
	}
	return segments
}

func formatDuration(duration time.Duration) string {
	if duration < 0 {
		duration = -duration
	}
	totalSeconds := int(duration.Round(time.Second).Seconds())
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	if hours > 0 {
		return fmt.Sprintf("%dh %02dm %02ds", hours, minutes, seconds)
	}
	return fmt.Sprintf("%dm %02ds", minutes, seconds)
}
