package store

import (
	"sort"
)

const rrfK = 60.0

// ReciprocalRankFusion combines multiple lists of search results into a single ranked list.
// Score = Sum(1 / (k + rank))
func ReciprocalRankFusion(resultLists ...[]SearchResult) []SearchResult {
	// Map to aggregate scores by filepath
	type docScore struct {
		result SearchResult
		score  float64
	}
	scores := make(map[string]*docScore)

	for _, list := range resultLists {
		for rank, result := range list {
			// RRF formula: 1 / (k + rank)
			// rank is 0-indexed here, so we use rank + 1
			rrfScore := 1.0 / (rrfK + float64(rank+1))

			if existing, ok := scores[result.Filepath]; ok {
				existing.score += rrfScore
				// If the existing result doesn't have a good snippet (e.g. from vector search),
				// but this one does (e.g. from FTS), upgrade it.
				if len(existing.result.Matches) == 0 && len(result.Matches) > 0 {
					existing.result.Matches = result.Matches
					existing.result.Snippet = result.Snippet
				}
			} else {
				scores[result.Filepath] = &docScore{
					result: result,
					score:  rrfScore,
				}
			}
		}
	}

	// Convert map to slice
	var fused []SearchResult
	for _, ds := range scores {
		// Update the final score to the RRF score
		ds.result.Score = ds.score
		fused = append(fused, ds.result)
	}

	// Sort descending by score
	sort.Slice(fused, func(i, j int) bool {
		return fused[i].Score > fused[j].Score
	})

	return fused
}
