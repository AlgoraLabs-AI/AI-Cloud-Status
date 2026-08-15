package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewerOrdersVersions pins the comparison that decides whether a user is
// prompted at all. Anything unparseable must read as NOT newer: a malformed tag
// on the release side must never be able to push a download.
func TestNewerOrdersVersions(t *testing.T) {
	cases := []struct {
		tag, current string
		want         bool
	}{
		{"v1.0.1", "v1.0.0", true},
		{"v1.1.0", "v1.0.9", true},
		{"v2.0.0", "v1.9.9", true},
		{"v1.0.0", "1.0.0", false}, // the leading v is not part of the version
		{"v1.0.0", "v1.0.0", false},
		{"v1.0.0", "v1.0.1", false}, // never downgrade
		{"v1.0", "v1.0.0", false},
		{"v1.1", "v1.0.9", true},
		{"v1.0.0-rc1", "v1.0.0", false}, // a prerelease of the same version is not newer
		{"", "v1.0.0", false},
		{"garbage", "v1.0.0", false},
		{"v1.0.0.1", "v1.0.0", false}, // four components is not a version we understand
		{"v-1.0.0", "v1.0.0", false},
		{"v99999.0.0", "v1.0.0", true},
	}
	for _, c := range cases {
		if got := Newer(c.tag, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.tag, c.current, got, c.want)
		}
	}
}

// TestCheckURLRefusesUntrustedTargets is the transport rule. An updater that
// follows a URL anywhere is a remote-code-execution primitive, so both the
// scheme and the host are constrained.
func TestCheckURLRefusesUntrustedTargets(t *testing.T) {
	bad := []string{
		"http://github.com/x",                      // plaintext
		"https://evil.example.com/x",               // wrong host
		"https://github.com.evil.example.com/x",    // suffix-confusion host
		"https://notgithub.com/x",                  // substring host
		"ftp://github.com/x",                       // wrong scheme
		"https://objects.githubusercontent.com.x/", // suffix confusion on the CDN host
	}
	for _, raw := range bad {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if err := checkURL(u); !errors.Is(err, ErrUntrustedHost) {
			t.Errorf("checkURL(%q) = %v, want ErrUntrustedHost", raw, err)
		}
	}
	for _, raw := range []string{
		"https://api.github.com/repos/x/y/releases/latest",
		"https://objects.githubusercontent.com/blob",
		"https://GitHub.com/x", // host comparison is case-insensitive
	} {
		u, _ := url.Parse(raw)
		if err := checkURL(u); err != nil {
			t.Errorf("checkURL(%q) = %v, want nil", raw, err)
		}
	}
}

// TestAssetNameMatchesTheReleaseWorkflow guards the quietest possible failure:
// if this name drifts from what .github/workflows/release.yml uploads, Check
// finds no asset and every user is told "you are up to date" forever.
func TestAssetNameMatchesTheReleaseWorkflow(t *testing.T) {
	wf, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Skipf("release workflow not readable: %v", err)
	}
	// The Windows step names its output explicitly; the others are derived from
	// the matrix target, so assert the one that is a literal.
	if !strings.Contains(string(wf), "AI-Cloud-Status-windows-amd64.exe") {
		t.Error("release.yml no longer produces AI-Cloud-Status-windows-amd64.exe — AssetName() is now wrong")
	}
	if !strings.Contains(string(wf), sumsAsset) {
		t.Errorf("release.yml no longer publishes %s — every update would be refused for lack of a checksum", sumsAsset)
	}
}

// newTestServer stands in for the GitHub API and asset host. It returns the
// server plus the sha256 of the asset body it serves.
func newTestServer(t *testing.T, tag string, body []byte, withSums bool) (*httptest.Server, string) {
	t.Helper()
	sum := sha256.Sum256(body)
	hexsum := hex.EncodeToString(sum[:])
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		assets := fmt.Sprintf(`{"name":%q,"browser_download_url":%q}`, AssetName(), srv.URL+"/asset")
		if withSums {
			assets += fmt.Sprintf(`,{"name":%q,"browser_download_url":%q}`, sumsAsset, srv.URL+"/sums")
		}
		fmt.Fprintf(w, `{"tag_name":%q,"body":"notes","html_url":"https://github.com/x","assets":[%s]}`, tag, assets)
	})
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hexsum, AssetName())
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, hexsum
}

