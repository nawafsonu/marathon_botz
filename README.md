# Marathon Tracker

Go-powered Marathon Tracker MVP for race organizers and checkpoint volunteers.

## What It Includes

- Live race command center dashboard.
- Fast runner registration with generated `BIB-###` numbers.
- Race setup controls for distance and start time.
- Excel/CSV runner import with explicit column mapping for bib number, name, phone, and notes.
- Tablet-friendly checkpoint entry with server-time logging.
- Ordered checkpoint validation and duplicate prevention.
- Live leaderboard and newest-first race feed.
- Runner profile with checkpoint timeline and segment performance.
- CSV final-results export.

## Run Locally

```powershell
go test ./...
go run ./cmd/marathon
```

Open `http://localhost:8080`.

Set a different port with:

```powershell
$env:PORT='8090'; go run ./cmd/marathon
```

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

## Product Notes

The current MVP supports MongoDB snapshot persistence and falls back to memory when no database is configured. Production deployment should add:

- Admin and volunteer authentication.
- CSRF protection for browser writes.
- Audit logs for checkpoint changes.
- Rate limits for checkpoint and registration endpoints.

## Deployment Notes

For Render, configure a Go web service:

- Build command: `go build -o marathon ./cmd/marathon`
- Start command: `./marathon`
- Environment: `PORT` is supplied by Render.
- Secret environment variables: set `MONGODB_URI`; optionally set `MONGODB_DATABASE`.

MongoDB is now the configured persistence target for this Go MVP. Use a restricted database user and rotate credentials before public deployment if the connection string has been shared outside a secure secret manager.
