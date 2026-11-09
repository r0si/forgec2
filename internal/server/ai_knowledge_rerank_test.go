package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/db"
)

// seedPhraseRecallCorpus builds 200 chunks where relevance is decided by the
// query phrase, not by a term no distractor has: every distractor also holds
// both query words, so a term-presence scorer ties them with the answer and
// falls back to rowid order.
//
// Layout per query group: 12 long distractors with both words far apart, 12
// short distractors repeating one word, 3 short answers holding the phrase and
// the first word twice. Groups are interleaved so ties are not decided by
// insertion order in either direction.
func seedPhraseRecallCorpus(t *testing.T, s *Server) (uint, map[string]map[uint]bool) {
	t.Helper()
	collection := db.AIKnowledgeCollection{TenantID: 1, OwnerID: 1, Name: "phrase-recall"}
	if err := s.db.Create(&collection).Error; err != nil {
		t.Fatalf("seed collection: %v", err)
	}
	source := db.AIKnowledgeSource{TenantID: 1, CollectionID: collection.ID, Name: "phrase-recall-src"}
	if err := s.db.Create(&source).Error; err != nil {
		t.Fatalf("seed source: %v", err)
	}

	groups := []struct{ first, second string }{
		{"quota", "crisis"},
		{"credential", "rotation"},
		{"beacon", "jitter"},
	}
	goldPositions := make(map[string]map[int]bool, len(groups))
	chunks := make([]db.AIKnowledgeChunk, 0, 200)
	add := func(body string) {
		chunks = append(chunks, db.AIKnowledgeChunk{
			TenantID: 1, CollectionID: collection.ID, SourceID: source.ID,
			Position: len(chunks), Content: body, SearchTokens: s.blindAIKnowledgeTokens(body),
		})
	}

	for _, g := range groups {
		query := g.first + " " + g.second
		goldPositions[query] = make(map[int]bool)
		for i := 0; i < 12; i++ {
			filler := strings.Repeat("weekly operations review material ", 40)
			add(fmt.Sprintf("%s %s %s entry%d", g.first, filler, g.second, i))
			add(fmt.Sprintf("%s %s %s statusboard%d", g.first, g.first, g.first, i))
			if i < 3 {
				goldPositions[query][len(chunks)] = true
				add(fmt.Sprintf("incident note: the %s %s playbook, %s handling steps", g.first, g.second, g.first))
			}
		}
	}
	for len(chunks) < 200 {
		add(fmt.Sprintf("unrelated inventory entry %d with no matching words", len(chunks)))
	}

	if err := s.db.CreateInBatches(&chunks, 50).Error; err != nil {
		t.Fatalf("seed chunks: %v", err)
	}
	for _, chunk := range chunks {
		if err := s.db.Exec("INSERT INTO ai_knowledge_fts(chunk_id, tokens) VALUES(?, ?)", chunk.ID, chunk.SearchTokens).Error; err != nil {
			t.Fatalf("seed fts: %v", err)
		}
	}

	goldByQuery := make(map[string]map[uint]bool, len(goldPositions))
	for query, positions := range goldPositions {
		gold := make(map[uint]bool, len(positions))
		for position := range positions {
			gold[chunks[position].ID] = true
		}
		goldByQuery[query] = gold
	}
	return collection.ID, goldByQuery
}

// TestSearchRerankerLiftsPhraseRecall is the retrieval-quality gate: on a
// corpus where every candidate holds both query words, the chunk containing
// the phrase must be the top hit, and all three answers must be in the top 5.
//
// Measured on this corpus before the BM25 + phrase reranker: recall@5 = 2/3
// per query, first answer at rank 2, MRR 0.5 (all term-presence matches tied
// at 0.609 and rowid order decided). After: 3/3, rank 1, MRR 1.0.
func TestSearchRerankerLiftsPhraseRecall(t *testing.T) {
	s := knowledgeTestServer(t)
	collectionID, goldByQuery := seedPhraseRecallCorpus(t, s)
	principal := aiPrincipal{UserID: 1, Username: "alice", TenantID: 1, Role: db.RoleAdmin}

	for query, gold := range goldByQuery {
		results, err := s.searchAIKnowledge(principal, aiKnowledgeSearchRequest{
			Query: query, CollectionIDs: []uint{collectionID}, Limit: 5,
		})
		if err != nil {
			t.Fatalf("%s: search: %v", query, err)
		}
		if len(results) != 5 {
			t.Fatalf("%s: got %d results, want 5", query, len(results))
		}
		hits := 0
		for _, r := range results {
			if gold[r.ChunkID] {
				hits++
			}
		}
		if hits != len(gold) {
			t.Fatalf("%s: recall@5 = %d/%d; top=%v", query, hits, len(gold), topContents(results))
		}
		if !gold[results[0].ChunkID] {
			t.Fatalf("%s: top hit is not an answer: %v", query, topContents(results))
		}
		// The answer is the chunk holding the query phrase; the long fillers
		// (both words, far apart) must sort below it.
		phrase := strings.Join(strings.Fields(query), " ")
		if !strings.Contains(strings.ToLower(results[0].Content), phrase) {
			t.Fatalf("%s: top hit lacks the phrase: %.90s", query, results[0].Content)
		}
	}
}

