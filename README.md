# Hjem

Et værktøj til **prisanalyse af danske boliger** baseret på historiske salgsdata fra [Boliga](https://www.boliga.dk) og vurderinger fra [Dingeo](https://www.dingeo.dk).

Værktøjet er skabt for at give boligkøbere bedre indsigt i markedet — uden at skulle stole blindt på en ejendomsmæglers vurdering.

## Funktioner

- **Sammenlignelig salgsanalyse** — vægtet estimat baseret på nylige salg med lignende størrelse, rum, byggeår og afstand
- **Simpel m²-pris estimat** — områdets gennemsnitlige kvadratmeterpris ganget med boligens størrelse
- **Offentlige vurderinger** — gennemsnit af Skat, Realkredit, Geomatics AVM, Vertex AI m.fl. via Dingeo
- **Interaktive grafer** — salgspriser og kr/m² over tid med mulighed for at skifte mellem visninger
- **Sorterbar salgstabel** — alle salg i området med kr/m², afvigelse fra gennemsnit og hover-info
- **Adresse-ekskludering** — fravælg specifikke adresser i tabellen for at justere beregningerne
- **Outlier-filtrering** — IQR-baseret filtrering + automatisk fjernelse af helbygningssalg
- **Procentvis ændring** — vises på seneste salg, estimeret værdi og m²-pris år-over-år
- **Caching** — Boliga-data caches i 10 dage (PostgreSQL eller SQLite)
- **Delvist resultat** — hvis nogle gader fejler, vises det hentede data med en advarsel

## Kørsel med Docker

```shell
docker run -p 8080:8080 ghcr.io/simonottosen/hjem
```

Værktøjet er tilgængeligt på `http://localhost:8080`.

### Med PostgreSQL (anbefalet for persistens)

```shell
docker run -p 8080:8080 \
  -e POSTGRES_PASSWORD=dit_password \
  -e POSTGRES_HOST=postgres \
  -e POSTGRES_PORT=5432 \
  -e POSTGRES_DB=hjem \
  ghcr.io/simonottosen/hjem
```

Opret databasen først: `docker exec -it postgres psql -U postgres -c "CREATE DATABASE hjem;"`

Uden PostgreSQL bruges SQLite automatisk som fallback (data gemmes i `/data/hjem.db`).

### Med Dingeo-vurderinger (via FlareSolverr)

Dingeo er bag Cloudflare bot-beskyttelse. For at hente vurderinger skal [FlareSolverr](https://github.com/FlareSolverr/FlareSolverr) køre:

```shell
docker run -p 8080:8080 \
  -e FLARESOLVERR_URL=http://flaresolverr:8191 \
  ghcr.io/simonottosen/hjem
```

Uden FlareSolverr springes Dingeo-vurderinger over — resten af værktøjet fungerer stadig.

### Med SQLite og data-volume

```shell
docker run -p 8080:8080 -v $(pwd)/data:/data ghcr.io/simonottosen/hjem
```

## Fra source

```shell
cd frontend && npm install && npm run build && cd ..
cd app && go run main.go
```

Kræver Go 1.23+ og Node.js 22+.

## API-endpoints

| Endpoint | Beskrivelse |
|----------|-------------|
| `GET /` | Webapplikation |
| `POST /api/lookup` | Start et opslag (asynkront) |
| `GET /api/progress` | Poll status og resultat |
| `GET /api/health` | Sundhedstjek (JSON) |
| `GET /metrics` | Prometheus-metrikker |
| `GET /download/csv` | Download data som CSV |

## Prometheus-metrikker

`/metrics` eksponerer:
- `hjem_uptime_seconds` — oppetid
- `hjem_lookups_total` — antal opslag
- `hjem_cache_hits_total` / `hjem_cache_misses_total` — cache-statistik
- `hjem_boliga_requests_total{result="ok|fail"}` — Boliga API-kald
- `hjem_recent_errors{type="..."}` — nylige fejl efter type

En Grafana-dashboard kan importeres fra `grafana/dashboard.json`.

## Analyserne

Værktøjet anvender tre estimeringsmetoder:

1. **Sammenlignelige salg** — vægter nylige, nærliggende salg med lignende egenskaber (størrelse, rum, byggeår, afstand) højere. Bruger tidsbaseret afvigelse, gaussisk lighed og IQR-outlierfjernelse.

2. **Simpel m²-pris** — områdets gennemsnitlige kr/m² ganget med boligens størrelse. Tager ikke højde for individuelle forskelle.

3. **Offentlige vurderinger** — gennemsnit af eksterne vurderingsmodeller hentet fra Dingeo (Skat, Realkredit, AVM, Vertex AI m.fl.).

### Begrænsninger

Aspekter der **ikke** afspejles i estimaterne:
- Renovering, tilbygning eller omstrukturering
- Ændringer i områdets popularitet eller infrastruktur
- Rente- og markedsforhold
- Ejendommens stand og vedligeholdelse

## Data

- Kun **almindelige frie salg** ("Alm. Salg") medtages — familiesalg, tvangsauktioner m.v. filtreres fra
- **Helbygningssalg** fjernes automatisk (3+ lejligheder med samme pris på samme dato)
- Priser er fra nærområdet (valgt radius) — ikke begrænset af postnummer
- Som standard filtreres outliers med IQR-metoden — kan justeres eller slås fra

## Det med småt

Værktøjet indsamler kun data fra offentligt tilgængelige kilder. Af juridiske hensyn fraskriver vi os ethvert ansvar for de opslag værktøjet udfører.

Skabt af **Simon Ottosen**, baseret på det oprindelige arbejde af **Thomas Panum**.
