# Marathon Tracker

Go-powered Marathon Tracker MVP for race organizers and checkpoint volunteers.

## What It Includes

- Live race command center dashboard.
- Fast runner registration with generated `BIB-###` numbers.
- Race setup controls for distance and start time.
- Separate marathon projects, each with isolated runners, checkpoints, logs, leaderboard, and race-entry page.
- Excel/CSV runner import with explicit column mapping for bib number, name, phone, and notes.
- Tablet-friendly checkpoint entry with server-time logging.
- Ordered checkpoint validation and duplicate prevention.
- Optional camera chest-number reader powered by a Python YOLO/OCR service.
- Live leaderboard and newest-first race feed.
- Runner profile with checkpoint timeline and segment performance.
- CSV final-results export.
- Login-first workflow with one admin and four volunteer accounts stored locally in `logincred.txt`.

## Run Locally

```powershell
go test ./...
go run ./cmd/marathon
```

Open `http://localhost:8080`.

On first run, the app creates `logincred.txt` locally:

```text
admin / admin2026
volunteer1 / volunteer12026
volunteer2 / volunteer22026
volunteer3 / volunteer32026
volunteer4 / volunteer42026
```

`logincred.txt` is intentionally gitignored. Admin users can create marathons, set race distance/start time, manage checkpoints, and add/remove volunteer logins. Volunteers can log in to add participants and record checkpoint entries for the selected race.

Set a different port with:

```powershell
$env:PORT='8090'; go run ./cmd/marathon
```

## Camera Chest Reader

The checkpoint race page can scan chest numbers with the device camera when `CHEST_READER_URL` is configured. The Go app proxies camera frames to a Python OCR service, validates the result against registered runners, then uses the existing checkpoint endpoint to record the entry.

Run the Python service locally from the separate OCR repo:

```powershell
git clone https://github.com/nawafsonu/ocr.git
cd ocr
python -m venv .venv
.\.venv\Scripts\Activate.ps1
pip install -r requirements.txt
$env:PORT='8096'
uvicorn app.main:app --host 0.0.0.0 --port 8096
```

Local Go `.env` values:

```text
CHEST_READER_URL=http://127.0.0.1:8096/read
CHEST_READER_TOKEN=
CHEST_READER_MIN_CONFIDENCE=0.82
```

You can also connect OCR at runtime from the race page. Log in as admin, enter local OCR port `8096` in the Camera chest reader block, then press `Connect OCR`. This updates the Go server in memory without editing `.env` or restarting the app.

YOLO is used to crop the person/chest area; OCR reads the text. For production accuracy, replace the generic YOLO model with a race-specific bib detector trained from labeled event photos.

## MongoDB Persistence

The app uses in-memory state when `MONGODB_URI` is not set. To persist race state to MongoDB, set the connection string in your shell or deployment environment:

```powershell
$env:MONGODB_URI='your MongoDB connection string'
go run ./cmd/marathon
```

You can also create a local `.env` file from `.env.example`:

```powershell
Copy-Item .env.example .env
```

Then fill in `MONGODB_URI`. Do not commit real database credentials. If `MONGODB_DATABASE` is omitted, the app uses the database name from the URI path, falling back to `marathon_tracker`.

## Smoke Checks

```powershell
Invoke-RestMethod http://localhost:8080/api/state
Invoke-WebRequest http://localhost:8080/reports/final.csv
```

Runner import accepts `.xlsx` or `.csv` files with a header row. In the UI, type the exact header names for the number/bib column and name column. Phone and notes columns are optional.

## Marathon Projects

Each marathon is treated as a separate project. Create a new marathon from the dashboard, then use its project URL:

```text
/events/{marathon-id}
/events/{marathon-id}/race
```

Registrations, imports, checkpoints, checkpoint logs, runner profiles, leaderboards, and CSV exports are scoped to that marathon.

## Product Notes

The current MVP supports MongoDB snapshot persistence and falls back to memory when no database is configured. MongoDB stores one `race_snapshots` document per marathon event ID, so each marathon project keeps isolated runners, checkpoints, logs, leaderboard state, and race setup across restarts.

Production deployment should harden:

- Password hashing for `logincred.txt` or replacement with a managed identity store.
- CSRF protection for browser writes.
- Audit logs for checkpoint changes.
- Rate limits for checkpoint and registration endpoints.

## Deployment Notes

This repo includes `render.yaml` for Render Blueprint deployment. It defines the Go web service named `marathon-tracker`. Deploy the Python OCR service from `https://github.com/nawafsonu/ocr` and point `CHEST_READER_URL` at its `/read` endpoint.

- Build command: `go mod download && go build -tags netgo -ldflags '-s -w' -o app ./cmd/marathon`
- Start command: `./app`
- Environment: `PORT` is supplied by Render.
- Secret environment variables: set `MONGODB_URI`, `GROQ_API_KEY`, and the same `CHEST_READER_TOKEN` on both Render services.

Recommended pre-deploy validation when the Render CLI is installed:

```powershell
render blueprints validate
```

MongoDB is now the configured persistence target for this Go MVP. Use a restricted database user and rotate credentials before public deployment if the connection string has been shared outside a secure secret manager.
