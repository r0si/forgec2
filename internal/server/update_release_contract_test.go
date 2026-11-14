package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// multiPlatformReleaseAssets mirrors what a real ForgeC2 release publishes:
// one raw server binary per platform, its checksum, its detached signature,
// an SBOM, and the one-click bundles. The hot update has to pick the right
// binary out of this list on every platform - the v2.7.0 bugs (a checksum
// belonging to another platform, a wrong architecture) were all "the list was
// realistic, the picker was not".
func multiPlatformReleaseAssets(baseURL string) []GitHubAsset {
	assets := []GitHubAsset{}
	add := func(name string) {
		assets = append(assets, GitHubAsset{Name: name, BrowserDownloadURL: baseURL + "/download/" + name})
	}
	for _, bin := range []string{
		"forgec2-server-linux-amd64",
		"forgec2-server-linux-arm64",
		"forgec2-server-windows-amd64.exe",
		"forgec2-server-windows-arm64.exe",
		"forgec2-server-darwin-amd64",
		"forgec2-server-darwin-arm64",
	} {
		add(bin)
		add(bin + ".sha256")
		add(bin + ".sha256.sig")
	}
	add("forgec2-server-linux-amd64.sbom.json")
	add("forgec2-server-v9.9.9-linux-amd64.tar.gz")
	add("forgec2-server-v9.9.9-linux-amd64.zip")
	add("forgec2-server-v9.9.9-windows-amd64.zip")
	add("QUICKSTART.txt")
	return assets
}

// TestGitHubReleaseAPIContract pins the wire contract with the releases API:
// the paths the checker calls, the headers it sends and the shape it reads.
// A GitHub change (or our own drift) that breaks this contract must fail here
// rather than in a hot update on a customer machine.
func TestGitHubReleaseAPIContract(t *testing.T) {
	type request struct {
		method string
		path   string
		accept string
		agent  string
	}
	var seen []request
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, request{
			method: r.Method,
			path:   r.URL.Path,
			accept: r.Header.Get("Accept"),
			agent:  r.Header.Get("User-Agent"),
		})
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			_ = json.NewEncoder(w).Encode(GitHubRelease{TagName: "v9.9.9", HTMLURL: "https://example.invalid/v9.9.9"})
		case strings.Contains(r.URL.Path, "/releases/tags/"):
			_ = json.NewEncoder(w).Encode(GitHubReleaseFull{
				TagName: "v9.9.9",
				HTMLURL: "https://example.invalid/v9.9.9",
				Assets:  multiPlatformReleaseAssets(srv.URL),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	previousCheck, previousAssets := updateCheckURLFmt, releaseAssetsURLFmt
	updateCheckURLFmt = srv.URL + "/repos/%s/releases/latest"
	releaseAssetsURLFmt = srv.URL + "/repos/%s/releases/tags/%s"
	defer func() {
		updateCheckURLFmt, releaseAssetsURLFmt = previousCheck, previousAssets
	}()

	version, err := fetchLatestVersion("acme/forgec2")
	if err != nil {
		t.Fatalf("fetchLatestVersion: %v", err)
	}
	if version != "v9.9.9" {
		t.Fatalf("version = %q, want v9.9.9", version)
	}
	assets, err := fetchReleaseAssets("acme/forgec2", version)
	if err != nil {
		t.Fatalf("fetchReleaseAssets: %v", err)
	}
	if len(assets) == 0 {
		t.Fatal("the release payload carried no assets")
	}
	for _, a := range assets {
		if a.Name == "" || a.BrowserDownloadURL == "" {
			t.Fatalf("asset without name or URL: %+v", a)
		}
	}

	if len(seen) < 2 {
		t.Fatalf("the checker made %d request(s), want the release and the asset list", len(seen))
	}
	if seen[0].method != http.MethodGet {
		t.Fatalf("method = %s, want GET", seen[0].method)
	}
	if seen[0].path != "/repos/acme/forgec2/releases/latest" {
		t.Fatalf("path = %q", seen[0].path)
	}
	for _, r := range seen {
		if !strings.Contains(r.accept, "application/json") {
			t.Fatalf("request %s %s did not ask for JSON (Accept=%q)", r.method, r.path, r.accept)
		}
		if !strings.HasPrefix(r.agent, "ForgeC2/") {
			t.Fatalf("request %s %s has User-Agent %q, want ForgeC2/*", r.method, r.path, r.agent)
		}
	}
	if !strings.Contains(seen[1].path, "/releases/tags/v9.9.9") {
		t.Fatalf("asset request path = %q", seen[1].path)
	}
}

// TestMultiPlatformReleaseResolvesTheRunningBinaryAndItsOwnSidecars is the
// integration test the update path lacked: a realistic multi-platform release,
// and for every supported platform the binary, its checksum and its signature
// must come from THAT platform - never from another one.
func TestMultiPlatformReleaseResolvesTheRunningBinaryAndItsOwnSidecars(t *testing.T) {
	assets := multiPlatformReleaseAssets("https://example.invalid")
	cases := []struct{ goos, goarch, want string }{
		{"linux", "amd64", "forgec2-server-linux-amd64"},
		{"linux", "arm64", "forgec2-server-linux-arm64"},
		{"windows", "amd64", "forgec2-server-windows-amd64.exe"},
		{"windows", "arm64", "forgec2-server-windows-arm64.exe"},
		{"darwin", "amd64", "forgec2-server-darwin-amd64"},
		{"darwin", "arm64", "forgec2-server-darwin-arm64"},
	}
	for _, tc := range cases {
		got, err := selectBinaryAsset(assets, tc.goos, tc.goarch)
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.goos, tc.goarch, err)
		}
		if got.Name != tc.want {
			t.Fatalf("%s/%s selected %q, want %q", tc.goos, tc.goarch, got.Name, tc.want)
		}
		// The checksum and the signature belong to that binary.
		if _, ok := findAssetByName(assets, got.Name+".sha256"); !ok {
			t.Fatalf("%s: no checksum for the selected binary", got.Name)
		}
		if _, ok := findAssetByName(assets, got.Name+".sha256.sig"); !ok {
			t.Fatalf("%s: no signature for the selected binary", got.Name)
		}
	}
	// The bundle, the SBOM and the quickstart are never installable.
	for _, name := range []string{
		"forgec2-server-v9.9.9-linux-amd64.tar.gz",
		"forgec2-server-linux-amd64.sbom.json",
		"QUICKSTART.txt",
		"forgec2-server-linux-amd64.sha256",
	} {
		if _, _, ok := matchAssetPlatform(name); ok {
			t.Fatalf("%q was accepted as a server binary", name)
		}
	}
}

