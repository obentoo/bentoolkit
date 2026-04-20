package provider

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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

// RepositoryRegistry fetches, caches, and parses repositories.xml
type RepositoryRegistry struct {
	CacheDir string
	CacheTTL time.Duration
	XMLPath  string
	url      string
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
		CacheDir: cacheDir,
		CacheTTL: defaultCacheTTL,
		XMLPath:  filepath.Join(cacheDir, registryXMLFile),
		url:      registryURL,
	}, nil
}

func (r *RepositoryRegistry) ensureXML() ([]byte, error) {
	info, err := os.Stat(r.XMLPath)
	if err == nil && time.Since(info.ModTime()) < r.CacheTTL {
		return os.ReadFile(r.XMLPath)
	}

	data, err := r.download()
	if err == nil {
		return data, nil
	}

	home, _ := os.UserHomeDir() //nolint:errcheck // best-effort fallback, error handled by empty check below
	if home != "" {
		fallback := filepath.Join(home, eselectCachePath)
		if fbData, fbErr := os.ReadFile(fallback); fbErr == nil {
			return fbData, nil
		}
	}

	return nil, fmt.Errorf("failed to fetch repository list: %w. Run `eselect repository list` to populate cache, or use --sync to retry", err)
}

func (r *RepositoryRegistry) download() ([]byte, error) {
	resp, err := http.Get(r.url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d fetching registry", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if err := os.WriteFile(r.XMLPath, data, 0o600); err != nil {
		return nil, fmt.Errorf("failed to write cache: %w", err)
	}

	return data, nil
}

func (r *RepositoryRegistry) Sync() error {
	_, err := r.download()
	return err
}

func (r *RepositoryRegistry) Resolve(name string) (*RepositoryInfo, error) {
	data, err := r.ensureXML()
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

func (r *RepositoryRegistry) List() ([]string, error) {
	data, err := r.ensureXML()
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
