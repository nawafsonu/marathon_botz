package race

import (
	"context"
	"errors"
	"fmt"
	"math"
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

func (s *Service) RecordCheckpoint(bibNumber, checkpointID, volunteerID string, at time.Time) (CheckpointLog, error) {
	bibNumber = strings.ToUpper(strings.TrimSpace(bibNumber))
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
		AverageFinishTime: average,
	}
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
	if len(s.checkpoints) == 0 {
		return 1
	}
	return s.checkpoints[0].Sequence
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
