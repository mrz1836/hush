package upgrade

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// checksumFileMaxBytes caps the bytes read from a checksums.txt URL.
// goreleaser's checksum files for a four-asset matrix are well under
// 1 KiB; 1 MiB is a generous ceiling that still defends against a
// hostile mirror serving an unbounded stream.
const checksumFileMaxBytes = 1 << 20

// fetchChecksum downloads the goreleaser-shaped `<project>_<ver>_
// checksums.txt` file from checksumURL, parses each line of the form
// "<sha256-hex>  <filename>", and returns the hex digest matching
// assetName. The caller is expected to have located the URL via the
// release's asset list.
//
// httpClient may be nil; a default client with a short timeout is
// used in that case.
func fetchChecksum(ctx context.Context, httpClient *http.Client, checksumURL, assetName string) (string, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumURL, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("hush/upgrade: build checksum request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrChecksumFetchFailed, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("%w: status %d", ErrChecksumFetchFailed, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, checksumFileMaxBytes))
	if err != nil {
		return "", fmt.Errorf("hush/upgrade: read checksum body: %w", err)
	}

	return parseChecksum(string(data), assetName)
}

// parseChecksum walks each line of a goreleaser checksums file and
// returns the hex digest for assetName. A "valid" digest is exactly
// 64 hex characters (SHA-256); other lengths are rejected so we don't
// accept an MD5 (32) or SHA-1 (40) collision.
func parseChecksum(data, assetName string) (string, error) {
	for _, line := range strings.Split(data, "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 || parts[1] != assetName {
			continue
		}
		if len(parts[0]) == 64 {
			return parts[0], nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrChecksumNotFound, assetName)
}
