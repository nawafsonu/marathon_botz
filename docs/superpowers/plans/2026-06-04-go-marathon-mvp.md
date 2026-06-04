# Go Marathon Tracker MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a runnable Go Marathon Tracker MVP covering registration, checkpoint entry, live dashboard, leaderboard, runner profile, and CSV results.

**Architecture:** A Go monolith serves HTML, JSON, static assets, and CSV exports. Race rules live in `internal/race` with tests; HTTP concerns live in `internal/web`; `cmd/marathon` wires the app with seeded demo data.

**Tech Stack:** Go 1.26, standard-library `net/http`, `html/template`, vanilla JavaScript, CSS, in-memory repository for MVP.

---

### Task 1: Race Domain

**Files:**
- Create: `go.mod`
- Create: `internal/race/race_test.go`
- Create: `internal/race/race.go`

- [ ] Write failing tests for bib generation, checkpoint validation, ranking, and stats.
- [ ] Run `go test ./internal/race` and confirm tests fail because production types do not exist.
- [ ] Implement domain types and `Service`.
- [ ] Run `go test ./internal/race` and confirm tests pass.

### Task 2: Web Layer

**Files:**
- Create: `internal/web/server.go`
- Create: `internal/web/server_test.go`

- [ ] Write failing handler tests for state JSON, checkpoint submission validation, registration, and CSV export.
- [ ] Run `go test ./internal/web` and confirm tests fail because handlers do not exist.
- [ ] Implement HTTP server routes and response helpers.
- [ ] Run `go test ./internal/web ./internal/race` and confirm tests pass.

### Task 3: UI Templates And Assets

**Files:**
- Create: `web/templates/dashboard.html`
- Create: `web/templates/runner.html`
- Create: `web/static/styles.css`
- Create: `web/static/app.js`

- [ ] Build the dashboard, checkpoint, leaderboard, live feed, registration, and runner profile UI from the concept.
- [ ] Include loading, empty, error, and success states.
- [ ] Verify no horizontal overflow on mobile/tablet layouts.

### Task 4: Application Entry

**Files:**
- Create: `cmd/marathon/main.go`
- Create: `README.md`

- [ ] Wire seeded event, checkpoints, participants, and sample logs.
- [ ] Document local run, smoke test, and deployment notes.
- [ ] Run `go test ./...`.
- [ ] Run `go test -race ./...`.
- [ ] Run `go run ./cmd/marathon`.
- [ ] Smoke test key endpoints with HTTP requests.
- [ ] Open in browser and inspect desktop and mobile layouts.
