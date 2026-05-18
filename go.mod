module github.com/nevindra/oasis

go 1.26.1

replace (
	github.com/nevindra/oasis/ingest => ./ingest
	github.com/nevindra/oasis/mcp => ./mcp
	github.com/nevindra/oasis/provider/gemini => ./provider/gemini
	github.com/nevindra/oasis/provider/openaicompat => ./provider/openaicompat
)

require (
	github.com/go-shiori/go-readability v0.0.0-20251205110129-5db1dc9836f0
	github.com/google/uuid v1.6.0
	github.com/nevindra/oasis/ingest v0.0.0-00010101000000-000000000000
	github.com/nevindra/oasis/mcp v0.0.0-00010101000000-000000000000
	github.com/nevindra/oasis/provider/gemini v0.0.0-00010101000000-000000000000
	github.com/nevindra/oasis/provider/openaicompat v0.0.0-00010101000000-000000000000
	golang.org/x/text v0.33.0
)

require (
	github.com/andybalholm/cascadia v1.3.3 // indirect
	github.com/araddon/dateparse v0.0.0-20210429162001-6b43995a97de // indirect
	github.com/go-shiori/dom v0.0.0-20230515143342-73569d674e1c // indirect
	github.com/gogs/chardet v0.0.0-20211120154057-b7413eaefb8f // indirect
	github.com/ledongthuc/pdf v0.0.0-20250511090121-5959a4027728 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	golang.org/x/net v0.49.0 // indirect
)
