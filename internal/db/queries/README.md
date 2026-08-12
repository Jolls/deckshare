# Queries

`sqlc` query files (`.sql`, annotated per [sqlc's query syntax](https://docs.sqlc.dev/en/latest/reference/query-annotations.html)).
`sqlc generate` reads these plus `migrations/` (per `sqlc.yaml`) and writes generated Go into
`internal/db/`.
