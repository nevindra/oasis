package rag

import "github.com/nevindra/oasis/core"

// CosineSimilarity computes the cosine similarity between two float32 vectors.
// Returns 0 if either vector is empty, mismatched in length, or has zero magnitude.
func CosineSimilarity(a, b []float32) float32 {
	return core.CosineSimilarity(a, b)
}
