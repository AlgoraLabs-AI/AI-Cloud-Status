// Package updater checks the project's GitHub Releases for a newer published
// version and, where the platform allows it, replaces the running executable in
// place.
//
// The security posture is the whole point of this package, because an updater is
// the one component whose JOB is to download code and run it. Three rules are
// non-negotiable and enforced here rather than left to the caller:
//
//  1. TRANSPORT. Only HTTPS, and only to a fixed allow-list of GitHub hosts —
//     checked again on every redirect hop, because Go follows redirects by
//     default and a release asset URL always redirects to a storage host. A
//     redirect to anywhere else is refused rather than followed.
//  2. INTEGRITY. The download is verified against the SHA256SUMS.txt published
//     with the release, and an unverifiable download is REFUSED, never installed.
//     A checksum from the same origin as the binary is not a defence against a
//     compromised release — only signing would be — but it does close corruption,
//     truncation, and a swapped asset, and it means a partial download can never
//     be swapped in over a working install.
//  3. REVERSIBILITY. The running binary is renamed aside, never deleted, and is
//     renamed back if anything after that point fails. There is no window in
//     which the user has no executable.
package updater

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub repository releases are published to.
const Repo = "AlgoraLabs-AI/AI-Cloud-Status"

// sumsAsset is the checksum manifest the release workflow publishes alongside
// the binaries. Without it a release is not installable (rule 2).
const sumsAsset = "SHA256SUMS.txt"

// maxDownload caps a release asset so a hostile or broken server cannot fill the
// user's disk. The real binary is ~45 MiB; 300 MiB is generous headroom that
// still bounds the damage.
const maxDownload = 300 << 20

// allowedHosts are the only hosts a release asset may be fetched from, checked
// on the initial URL and on every redirect. GitHub serves the API from
// api.github.com and redirects asset downloads to objects.githubusercontent.com
// (release-assets.githubusercontent.com on newer accounts).
var allowedHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
	"raw.githubusercontent.com":            true,
}

// ErrNoChecksum means the release has no usable SHA256SUMS.txt entry for this
// platform's asset. It is a refusal, not a warning: see rule 2.
var ErrNoChecksum = errors.New("release has no published checksum for this platform's asset")

// ErrUntrustedHost means a URL (or a redirect target) pointed somewhere outside
// allowedHosts.
var ErrUntrustedHost = errors.New("refusing to fetch an update from an untrusted host")

// Release is a published release newer than the running build.
type Release struct {
	Version   string // tag, e.g. "v1.1.0"
	Notes     string // release body (markdown), may be empty
	PageURL   string // human-readable release page
	AssetName string // the asset for THIS platform
	AssetURL  string // its download URL
	Sum       string // its expected sha256, lowercase hex
}

// AssetName returns the release-asset filename this platform's build is
// published under. It must stay in lockstep with .github/workflows/release.yml —
// a mismatch here reads to the user as "no update available" forever, which is
// the quietest possible failure, so it is asserted by a test.
func AssetName() string {
	switch runtime.GOOS {
	case "windows":
		return "AI-Cloud-Status-windows-amd64.exe"
	case "darwin":
		return "AI-Cloud-Status-darwin-arm64.tar.gz"
	default:
		return "AI-Cloud-Status-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
	}
}

// SelfReplaceSupported reports whether this platform can install an update
// without the user doing anything by hand.
//
// Only Windows can today, and the reason is the shape of the artifact rather
// than the OS: the Windows release IS the executable, so installing it is one
// rename. The macOS and Linux artifacts are tar.gz archives, and an updater that
// unpacks an archive over a user's install — with no idea whether it was placed
// by hand, by Homebrew, or by a package manager — can do real damage. There the
// user is told a version exists and pointed at the release page, which is honest
// about what the app knows.
func SelfReplaceSupported() bool { return runtime.GOOS == "windows" }

// client is the HTTP client used for every request. Its CheckRedirect enforces
// the host allow-list on each hop (rule 1) — without it, a redirect is followed
// blindly and the allow-list on the initial URL means nothing.
func client() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Minute, // a whole asset download, not a single request
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return checkURL(req.URL)
		},
	}
}

// schemeRequired is the only URL scheme accepted. It is a var solely so the
// package's own tests can point it at an httptest server; production never
// reassigns it, and the allow-list it guards is tested directly.
var schemeRequired = "https"

// checkURL enforces HTTPS and the host allow-list.
func checkURL(u *url.URL) error {
	if u.Scheme != schemeRequired {
		return fmt.Errorf("%w: %s is not %s", ErrUntrustedHost, u.Scheme, schemeRequired)
	}
	if !allowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("%w: %s", ErrUntrustedHost, u.Hostname())
	}
	return nil
}

// SafePageURL parses a release page URL and returns it only when it passes the
// SAME transport rule as everything else this package fetches.
//
// It exists because a release page URL is not fetched by this package — it is
// handed to the UI, made into a hyperlink, and from there to the OS protocol
// handler (on Windows, rundll32 url.dll,FileProtocolHandler, i.e. ShellExecute
// semantics). That sink will launch ANY registered scheme, so an html_url of
// "file://attacker/share/payload.exe" in a forged API response would be one
// click from execution — on exactly the platforms where the checksum-verified
// install path is disabled and this link is the only thing offered.
//
// The rule belongs here, next to the allow-list, rather than in the UI: the app
// already learned this lesson once for config-supplied URLs (see httpLinkOnly in
// the ui package), and a remote-supplied URL deserves at least as much.
func SafePageURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || checkURL(u) != nil {
		return nil, false
	}
	return u, true
}

// apiBase is the GitHub API root. It is a var so tests can point the whole
// package at an httptest server; production never reassigns it.
var apiBase = "https://api.github.com"