// withTestAPI points the package at a test server, relaxing the host allow-list
// for the duration. Production never does this — the allow-list is exercised
// directly by TestCheckURLRefusesUntrustedTargets.
func withTestAPI(t *testing.T, srv *httptest.Server) {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	oldBase, oldHosts := apiBase, allowedHosts
	apiBase = srv.URL
	allowedHosts = map[string]bool{u.Hostname(): true}
	restore := func() { apiBase, allowedHosts = oldBase, oldHosts }
	t.Cleanup(restore)
	// The test server is plain HTTP; relax the scheme check the same way.
	oldCheck := schemeRequired
	schemeRequired = u.Scheme
	t.Cleanup(func() { schemeRequired = oldCheck })
}

func TestCheckReportsANewerRelease(t *testing.T) {
	srv, hexsum := newTestServer(t, "v9.9.9", []byte("new binary"), true)
	withTestAPI(t, srv)

	rel, err := Check(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel == nil {
		t.Fatal("Check returned no release for a newer tag")
	}
	if rel.Version != "v9.9.9" || rel.AssetURL == "" {
		t.Fatalf("release = %+v", rel)
	}
	if rel.Sum != hexsum {
		t.Errorf("sum = %q, want %q", rel.Sum, hexsum)
	}
}

func TestCheckIsSilentWhenUpToDate(t *testing.T) {
	srv, _ := newTestServer(t, "v1.0.0", []byte("x"), true)
	withTestAPI(t, srv)

	rel, err := Check(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel != nil {
		t.Errorf("Check offered %v to an up-to-date build", rel.Version)
	}
}

// TestDownloadRefusesAMismatchedChecksum is the integrity rule. A tampered or
// truncated body must never reach disk as an installable file.
func TestDownloadRefusesAMismatchedChecksum(t *testing.T) {
	srv, _ := newTestServer(t, "v9.9.9", []byte("new binary"), true)
	withTestAPI(t, srv)

	rel, err := Check(context.Background(), "v1.0.0")
	if err != nil || rel == nil {
		t.Fatalf("Check: %v %v", rel, err)
	}
	rel.Sum = strings.Repeat("a", 64) // what an attacker-swapped body looks like

	dir := t.TempDir()
	if _, err := Download(context.Background(), rel, dir, nil); err == nil {
		t.Fatal("Download accepted a body whose checksum did not match")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("a rejected download was left on disk: %v", entries)
	}
}

// TestDownloadRefusesAnUnverifiableRelease: no checksum published means no
// install, rather than an install on trust.
func TestDownloadRefusesAnUnverifiableRelease(t *testing.T) {
	srv, _ := newTestServer(t, "v9.9.9", []byte("new binary"), false)
	withTestAPI(t, srv)

	rel, err := Check(context.Background(), "v1.0.0")
	if err != nil || rel == nil {
		t.Fatalf("Check: %v %v", rel, err)
	}
	if rel.Sum != "" {
		t.Fatalf("sum = %q, want empty when no manifest is published", rel.Sum)
	}
	if _, err := Download(context.Background(), rel, t.TempDir(), nil); !errors.Is(err, ErrNoChecksum) {
		t.Errorf("Download = %v, want ErrNoChecksum", err)
	}
}

func TestDownloadVerifiesAndReportsProgress(t *testing.T) {
	body := []byte(strings.Repeat("payload", 5000))
	srv, _ := newTestServer(t, "v9.9.9", body, true)
	withTestAPI(t, srv)

	rel, err := Check(context.Background(), "v1.0.0")
	if err != nil || rel == nil {
		t.Fatalf("Check: %v %v", rel, err)
	}
	var last int64
	path, err := Download(context.Background(), rel, t.TempDir(), func(done, _ int64) { last = done })
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Error("downloaded bytes differ from what was served")
	}
	if last != int64(len(body)) {
		t.Errorf("final progress = %d, want %d", last, len(body))
	}
}

// TestSwapIsReversible is the reversibility rule: after a failed install the
// user must still have a working executable exactly where it was.
func TestSwapIsReversible(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "acs-test-exe")
	if err := os.WriteFile(self, []byte("OLD"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Point SelfPath at the fixture instead of the test binary.
	oldSelf := selfPathFn
	selfPathFn = func() (string, error) { return self, nil }
	t.Cleanup(func() { selfPathFn = oldSelf })

	newExe := filepath.Join(dir, "new")
	if err := os.WriteFile(newExe, []byte("NEW"), 0o700); err != nil {
		t.Fatal(err)
	}

	backup, err := Swap(newExe, "")
	if err != nil {
		t.Fatalf("Swap: %v", err)
	}
	if got, _ := os.ReadFile(self); string(got) != "NEW" {
		t.Errorf("executable holds %q after swap, want NEW", got)
	}
	if got, _ := os.ReadFile(backup); string(got) != "OLD" {
		t.Errorf("backup holds %q, want OLD", got)
	}

	CleanupBackup()
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Errorf("CleanupBackup left %s behind", backup)
	}
}

// TestSwapRefusesBytesThatChangedAfterVerification closes the window between
// "Download checked this file" and "Swap renamed it over the executable". On a
// portable install the staging directory is writable by anything running as the
// user, so the file that was verified and the file that gets installed are not
// the same thing by construction — they have to be checked to be.
func TestSwapRefusesBytesThatChangedAfterVerification(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "acs-test-exe")
	if err := os.WriteFile(self, []byte("OLD"), 0o700); err != nil {
		t.Fatal(err)
	}
	oldSelf := selfPathFn
	selfPathFn = func() (string, error) { return self, nil }
	t.Cleanup(func() { selfPathFn = oldSelf })

	staged := filepath.Join(dir, "staged")
	if err := os.WriteFile(staged, []byte("VERIFIED"), 0o700); err != nil {
		t.Fatal(err)
	}
	verified := sha256.Sum256([]byte("VERIFIED"))
	// Someone swaps the staged file after it was verified.
	if err := os.WriteFile(staged, []byte("MALICIOUS"), 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := Swap(staged, hex.EncodeToString(verified[:])); err == nil {
		t.Fatal("Swap installed a staged file whose bytes had changed since verification")
	}
	if got, _ := os.ReadFile(self); string(got) != "OLD" {
		t.Errorf("executable holds %q, want the previous version untouched", got)
	}
}

// TestDownloadRefusesAForeignAssetName pins the staging filename to this
// platform's published asset, so the name Download builds a path from can never
// be whatever the API happened to return.
func TestDownloadRefusesAForeignAssetName(t *testing.T) {
	srv, _ := newTestServer(t, "v9.9.9", []byte("new binary"), true)
	withTestAPI(t, srv)

	rel, err := Check(context.Background(), "v1.0.0")
	if err != nil || rel == nil {
		t.Fatalf("Check: %v %v", rel, err)
	}
	rel.AssetName = "../../evil.exe"
	if _, err := Download(context.Background(), rel, t.TempDir(), nil); err == nil {
		t.Fatal("Download accepted an asset name that is not this platform's")
	}
}

// TestSafePageURLAppliesTheTransportRule: the release page URL is not fetched by
// this package, it is handed to the OS protocol handler as a clickable link — so
// a forged html_url with a file:// or custom scheme must never become one.
func TestSafePageURLAppliesTheTransportRule(t *testing.T) {
	for _, raw := range []string{
		"file://attacker/share/payload.exe",
		"ms-msdt:/id PCWDiagnostic",
		"javascript:alert(1)",
		"http://github.com/x/y/releases/tag/v1",
		"https://evil.example.com/releases",
		"",
	} {
		if u, ok := SafePageURL(raw); ok {
			t.Errorf("SafePageURL(%q) allowed %v", raw, u)
		}
	}
	if _, ok := SafePageURL("https://github.com/" + Repo + "/releases/tag/v1.0.0"); !ok {
		t.Error("SafePageURL rejected a real release page URL")
	}
}

// TestSwapRestoresOnFailure: if the replacement cannot be moved into place, the
// previous binary must come back rather than leaving the user with nothing.
func TestSwapRestoresOnFailure(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "acs-test-exe")
	if err := os.WriteFile(self, []byte("OLD"), 0o700); err != nil {
		t.Fatal(err)
	}
	oldSelf := selfPathFn
	selfPathFn = func() (string, error) { return self, nil }
	t.Cleanup(func() { selfPathFn = oldSelf })

	// A replacement that does not exist makes the second rename fail.
	if _, err := Swap(filepath.Join(dir, "does-not-exist"), ""); err == nil {
		t.Fatal("Swap succeeded with a missing replacement")
	}
	got, err := os.ReadFile(self)
	if err != nil {
		t.Fatalf("the previous executable is GONE after a failed swap: %v", err)
	}
	if string(got) != "OLD" {
		t.Errorf("executable holds %q after a failed swap, want OLD restored", got)
	}
}
