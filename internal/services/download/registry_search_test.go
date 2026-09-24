package download

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/storage"
)

type registrySearchProvider struct {
	name    string
	results []SearchResult
	query   string
	opts    SearchOptions
}

func (p *registrySearchProvider) Name() string    { return p.name }
func (*registrySearchProvider) Match(string) bool { return false }
func (*registrySearchProvider) Download(context.Context, string, storage.Storage, DownloadOptions) (*storage.Asset, error) {
	return nil, errors.New("not used")
}
func (p *registrySearchProvider) Search(_ context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	p.query = query
	p.opts = opts
	return p.results, nil
}

type registryDownloadOnlyProvider struct{ name string }

func (p *registryDownloadOnlyProvider) Name() string    { return p.name }
func (*registryDownloadOnlyProvider) Match(string) bool { return false }
func (*registryDownloadOnlyProvider) Download(context.Context, string, storage.Storage, DownloadOptions) (*storage.Asset, error) {
	return nil, errors.New("not used")
}

func TestRegistrySearchUsesOptionalProviderCapability(t *testing.T) {
	searcher := &registrySearchProvider{name: "extractor", results: []SearchResult{{Provider: "extractor", Source: "youtube", SourceID: "abcdefghijk"}}}
	registry := NewRegistry(searcher, &registryDownloadOnlyProvider{name: "http"})
	results, err := registry.Search(context.Background(), "extractor", "query", SearchOptions{Limit: 3})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || searcher.query != "query" || searcher.opts.Limit != 3 {
		t.Fatalf("search delegation mismatch: results=%+v query=%q opts=%+v", results, searcher.query, searcher.opts)
	}
	if _, err := registry.Search(context.Background(), "http", "query", SearchOptions{}); !errors.Is(err, ErrSearchUnsupported) {
		t.Fatalf("download-only provider error = %v, want ErrSearchUnsupported", err)
	}
	if _, err := registry.Search(context.Background(), "missing", "query", SearchOptions{}); !errors.Is(err, ErrNoMatchingProvider) {
		t.Fatalf("missing provider error = %v, want ErrNoMatchingProvider", err)
	}
	if _, err := registry.Search(context.Background(), "", "query", SearchOptions{}); !errors.Is(err, core.ErrInvalidArgs) {
		t.Fatalf("empty provider error = %v, want ErrInvalidArgs", err)
	}
}