// ghRelease is the subset of GitHub's release JSON this package reads.
type ghRelease struct {
	TagName    string `json:"tag_name"`
	Body       string `json:"body"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Check asks GitHub for the latest release and returns it when it is NEWER than
// current. It returns (nil, nil) when the running build is up to date — the
// common case, and not an error.
//
// A failure here is never fatal and never shown as an alert: not reaching GitHub
// says nothing about the services this app monitors, and a status monitor that
// nags about its own update check would be noise on exactly the networks where
// it is most useful.
func Check(ctx context.Context, current string) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	if err := checkURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "AI-Cloud-Status/"+current)

	resp, err := client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github releases API answered HTTP %d", resp.StatusCode)
	}

	var gr ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&gr); err != nil {
		return nil, err
	}
	if gr.Draft || gr.Prerelease {
		return nil, nil // never offer an unfinished release
	}
	if !Newer(gr.TagName, current) {
		return nil, nil
	}

	rel := &Release{Version: gr.TagName, Notes: gr.Body, PageURL: gr.HTMLURL}
	want, sums := AssetName(), ""
	for _, a := range gr.Assets {
		switch a.Name {
		case want:
			rel.AssetName, rel.AssetURL = a.Name, a.URL
		case sumsAsset:
			sums = a.URL
		}
	}
	if rel.AssetURL == "" {
		// A release with no artifact for this platform is not an update we can
		// offer, but it IS a real newer version — report it with no asset so the
		// caller can still point the user at the page.
		return rel, nil
	}
	if sums != "" {
		if sum, err := fetchSum(ctx, sums, rel.AssetName); err == nil {
			rel.Sum = sum
		}
	}
	return rel, nil
}

// fetchSum downloads SHA256SUMS.txt and returns the hex digest recorded for
// asset. The format is `sha256sum` output: "<hex>  <name>", two spaces.
func fetchSum(ctx context.Context, sumsURL, asset string) (string, error) {
	u, err := url.Parse(sumsURL)
	if err != nil {
		return "", err
	}
	if err := checkURL(u); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sumsURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum manifest answered HTTP %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		// sha256sum writes "*name" for binary mode; accept both spellings.
		if strings.TrimPrefix(fields[1], "*") == asset {
			sum := strings.ToLower(fields[0])
			if len(sum) != sha256.Size*2 {
				return "", fmt.Errorf("checksum for %s is not a sha256 digest", asset)
			}
			if _, err := hex.DecodeString(sum); err != nil {
				return "", fmt.Errorf("checksum for %s is not hex: %w", asset, err)
			}
			return sum, nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("reading the checksum manifest: %w", err)
	}
	return "", ErrNoChecksum
}

// Download fetches the release asset into dir and returns its path. The bytes
// are hashed as they are written and the file is DELETED and an error returned
// if the digest does not match rel.Sum — a mismatched download never survives
// long enough to be installed.
//
// progress, when non-nil, is called with (bytesSoFar, totalBytes); total is 0
// when the server sends no Content-Length.
func Download(ctx context.Context, rel *Release, dir string, progress func(done, total int64)) (string, error) {
	if rel == nil || rel.AssetURL == "" {
		return "", errors.New("release has no downloadable asset for this platform")
	}
	if rel.Sum == "" {
		return "", ErrNoChecksum
	}
	// The staging filename is built from AssetName, so pin it to the constant this
	// platform actually publishes rather than trusting whatever the API returned.
	// Check already assigns nothing else — this makes that a property of Download
	// itself instead of one its caller has to preserve.
	if rel.AssetName != AssetName() {
		return "", fmt.Errorf("release asset %q is not this platform's asset %q", rel.AssetName, AssetName())
	}
	u, err := url.Parse(rel.AssetURL)
	if err != nil {
		return "", err
	}
	if err := checkURL(u); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.AssetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "AI-Cloud-Status")
	resp, err := client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download answered HTTP %d", resp.StatusCode)
	}

	// Written 0700, not 0600: this file becomes the executable.
	//
	// O_EXCL, and a remove first: the staging file must be one THIS process
	// created. Opening an existing path with O_TRUNC would happily adopt a file
	// another process planted (or still holds open) in the install directory, and
	// that file is on its way to being renamed over the executable.
	dst := filepath.Join(dir, rel.AssetName+".part")
	_ = os.Remove(dst)
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	total := resp.ContentLength
	done := int64(0)
	buf := make([]byte, 128<<10)
	body := io.LimitReader(resp.Body, maxDownload+1)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			done += int64(n)
			if done > maxDownload {
				f.Close()
				os.Remove(dst)
				return "", fmt.Errorf("update is larger than the %d MiB limit", maxDownload>>20)
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				os.Remove(dst)
				return "", werr
			}
			h.Write(buf[:n])
			if progress != nil {
				progress(done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(dst)
			return "", rerr
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(dst)
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != rel.Sum {
		os.Remove(dst)
		return "", fmt.Errorf("checksum mismatch: downloaded %s, expected %s", got, rel.Sum)
	}
	return dst, nil
}

// Newer reports whether tag is a strictly higher version than current. Both may
// carry a leading "v". Anything unparseable is treated as NOT newer, so a
// malformed tag can never trigger an update.
func Newer(tag, current string) bool {
	a, aok := parseVersion(tag)
	b, bok := parseVersion(current)
	if !aok || !bok {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// parseVersion parses "v1.2.3" / "1.2" / "1.2.3-rc1" into major/minor/patch.
// A missing component is 0; any pre-release suffix is ignored for ordering, so
// "1.2.3-rc1" and "1.2.3" compare equal and neither triggers an update over the
// other. Check already refuses prereleases outright.
func parseVersion(s string) ([3]int, bool) {
	var out [3]int
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return out, false
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
