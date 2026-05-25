package vault

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// BenchmarkLoad measures the encrypted-vault read path: file open + header
// parse + Argon2id-free decrypt (key already derived) + secret-table
// rehydration. Baseline for the server reload path and the upgrade tool.
//
//nolint:gocognit,gocyclo // setup-heavy benchmark with secret-table population; complexity is structural
func BenchmarkLoad(b *testing.B) {
	dir := b.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // 0700 correct for dirs
		b.Fatalf("chmod: %v", err)
	}
	path := filepath.Join(dir, "bench.hush")

	key := make([]byte, 32)
	for i := range key {
		key[i] = 0xAB
	}
	vk, err := securebytes.New(key)
	if err != nil {
		b.Fatalf("securebytes.New: %v", err)
	}
	defer func() { _ = vk.Destroy() }()

	// Populate a representative vault: 16 secrets of ~64 bytes each.
	const secretCount = 16
	secrets := make([]Secret, 0, secretCount)
	for i := range secretCount {
		val := make([]byte, 64)
		for j := range val {
			val[j] = byte(i*7 + j)
		}
		sb, err := securebytes.New(val)
		if err != nil {
			b.Fatalf("securebytes.New: %v", err)
		}
		secrets = append(secrets, Secret{
			Name:        string(rune('A'+i)) + "_BENCH_KEY",
			Description: "benchmark secret",
			Value:       sb,
		})
	}
	if err := Save(b.Context(), path, vk, secrets); err != nil {
		b.Fatalf("Save: %v", err)
	}
	for _, s := range secrets {
		_ = s.Value.Destroy()
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		store, err := Load(b.Context(), path, vk)
		if err != nil {
			b.Fatalf("Load: %v", err)
		}
		_ = store.Destroy()
	}
}