// TestHotUpdateDownloadsTheSelectedAsset proves the selected asset's URL is
// the one that is fetched: a release where every platform's bytes differ
// would install the wrong build if the URL were rebuilt instead of taken from
// the asset.
func TestHotUpdateDownloadsTheSelectedAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The payload names the asset it belongs to, so a wrong pick is visible.
		_, _ = fmt.Fprint(w, "payload-for:"+filepath.Base(r.URL.Path))
	}))
	defer srv.Close()

	assets := multiPlatformReleaseAssets(srv.URL)
	s := newUpdateTestServer(t)
	canonical := canonicalBinaryName(runtime.GOOS, runtime.GOARCH)
	selected, err := selectBinaryAsset(assets, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("selectBinaryAsset: %v", err)
	}
	if selected.Name != canonical {
		t.Fatalf("selected %q, want the canonical %q", selected.Name, canonical)
	}
	dest := filepath.Join(t.TempDir(), "update.bin")
	if err := s.downloadUpdateFile(selected.BrowserDownloadURL, dest, "v9.9.9"); err != nil {
		t.Fatalf("downloadUpdateFile: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if want := "payload-for:" + selected.Name; string(got) != want {
		t.Fatalf("downloaded %q, want %q", got, want)
	}
}

// TestHotUpdateRefusesAChecksumFromAnotherPlatform is the regression test for
// the bug the release notes mention: a checksum that belonged to a different
// platform was accepted. The checksum is resolved from the selected binary's
// name, so a release that only ships another platform's checksum must be
// refused instead of installing an unverified binary.
func TestHotUpdateRefusesAChecksumFromAnotherPlatform(t *testing.T) {
	stubReleaseAssets(t, []GitHubAsset{
		{Name: canonicalBinaryName(runtime.GOOS, runtime.GOARCH)},
		{Name: canonicalBinaryName("plan9", "amd64") + ".sha256"},
	})

	s := newUpdateTestServer(t)
	err := s.performHotUpdate("v9.9.9")
	if err == nil {
		t.Fatal("a release whose checksum belongs to another platform was installed")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("error = %q, want it to name the missing checksum", err)
	}
}