func topContents(results []aiKnowledgeSearchResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, fmt.Sprintf("%.40s(score=%.3f)", r.Content, r.Score))
	}
	return out
}

// BenchmarkAIKnowledgeRerank measures the reranker at the search path's upper
// bound: 500 candidates of the production chunk size (1600 runes), which is the
// most the FTS/per-term candidate fetch can hand it.
//
// Measured 2026-11-09 (Windows amd64, 20 iterations): ~13 ms/op, 1.3 MB/op,
// ~100k allocs/op. The retrieval path it sits behind is ~241 ms/op on 10k
// chunks (BenchmarkAISearchKnowledgeLat), so the rerank is a small fraction of
// the search while lifting recall@5 on the phrase corpus from 2/3 to 3/3.
func BenchmarkAIKnowledgeRerank(b *testing.B) {
	filler := strings.Repeat("incident note quota handling weekly operations review material ", 22)
	results := make([]aiKnowledgeSearchResult, 500)
	gold := make(map[uint]bool)
	for i := range results {
		body := filler
		if i%50 == 0 {
			body = "incident note: the quota crisis playbook, quota handling steps " + filler[:200]
			gold[uint(i+1)] = true
		}
		results[i] = aiKnowledgeSearchResult{ChunkID: uint(i + 1), Content: body, Score: 1}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reranked := append([]aiKnowledgeSearchResult(nil), results...)
		aiKnowledgeRerank("quota crisis", reranked)
		if !gold[reranked[0].ChunkID] {
			b.Fatalf("phrase chunk not first: id=%d", reranked[0].ChunkID)
		}
	}
}

// TestSearchTokensKeepIndexContract pins the tokenizer the blind index and the
// reranker share: order and duplicates are preserved, and aiSearchTerms still
// dedupes and sorts (the stored HMAC tokens depend on it).
func TestSearchTokensKeepIndexContract(t *testing.T) {
	tokens := aiSearchTokens("Quota quota CRISIS crisis2")
	want := []string{"quota", "quota", "crisis", "crisis2"}
	if len(tokens) != len(want) {
		t.Fatalf("tokens = %v, want %v", tokens, want)
	}
	for i := range want {
		if tokens[i] != want[i] {
			t.Fatalf("tokens = %v, want %v", tokens, want)
		}
	}
	terms := aiSearchTerms("Quota quota CRISIS crisis2")
	wantTerms := []string{"crisis", "crisis2", "quota"}
	if len(terms) != len(wantTerms) {
		t.Fatalf("terms = %v, want %v", terms, wantTerms)
	}
	for i := range wantTerms {
		if terms[i] != wantTerms[i] {
			t.Fatalf("terms = %v, want %v", terms, wantTerms)
		}
	}
	// CJK is indexed as overlapping bigrams, in order.
	cjk := aiSearchTokens("共享配额")
	if len(cjk) != 3 || cjk[0] != "共享" || cjk[1] != "享配" || cjk[2] != "配额" {
		t.Fatalf("cjk tokens = %v, want 共享/享配/配额", cjk)
	}
}

// TestQueryPhrasesCoverWholeQueryAndPairs pins what the reranker treats as
// adjacency evidence: the whole query and each adjacent pair, deduplicated.
func TestQueryPhrasesCoverWholeQueryAndPairs(t *testing.T) {
	idf := map[string]float64{"quota": 2, "crisis": 4, "sharing": 1}
	phrases := aiQueryPhrases("quota crisis", idf)
	texts := make(map[string]bool)
	for _, p := range phrases {
		texts[p.text] = true
	}
	if !texts["quota crisis"] {
		t.Fatalf("whole query missing from %+v", phrases)
	}
	// The pair and the whole string are the same text here: it must appear once.
	count := 0
	for _, p := range phrases {
		if p.text == "quota crisis" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("phrase %q listed %d times", "quota crisis", count)
	}
	if len(aiQueryPhrases("quota", idf)) != 0 {
		t.Fatal("a single-term query has no adjacency evidence")
	}
}
