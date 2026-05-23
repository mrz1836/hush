package upgrade

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const goreleaserChecksums = `8d5e69b1c5dca9f2f7a5e9c2b8d2f3b4a6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1  hush_0.2.0_darwin_amd64.tar.gz
0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  hush_0.2.0_darwin_arm64.tar.gz
fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210  hush_0.2.0_linux_amd64.tar.gz
abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789  hush_0.2.0_linux_arm64.tar.gz
`

func TestParseChecksum_FindsMatch(t *testing.T) {
	sum, err := parseChecksum(goreleaserChecksums, "hush_0.2.0_linux_amd64.tar.gz")
	require.NoError(t, err)
	assert.Equal(t, "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210", sum)
}

func TestParseChecksum_MissingAsset(t *testing.T) {
	_, err := parseChecksum(goreleaserChecksums, "hush_0.2.0_freebsd_riscv.tar.gz")
	require.ErrorIs(t, err, ErrChecksumNotFound)
}

func TestParseChecksum_RejectsShortDigest(t *testing.T) {
	// 32-char digest would be MD5; must be rejected.
	body := "abcdef0123456789abcdef0123456789  hush_x.tar.gz\n"
	_, err := parseChecksum(body, "hush_x.tar.gz")
	require.ErrorIs(t, err, ErrChecksumNotFound)
}

func TestParseChecksum_HandlesBlankAndShortLines(t *testing.T) {
	body := "\n   \nshortline\n" + goreleaserChecksums
	sum, err := parseChecksum(body, "hush_0.2.0_darwin_arm64.tar.gz")
	require.NoError(t, err)
	assert.Equal(t, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", sum)
}

func TestFetchChecksum_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(goreleaserChecksums))
	}))
	t.Cleanup(srv.Close)

	sum, err := fetchChecksum(t.Context(), srv.Client(), srv.URL+"/checksums.txt", "hush_0.2.0_darwin_arm64.tar.gz")
	require.NoError(t, err)
	assert.Equal(t, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", sum)
}

func TestFetchChecksum_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	_, err := fetchChecksum(t.Context(), srv.Client(), srv.URL+"/checksums.txt", "irrelevant")
	require.ErrorIs(t, err, ErrChecksumFetchFailed)
}

func TestFetchChecksum_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv.Close() // ensures the next request fails to dial

	_, err := fetchChecksum(t.Context(), srv.Client(), srv.URL+"/checksums.txt", "irrelevant")
	require.ErrorIs(t, err, ErrChecksumFetchFailed)
}

func TestFetchChecksum_AssetNotInFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(goreleaserChecksums))
	}))
	t.Cleanup(srv.Close)

	_, err := fetchChecksum(t.Context(), srv.Client(), srv.URL+"/checksums.txt", "missing.tar.gz")
	require.ErrorIs(t, err, ErrChecksumNotFound)
}

func TestFetchChecksum_DefaultClient(t *testing.T) {
	// Nil client should not panic. We expect the request to fail
	// (invalid URL) rather than succeed; we just verify the default
	// branch is exercised without nil-deref.
	_, err := fetchChecksum(t.Context(), nil, "http://127.0.0.1:0/missing", "asset")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "hush/upgrade"))
}
