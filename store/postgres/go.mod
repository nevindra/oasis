module github.com/nevindra/oasis/store/postgres

go 1.26.1

replace github.com/nevindra/oasis => ../../

require (
	github.com/jackc/pgx/v5 v5.9.2
	github.com/nevindra/oasis v0.0.0-00010101000000-000000000000
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.19.0 // indirect
	golang.org/x/text v0.33.0 // indirect
)
