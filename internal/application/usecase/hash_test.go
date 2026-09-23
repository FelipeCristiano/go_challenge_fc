package usecase_test

import (
	"testing"

	"github.com/felipecristiano/desafio/internal/application/usecase"
)

func TestCanonicalPayloadHash_Determinism(t *testing.T) {
	// Dois mapas com ordens de inserção diferentes devem gerar o mesmo hash
	mapA := map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": "tx-123",
		"roundId":               "round-1",
		"gameId":                "game-1",
		"kind":                  "BET",
		"amount":                "100.00",
		"currency":              "BRL",
	}

	mapB := map[string]any{
		"currency":              "BRL",
		"amount":                "100.00",
		"kind":                  "BET",
		"gameId":                "game-1",
		"roundId":               "round-1",
		"externalTransactionId": "tx-123",
		"providerId":            "provider-a",
	}

	hashA, err := usecase.CanonicalPayloadHash(mapA)
	if err != nil {
		t.Fatalf("failed to hash mapA: %v", err)
	}

	hashB, err := usecase.CanonicalPayloadHash(mapB)
	if err != nil {
		t.Fatalf("failed to hash mapB: %v", err)
	}

	if hashA != hashB {
		t.Errorf("expected deterministic hashes, got %s != %s", hashA, hashB)
	}
}

func TestCanonicalPayloadHash_Divergence(t *testing.T) {
	mapA := map[string]any{
		"providerId": "provider-a",
		"amount":     "100.00",
	}
	mapB := map[string]any{
		"providerId": "provider-a",
		"amount":     "100.01", // valor divergente
	}

	hashA, err := usecase.CanonicalPayloadHash(mapA)
	if err != nil {
		t.Fatalf("failed to hash mapA: %v", err)
	}
	hashB, err := usecase.CanonicalPayloadHash(mapB)
	if err != nil {
		t.Fatalf("failed to hash mapB: %v", err)
	}

	if hashA == hashB {
		t.Errorf("different payloads must produce different hashes")
	}
}

func BenchmarkCanonicalPayloadHash(b *testing.B) {
	payload := map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": "tx-123",
		"roundId":               "round-1",
		"gameId":                "game-1",
		"kind":                  "BET",
		"amount":                "100.00",
		"currency":              "BRL",
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := usecase.CanonicalPayloadHash(payload)
		if err != nil {
			b.Fatal(err)
		}
	}
}
