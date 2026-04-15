# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project is

**Hjem** is a Danish property valuation tool. Users enter an address and a radius, and the app aggregates recent sales data from Boliga.dk, address data from DAWA, and public valuations from Dingeo to produce three estimates: comparable sales (comps), square-meter average, and public valuations.

## Commands

### Backend (Go)
```bash
cd /path/to/repo
go build ./...                  # Build
go test ./...                   # Run all tests
go test -run TestBoliga         # Run single test
go run app/main.go              # Start server (default :8080, SQLite hjem.db)
go run app/main.go --port 9090 --db-file custom.db
```

### Frontend (React/Vite)
```bash
cd frontend
npm install
npm run dev     # Dev server at :3000, proxies /api to localhost:8080
npm run build   # tsc + vite build → dist/app.bundle.js (CSS injected into JS)
```

### Docker (full stack)
```bash
docker build -t hjem .
docker run -p 8080:8080 hjem
```

### Environment variables
- `POSTGRES_PASSWORD` — if set, uses PostgreSQL instead of SQLite (also needs `POSTGRES_HOST`, `POSTGRES_USER`, `POSTGRES_DB`)
- `FLARESOLVERR_URL` — enables Dingeo valuation fetching (bypasses Cloudflare)

## Architecture

### Request lifecycle
1. Frontend `POST /api/lookup` (address + radius) → immediate 202 Accepted
2. Backend spawns a goroutine running `runLookup()` in `api.go`
3. Frontend polls `GET /api/progress` every ~500ms until `done: true`
4. Backend updates a `Progress` struct (in `progress.go`) as each stage completes:
   - DAWA fuzzy address lookup → nearby addresses in radius
   - Boliga sales scrape (cached 10 days in DB via GORM)
   - Comps estimation (weighted by recency, size, rooms, age, distance)
   - Square-meter aggregation by year
   - Dingeo valuations (optional, skipped if FlareSolverr unavailable)

### Backend files
- `api.go` — HTTP routes, `handleLookup`, `runLookup` orchestration
- `boliga.go` — Boliga.dk scraper with DB caching
- `dawa.go` — DAWA (Danish address API) integration
- `dingeo.go` — Dingeo + FlareSolverr valuation scraper
- `comps.go` — Gaussian-weighted comparable sales estimation
- `math.go` — IQR outlier filtering, year-over-year stats
- `models.go` — GORM domain models (Address, Sale, CachedLookup)
- `progress.go` — Async progress tracking struct
- `health.go` — `/api/health` + Prometheus `/metrics`
- `app/main.go` — Entry point: flags, DB init, server start

### Frontend files
- `src/App.tsx` — Root state: search params, progress polling, result
- `src/hooks/useSearch.ts` — POST /api/lookup
- `src/hooks/useProgress.ts` — Poll /api/progress, parse result
- `src/hooks/useFilteredData.ts` — Client-side exclusion filtering
- `src/lib/compute.ts` — Client-side comps, sqm stats, projections
- `src/lib/api.ts` — fetch() wrappers
- `src/components/ui/` — Radix UI primitives (tabs, card, button, etc.)

## Behavioral guidelines

### Think before coding
- State assumptions explicitly. If uncertain, ask before implementing.
- If multiple interpretations exist, present them — don't pick silently.
- If something is unclear, name what's confusing and ask.

### Simplicity first
- No features beyond what was asked. No speculative abstractions.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

### Surgical changes
- Don't improve adjacent code, comments, or formatting that wasn't broken.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it — don't delete it.
- Remove imports/variables/functions that *your* changes made unused, but leave pre-existing dead code alone.

### Goal-driven execution
For multi-step tasks, state a brief plan with verifiable checks before starting:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
```

## Available agents

Specialized sub-agents in `.claude/agents/` can be invoked for specific tasks:

| Agent | Use for |
|-------|---------|
| `api-designer` | REST API design, OpenAPI specs, versioning |
| `backend-developer` | Go server-side features, microservices |
| `data-analyst` | Business data insights, dashboards, reports |
| `data-engineer` | Data pipelines, ETL/ELT, data infrastructure |
| `data-scientist` | Statistical analysis, predictive models |
| `debugger` | Bug diagnosis, root cause analysis |
| `deployment-engineer` | CI/CD pipelines, deployment automation |
| `design-bridge` | Translating design specs into UI code |
| `devops-engineer` | Infrastructure automation, containerization |
| `devops-incident-responder` | Production incidents, postmortems |
| `docker-expert` | Docker images, orchestration, optimization |
| `electron-pro` | Electron desktop apps |
| `error-detective` | Error correlation, root cause across services |
| `frontend-developer` | React/Vue/Angular full frontend features |
| `fullstack-developer` | Features spanning DB + API + frontend |
| `git-workflow-manager` | Git workflows, branching strategies |
| `graphql-architect` | GraphQL schemas, federation, query optimization |
| `javascript-pro` | Modern JS/ES2023+, async patterns, Node.js |
| `microservices-architect` | Distributed system design, service decomposition |
| `ml-engineer` | ML pipelines, model serving, retraining |
| `mobile-developer` | React Native / Flutter cross-platform apps |
| `multi-agent-coordinator` | Coordinating concurrent agents, shared state |
| `penetration-tester` | Authorized security testing, vulnerability validation |
| `performance-engineer` | Bottleneck identification, optimization |
| `postgres-pro` | PostgreSQL query optimization, replication, HA |
| `prompt-engineer` | LLM prompt design and evaluation |
| `react-specialist` | React 18+ advanced patterns, performance |
| `readme-generator` | Maintainer-ready README from codebase scan |
| `security-engineer` | Security controls, compliance, threat modeling |
| `sre-engineer` | SLOs, error budgets, reliability automation |
| `test-automator` | Test frameworks, CI/CD test integration |
| `ui-designer` | Visual interfaces, design systems, components |
| `ux-researcher` | Usability testing, user research, personas |
| `websocket-engineer` | Real-time WebSocket/Socket.IO features |

### Key design decisions
- No WebSocket — async work uses HTTP polling
- CSS is injected into `app.bundle.js` (single file output); don't try to import a separate CSS file
- Dev proxy: Vite forwards `/api` and `/download` to `:8080`, so frontend and backend can run separately in development
- SQLite is the default; PostgreSQL is opt-in via env vars
- Boliga sales are cached by address (10-day TTL) to avoid re-scraping
