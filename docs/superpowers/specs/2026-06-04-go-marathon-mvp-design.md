# Go Marathon Tracker MVP Design

## Understanding

Marathon Tracker is a race-day operations platform for organizers, volunteers, and runners. The core problem is reducing manual errors and delay during registration, checkpoint entry, live ranking, and result visibility.

The first production slice should make the race-day loop usable: register runners, record checkpoint passes quickly, calculate live standings, and inspect runner progress. The user has overridden the PRD stack with Go.

## Proposed Solution

Build a Go web application that serves both the backend and the UI:

- Go `net/http` server with server-rendered HTML templates.
- Vanilla JavaScript for live refresh, registration, and checkpoint submissions.
- In-memory repository for the first runnable MVP, with clean domain boundaries so PostgreSQL can replace it without rewriting race logic.
- Dark athletic UI based on the generated concept at `assets/marathon-tracker-ui-concept.png`.

## Architecture Impact

The system is split into:

- `internal/race`: domain model, validation, ranking, analytics, and state mutation.
- `internal/web`: HTTP handlers, JSON endpoints, template rendering, and CSV export.
- `cmd/marathon`: application entry point and seeded demo event.
- `web/templates`: server-rendered pages.
- `web/static`: CSS and browser JavaScript.

The domain layer has no HTTP dependency and is tested directly.

## Implementation Plan

1. Add Go module and domain tests for bib generation, checkpoint ordering, duplicate prevention, stats, and leaderboard ranking.
2. Implement race domain types and an in-memory service.
3. Add HTTP routes for dashboard, checkpoint entry, runner profile, state JSON, registration, checkpoint logs, and CSV export.
4. Build the premium dark UI with responsive dashboard, leaderboard, live feed, registration, checkpoint entry, empty/error/success states, and runner profile.
5. Verify with `go test`, `go test -race`, `go run`, HTTP smoke checks, and browser screenshots.

## Testing Strategy

Automated tests cover the highest-risk race logic:

- Sequential bib generation.
- Invalid bib rejection.
- Duplicate checkpoint protection.
- Future checkpoint prevention.
- Live standings and finish-time ordering.
- Event summary metrics.

Manual/browser QA covers core workflows and responsive UI.

## Security Review

MVP risk areas:

- This slice does not implement real authentication yet; admin and volunteer screens are open locally.
- Inputs are validated server-side, and templates use Go escaping.
- Checkpoint writes use server time.
- The production version should add password hashing, signed secure cookies, CSRF protection, audit logs, and rate limits before public deployment.

## Performance Review

The in-memory store is fine for local MVP validation. The domain API is shaped so a PostgreSQL repository can add indexes on event, participant bib, checkpoint sequence, and logs. The UI polls state every five seconds as required by the PRD.

## UI/UX Review

The primary UX target is speed under race-day pressure:

- Large numbers and clear hierarchy.
- One-hand/tablet checkpoint entry.
- Auto-focus bib input and Enter-to-submit.
- Instant success/error feedback.
- Live feed newest-first.
- No horizontal scrolling.

## Deployment Notes

The app can run locally with `go run ./cmd/marathon`. Render deployment will need a web service build command and, for production persistence, a PostgreSQL database URL plus a PostgreSQL repository implementation.

## Risks & Future Improvements

- Replace in-memory storage with PostgreSQL migrations/repository.
- Add real admin/volunteer authentication.
- Add QR/RFID integrations after manual entry is reliable.
- Add Excel export, certificates, notifications, and public leaderboards later.
