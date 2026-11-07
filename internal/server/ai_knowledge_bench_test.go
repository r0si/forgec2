package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/forgec2/forgec2/internal/config"
	forgecrypto "github.com/forgec2/forgec2/internal/crypto"
	"github.com/forgec2/forgec2/internal/db"
)

// BenchmarkAISearchKnowledgeLat measures searchAIKnowledge over a corpus
// large enough that the trigram index must do the work (10k chunks). It also
// asserts the ranking contract: the chunk holding the rare query term is
// returned first.
//
// Run with:
//   go test ./internal/server/ -run XXX -bench BenchmarkAISearchKnowledgeLat -benchtime 5x -v
//
// Measured 2026-11-07 (PostgreSQL 18, local): ~157ms/op (664KB/op) for a
// two-term query against 10k chunks — ~75x faster than the previous
// fetch-all-matches then rank design (12.3s/op), with the distinctive chunk
// still ranked first by the IDF scorer. Numbers move with hardware; the
// contract assertions are the binding part.
func BenchmarkAISearchKnowledgeLat(b *testing.B) {
	gdb, err := newMigratedDBB(b)
	if err != nil {
		b.Skipf("PostgreSQL unavailable: %v", err)
	}
	cfg := config.DefaultConfig()
	// Chunk Content is encrypted at rest by the model hook; the benchmark
	// needs a non-empty loot key or every seed insert fails.
	cfg.Crypto.LootKey = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	forgecrypto.InitLootEncryption(cfg.Crypto.LootKey)
	s := &Server{db: gdb, cfg: cfg}

	collection := db.AIKnowledgeCollection{TenantID: 1, OwnerID: 1, Name: "bench"}
	if err := gdb.Create(&collection).Error; err != nil {
		b.Fatal(err)
	}
	source := db.AIKnowledgeSource{TenantID: 1, CollectionID: collection.ID, Name: "bench-source"}
	if err := gdb.Create(&source).Error; err != nil {
		b.Fatal(err)
	}

	const corpus = 10000
	chunks := make([]db.AIKnowledgeChunk, 0, corpus)
	for i := 0; i < corpus; i++ {
		content := fmt.Sprintf("routine ops filler text number %06d with common words", i)
		if i == corpus-1 {
			content = "occasional distinctive targetpayload token here"
		}
		chunks = append(chunks, db.AIKnowledgeChunk{
			TenantID: 1, CollectionID: collection.ID, SourceID: source.ID,
			Position: i, Content: content, SearchTokens: s.blindAIKnowledgeTokens(content),
		})
	}
	if err := gdb.CreateInBatches(&chunks, 500).Error; err != nil {
		b.Fatalf("seed %d chunks: %v", corpus, err)
	}

	const indexName = "idx_ai_knowledge_chunks_search_trgm"
	var idxCount int64
	if err := gdb.Raw(
		"SELECT COUNT(*) FROM pg_indexes WHERE tablename = 'ai_knowledge_chunks' AND indexname = ?",
		indexName,
	).Scan(&idxCount).Error; err != nil {
		b.Fatalf("check index: %v", err)
	}
	if idxCount != 1 {
		b.Fatalf("trigram index missing (count=%d)", idxCount)
	}

	principal := aiPrincipal{UserID: 1, Username: "ai-bench", TenantID: 1, Role: db.RoleAdmin}
	req := aiKnowledgeSearchRequest{
		Query: "common targetpayload", CollectionIDs: []uint{collection.ID}, Limit: 10,
	}

	lastName := ""
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := s.searchAIKnowledge(principal, req)
		if err != nil {
			b.Fatalf("search: %v", err)
		}
		if len(results) == 0 || !strings.Contains(results[0].Content, "targetpayload") {
			b.Fatalf("distinctive chunk not first after %d iters: %+v", i, results)
		}
		lastName = results[0].Source
	}
	_ = lastName
}
