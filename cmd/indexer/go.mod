module github.com/devportal/cmd/indexer

go 1.26.1

require (
	github.com/devportal/retrieval v0.0.0
	github.com/joho/godotenv v1.5.1
)

require github.com/lib/pq v1.12.3 // indirect

replace github.com/devportal/retrieval => ../../pkg/retrieval
