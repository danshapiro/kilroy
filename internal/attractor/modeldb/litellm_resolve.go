package modeldb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type CatalogUpdatePolicy string

const (
	CatalogPinnedOnly CatalogUpdatePolicy = "pinned"
	CatalogOnRunStart CatalogUpdatePolicy = "on_run_start"
)

type ResolvedCatalog struct {
	SnapshotPath string
	Source       string
	SHA256       string
	Warning      string
}

// ResolveLiteLLMCatalog is a deprecated compatibility wrapper.
//
// Deprecated: use ResolveModelCatalog.
func ResolveLiteLLMCatalog(ctx context.Context, pinnedPath string, logsRoot string, policy CatalogUpdatePolicy, url string, timeout time.Duration) (*ResolvedCatalog, error) {
	return ResolveModelCatalog(ctx, pinnedPath, logsRoot, policy, url, timeout)
}

func fetchBytes(ctx context.Context, url string, timeout time.Duration) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return io.ReadAll(resp.Body)
}

func copyFile(dst, src string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
