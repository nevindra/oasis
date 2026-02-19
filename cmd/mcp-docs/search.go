// search.go implements a BM25-scored full-text search index for documentation.
//
// The index is built once at startup from embedded doc entries. Queries are
// tokenized into terms and scored using Okapi BM25 with heading boosts,
// so multi-word queries like "network multi-agent routing" correctly match
// documents containing those terms rather than requiring an exact substring.
package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
)

// BM25 tuning parameters.
const (
	bm25K1       = 1.2
	bm25B        = 0.75
	headingBoost = 2.0 // multiplier for terms found in headings
	maxResults   = 10
)

// searchIndex is a BM25-scored inverted index built from documentation.
type searchIndex struct {
	entries   []docEntry
	postings  map[string][]posting      // term -> doc postings
	headTerms map[string]map[int]bool   // term -> docIdx set (terms in headings)
	docLens   []int                     // token count per doc
	avgDL     float64                   // average document length in tokens
}

// posting records a term's frequency in a single document.
type posting struct {
	doc  int // index into entries
	freq int // how many times the term appears
}

// searchResult is a single search hit with score and context snippet.
type searchResult struct {
	entry   docEntry
	score   float64
	snippet string
}

// newSearchIndex builds an inverted index from the given doc entries.
func newSearchIndex(entries []docEntry) *searchIndex {
	idx := &searchIndex{
		entries:   entries,
		postings:  make(map[string][]posting),
		headTerms: make(map[string]map[int]bool),
		docLens:   make([]int, len(entries)),
	}

	totalLen := 0
	for i, e := range entries {
		tokens := tokenize(e.content)
		idx.docLens[i] = len(tokens)
		totalLen += len(tokens)

		// Count term frequencies.
		tf := make(map[string]int)
		for _, t := range tokens {
			tf[t]++
		}
		for term, freq := range tf {
			idx.postings[term] = append(idx.postings[term], posting{doc: i, freq: freq})
		}

		// Track terms that appear in markdown headings.
		for _, line := range strings.Split(e.content, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				for _, t := range tokenize(line) {
					if idx.headTerms[t] == nil {
						idx.headTerms[t] = make(map[int]bool)
					}
					idx.headTerms[t][i] = true
				}
			}
		}
	}

	if len(entries) > 0 {
		idx.avgDL = float64(totalLen) / float64(len(entries))
	}
	return idx
}

// search finds documents matching the query, ranked by BM25 score.
// Returns up to maxResults results.
func (idx *searchIndex) search(query string) []searchResult {
	terms := tokenize(query)
	if len(terms) == 0 {
		return nil
	}

	// Deduplicate query terms.
	seen := make(map[string]bool)
	var unique []string
	for _, t := range terms {
		if !seen[t] {
			seen[t] = true
			unique = append(unique, t)
		}
	}

	n := float64(len(idx.entries))
	scores := make(map[int]float64)

	for _, term := range unique {
		posts, ok := idx.postings[term]
		if !ok {
			continue
		}

		df := float64(len(posts))
		idf := math.Log((n-df+0.5)/(df+0.5) + 1.0)

		for _, p := range posts {
			dl := float64(idx.docLens[p.doc])
			tf := float64(p.freq)
			tfNorm := (tf * (bm25K1 + 1)) / (tf + bm25K1*(1-bm25B+bm25B*(dl/idx.avgDL)))

			score := idf * tfNorm
			if idx.headTerms[term][p.doc] {
				score *= headingBoost
			}

			scores[p.doc] += score
		}
	}

	if len(scores) == 0 {
		return nil
	}

	// Collect and sort by score descending.
	results := make([]searchResult, 0, len(scores))
	termSet := make(map[string]bool, len(unique))
	for _, t := range unique {
		termSet[t] = true
	}

	for docIdx, score := range scores {
		results = append(results, searchResult{
			entry:   idx.entries[docIdx],
			score:   score,
			snippet: extractSnippet(idx.entries[docIdx].content, termSet),
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if len(results) > maxResults {
		results = results[:maxResults]
	}
	return results
}

// extractSnippet finds the most relevant section of content for the given
// query terms. Returns the best matching window with surrounding context
// and the nearest heading above it.
func extractSnippet(content string, queryTerms map[string]bool) string {
	lines := strings.Split(content, "\n")

	// Score each line by distinct query terms it contains.
	lineScores := make([]int, len(lines))
	for i, line := range lines {
		seen := make(map[string]bool)
		for _, t := range tokenize(line) {
			if queryTerms[t] && !seen[t] {
				lineScores[i]++
				seen[t] = true
			}
		}
	}

	// Find best 5-line window by total score.
	const windowSize = 5
	bestStart, bestScore := 0, 0
	for i := 0; i < len(lines); i++ {
		score := 0
		end := min(i+windowSize, len(lines))
		for j := i; j < end; j++ {
			score += lineScores[j]
		}
		if score > bestScore {
			bestScore = score
			bestStart = i
		}
	}

	// Expand window with 1 line of context on each side.
	start := max(bestStart-1, 0)
	end := min(bestStart+windowSize+1, len(lines))
	snippet := strings.TrimSpace(strings.Join(lines[start:end], "\n"))

	// Prepend the nearest heading above for context.
	heading := ""
	for i := bestStart; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
			heading = strings.TrimSpace(lines[i])
			break
		}
	}
	if heading != "" && !strings.Contains(snippet, heading) {
		snippet = heading + "\n\n" + snippet
	}

	return snippet
}

// formatResults formats search results for MCP tool output.
func formatResults(query string, results []searchResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("No results found for %q. Try a different keyword.", query)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d matching document(s):\n", len(results))

	for _, r := range results {
		fmt.Fprintf(&b, "\n## %s (%s)\n\n%s\n\n===\n", r.entry.name, r.entry.uri, r.snippet)
	}

	return b.String()
}

// tokenize splits text into lowercase search tokens. Hyphenated words are
// indexed both as a whole ("multi-agent") and as parts ("multi", "agent").
func tokenize(text string) []string {
	lower := strings.ToLower(text)
	var tokens []string
	var buf strings.Builder

	flush := func() {
		if buf.Len() == 0 {
			return
		}
		word := strings.Trim(buf.String(), "-")
		buf.Reset()
		if len(word) < 2 {
			return
		}
		tokens = append(tokens, word)
		// Also index parts of hyphenated words.
		if strings.Contains(word, "-") {
			for _, part := range strings.Split(word, "-") {
				if len(part) >= 2 {
					tokens = append(tokens, part)
				}
			}
		}
	}

	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			buf.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()

	return tokens
}
