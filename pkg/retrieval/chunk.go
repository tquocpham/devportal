package retrieval

// Chunk is a piece of indexed content — source code or (eventually) a
// design doc — along with the location it came from.
type Chunk struct {
	FilePath  string
	RelPath   string
	Content   string
	StartLine int
	EndLine   int
}
