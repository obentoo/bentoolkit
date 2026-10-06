package provider

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/httpx"
)

const (
	registryURL      = "https://api.gentoo.org/overlays/repositories.xml"
	defaultCacheTTL  = 24 * time.Hour
	eselectCachePath = ".cache/eselect-repo/repositories.xml"
	bentooCacheDir   = ".cache/bentoo"
	registryXMLFile  = "repositories.xml"
)

type xmlRepositories struct {
	XMLName xml.Name  `xml:"repositories"`
	Repos   []xmlRepo `xml:"repo"`
}

type xmlRepo struct {
	Name    string      `xml:"name"`
	Sources []xmlSource `xml:"source"`
}

type xmlSource struct {
	Type string `xml:"type,attr"`
	URI  string `xml:",chardata"`
}

func parseRepositoriesXML(data []byte) ([]xmlRepo, error) {
	var repos xmlRepositories
	if err := xml.Unmarshal(data, &repos); err != nil {
		return nil, fmt.Errorf("failed to parse repository registry: %w", err)
	}
	return repos.Repos, nil
}

func selectBestSource(repo xmlRepo) (*RepositoryInfo, error) {
	var github, gitlab, generic *RepositoryInfo

	for _, src := range repo.Sources {
		uri := strings.TrimSpace(src.URI)
		if !strings.HasPrefix(uri, "https://") {
			continue
		}

		switch {
		case strings.Contains(uri, "github.com/"):
			orgRepo := extractGitHubOrgRepo(uri)
			if orgRepo != "" {
				github = &RepositoryInfo{
					Name:     repo.Name,
					Provider: "github",
					URL:      orgRepo,
					Branch:   "master",
				}
			}
		case strings.Contains(uri, "gitlab.com/"):
			gitlab = &RepositoryInfo{
				Name:     repo.Name,
				Provider: "gitlab",
				URL:      uri,
				Branch:   "master",
			}
		case strings.HasSuffix(uri, ".git") && generic == nil:
			generic = &RepositoryInfo{
				Name:     repo.Name,
				Provider: "git",
				URL:      uri,
				Branch:   "master",
			}
		}
	}

	if github != nil {
		return github, nil
	}
	if gitlab != nil {
		return gitlab, nil
	}
	if generic != nil {
		return generic, nil
	}
	return nil, fmt.Errorf("repository '%s' has no compatible git source URL", repo.Name)
}

func extractGitHubOrgRepo(uri string) string {
	idx := strings.Index(uri, "github.com/")
	if idx < 0 {
		return ""
	}
	path := uri[idx+len("github.com/"):]
	path = strings.TrimSuffix(path, ".git")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// registryHTTPTimeout bounds one registry download: a host that never sends a
// complete response is abandoned and the eselect cache is used instead.
const registryHTTPTimeout = 30 * time.Second

// RepositoryRegistry fetches, caches, and parses repositories.xml
type RepositoryRegistry struct {
	CacheDir string
	CacheTTL time.Duration
	XMLPath  string
	url      string
	// httpClient performs the download; nil means a client with
	// registryHTTPTimeout and the tuned transport (see client).
	httpClient *http.Client
}

// newRegistryHTTPClient is the client every registry download uses unless one
// was injected.
func newRegistryHTTPClient() *http.Client {
	return &http.Client{Timeout: registryHTTPTimeout, Transport: httpx.BuildTransport()}
}

// client returns the injected HTTP client, or the default timed one when none
// was set (a registry built as a struct literal has none).
func (r *RepositoryRegistry) client() *http.Client {
	if r.httpClient != nil {
		return r.httpClient
	}
	return newRegistryHTTPClient()
}

func NewRepositoryRegistry() (*RepositoryRegistry, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to determine home directory: %w", err)
	}

	cacheDir := filepath.Join(home, bentooCacheDir)
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	return &RepositoryRegistry{
		CacheDir:   cacheDir,
		CacheTTL:   defaultCacheTTL,
		XMLPath:    filepath.Join(cacheDir, registryXMLFile),
		url:        registryURL,
		httpClient: newRegistryHTTPClient(),
	}, nil
}

// ensureXML returns the registry XML: the cache while it is younger than its
// TTL, else a fresh download, else the eselect cache. A download cut short by
// ctx is returned as the caller's cancellation and never falls back: the caller
// asked to stop, and answering from the eselect cache would carry on past it.
func (r *RepositoryRegistry) ensureXML(ctx context.Context) ([]byte, error) {
	info, err := os.Stat(r.XMLPath)
	if err == nil && time.Since(info.ModTime()) < r.CacheTTL {
		return os.ReadFile(r.XMLPath)
	}

	data, err := r.download(ctx)
	if err == nil {
		return data, nil
	}
	// Decided by the caller's context, never by the transport error: a client
	// timeout also reads as a deadline error, and it must still fall back.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("fetching registry %s: %w", r.url, ctxErr)
	}

	home, _ := os.UserHomeDir()
	if home != "" {
		fallback := filepath.Join(home, eselectCachePath)
		if fbData, fbErr := os.ReadFile(fallback); fbErr == nil { //nolint:gosec // G304: fallback is a constant path (eselectCachePath) under the user's home directory
			return fbData, nil
		}
	}

	return nil, fmt.Errorf("failed to fetch repository list: %w. Run `eselect repository list` to populate cache, or use --sync to retry", err)
}

func (r *RepositoryRegistry) download(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return nil, fmt.Errorf("building registry request %s: %w", r.url, err)
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching registry %s: %w", r.url, err)
	}
	resp.Body = http.MaxBytesReader(nil, resp.Body, httpx.MaxBodyBytes)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d fetching registry", resp.StatusCode)
	}

	// An oversized body fails here, before the write, so it never replaces
	// the cache.
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading registry %s: %w", r.url, httpx.ClassifyBodyReadError(err))
	}

	if err := os.WriteFile(r.XMLPath, data, 0o600); err != nil {
		return nil, fmt.Errorf("failed to write cache: %w", err)
	}

	return data, nil
}

// Sync forces a download of the registry, cancellable through ctx.
func (r *RepositoryRegistry) Sync(ctx context.Context) error {
	_, err := r.download(ctx)
	return err
}

// Resolve looks name up in the registry; a download it needs is cancellable
// through ctx.
func (r *RepositoryRegistry) Resolve(ctx context.Context, name string) (*RepositoryInfo, error) {
	data, err := r.ensureXML(ctx)
	if err != nil {
		return nil, err
	}

	repos, err := parseRepositoriesXML(data)
	if err != nil {
		return nil, err
	}

	for _, repo := range repos {
		if repo.Name == name {
			return selectBestSource(repo)
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrRepositoryNotFound, name)
}

// List returns every repository name in the registry, sorted; a download it
// needs is cancellable through ctx.
func (r *RepositoryRegistry) List(ctx context.Context) ([]string, error) {
	data, err := r.ensureXML(ctx)
	if err != nil {
		return nil, err
	}

	repos, err := parseRepositoriesXML(data)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(repos))
	for _, repo := range repos {
		if repo.Name != "" {
			names = append(names, repo.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}
