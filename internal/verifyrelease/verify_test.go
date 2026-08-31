package verifyrelease

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openhoo/hooversion/internal/githubapi"
)

type fakeGitHub struct {
	release  *githubapi.Release
	resolved githubapi.ResolvedTag
	data     map[string][]byte
	err      error
}

func (client *fakeGitHub) LatestRelease(string) (*githubapi.Release, error) {
	return client.release, client.err
}

func (client *fakeGitHub) ReleaseByTag(string, string) (*githubapi.Release, error) {
	return client.release, client.err
}

func (client *fakeGitHub) ResolveTag(string, string) (githubapi.ResolvedTag, error) {
	return client.resolved, client.err
}

func (client *fakeGitHub) DownloadAsset(_ string, asset githubapi.Asset, destination string, maximum int64) (string, error) {
	data, ok := client.data[asset.Name]
	if !ok {
		return "", fmt.Errorf("missing fake asset %s", asset.Name)
	}
	if int64(len(data)) > maximum {
		return "", fmt.Errorf("oversized fake asset")
	}
	if err := os.WriteFile(destination, data, 0o600); err != nil {
		return "", err
	}
	return digest(data), nil
}

type recordedCommand struct {
	name string
	args []string
}

type fakeRunner struct {
	commands []recordedCommand
	err      error
}

func (runner *fakeRunner) Run(_ context.Context, name string, arguments, _ []string) ([]byte, error) {
	runner.commands = append(runner.commands, recordedCommand{name: name, args: append([]string(nil), arguments...)})
	return []byte("verified"), runner.err
}

func TestVerifyChecksEveryPayloadAndEmitsVSA(t *testing.T) {
	client := releaseFixture(map[string][]byte{"artifact.bin": []byte("artifact")})
	now := time.Date(2026, 8, 31, 12, 0, 0, 123, time.UTC)
	result, err := Verify(context.Background(), Options{
		Repository: "openhoo/demo", Tag: "v1.2.3", client: client, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TagCommit != strings.Repeat("a", 40) || len(result.Statement.Subject) != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Statement.Type != "https://in-toto.io/Statement/v1" || result.Statement.PredicateType != VSAPredicateType ||
		result.Statement.Predicate.VerificationResult != "PASSED" || result.Statement.Predicate.TimeVerified != now.Format(time.RFC3339Nano) ||
		result.Statement.Predicate.Verifier["id"] != DefaultVerifierID || result.Statement.Predicate.Extension.ChecksumSubjects != 1 {
		t.Fatalf("unexpected statement: %#v", result.Statement)
	}
	if result.Statement.Predicate.Policy.Digest["sha256"] == "" || result.Statement.Predicate.VerifiedLevels == nil {
		t.Fatalf("policy or verified levels omitted: %#v", result.Statement.Predicate)
	}
	data, err := MarshalStatement(result.Statement)
	if err != nil || !bytes.HasSuffix(data, []byte("\n")) || !bytes.Contains(data, []byte(`"https://openhoo.dev/hooversion/release-verification/v1"`)) {
		t.Fatalf("marshal data=%q err=%v", data, err)
	}
}

func TestVerifyRejectsIncompleteOrMutatedRelease(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fakeGitHub)
		want   string
	}{
		{"draft", func(client *fakeGitHub) { client.release.Draft = true }, "still a draft"},
		{"unsafe name", func(client *fakeGitHub) { client.release.Assets[0].Name = "../artifact.bin" }, "unsafe release asset"},
		{"not uploaded", func(client *fakeGitHub) { client.release.Assets[0].State = "new" }, "not uploaded"},
		{"missing checksum coverage", func(client *fakeGitHub) {
			client.data["SHA256SUMS"] = []byte(strings.Repeat("0", 64) + "  other.bin\n")
			client.release.Assets[1].Size = int64(len(client.data["SHA256SUMS"]))
		}, "does not cover"},
		{"checksum mismatch", func(client *fakeGitHub) {
			client.data["artifact.bin"] = []byte("mutated")
			client.release.Assets[0].Size = int64(len(client.data["artifact.bin"]))
		}, "checksum mismatch"},
		{"unsigned tag", func(client *fakeGitHub) {
			client.resolved.Annotated = true
			client.resolved.AllSignaturesVerified = false
		}, "lacks a verified"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := releaseFixture(map[string][]byte{"artifact.bin": []byte("artifact")})
			test.mutate(client)
			_, err := Verify(context.Background(), Options{
				Repository: "openhoo/demo", Tag: "v1.2.3", RequireSignedTag: test.name == "unsigned tag", client: client,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v, want %q", err, test.want)
			}
		})
	}
}

func TestVerifyUsesExactCosignAndGitHubAttestationPolicies(t *testing.T) {
	client := releaseFixture(map[string][]byte{
		"artifact.bin":   []byte("artifact"),
		"demo.spdx.json": []byte(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","dataLicense":"CC0-1.0","documentNamespace":"https://example.test/sbom","creationInfo":{"created":"2026-08-31T12:00:00Z","creators":["Tool: test"]},"packages":[{"SPDXID":"SPDXRef-Package"}]}`),
	})
	for _, asset := range append([]githubapi.Asset(nil), client.release.Assets...) {
		bundleName := asset.Name + ".sigstore.json"
		bundle := []byte("bundle for " + asset.Name)
		client.data[bundleName] = bundle
		client.release.Assets = append(client.release.Assets, githubapi.Asset{
			ID: int64(len(client.release.Assets) + 10), Name: bundleName, State: "uploaded", Size: int64(len(bundle)),
		})
	}
	runner := &fakeRunner{}
	result, err := Verify(context.Background(), Options{
		Repository: "openhoo/demo", Tag: "v1.2.3", client: client, runner: runner,
		RequireSBOM: true, RequireSignatures: true, SignatureIdentity: "workflow-identity", SignatureIssuer: "issuer",
		RequireAttestations: true, SignerWorkflow: "openhoo/demo/.github/workflows/release.yml", SourceRef: "refs/heads/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 6 || result.Statement.Predicate.Extension.SignaturesVerified != 3 || result.Statement.Predicate.Extension.AttestationsVerified != 3 {
		t.Fatalf("commands=%#v extension=%#v", runner.commands, result.Statement.Predicate.Extension)
	}
	for index, command := range runner.commands {
		if index < 3 {
			if command.name != "cosign" || !containsPair(command.args, "--certificate-identity", "workflow-identity") || !containsPair(command.args, "--certificate-oidc-issuer", "issuer") {
				t.Fatalf("unsafe cosign invocation: %#v", command)
			}
			continue
		}
		if command.name != "gh" || !containsPair(command.args, "--repo", "openhoo/demo") ||
			!containsPair(command.args, "--source-digest", strings.Repeat("a", 40)) ||
			!containsPair(command.args, "--source-ref", "refs/heads/main") ||
			!containsPair(command.args, "--predicate-type", "https://slsa.dev/provenance/v1") ||
			!containsValue(command.args, "--deny-self-hosted-runners") {
			t.Fatalf("unsafe gh invocation: %#v", command)
		}
	}
}

func TestVerifyRejectsNamedButInvalidSBOM(t *testing.T) {
	client := releaseFixture(map[string][]byte{
		"artifact.bin":   []byte("artifact"),
		"demo.spdx.json": []byte(`{"spdxVersion":"SPDX-2.3"}`),
	})
	if _, err := Verify(context.Background(), Options{Repository: "openhoo/demo", Tag: "v1.2.3", RequireSBOM: true, client: client}); err == nil || !strings.Contains(err.Error(), "required SPDX") {
		t.Fatalf("invalid SBOM accepted: %v", err)
	}
}

func TestVerifyRequiresLicenseInsideEveryArchive(t *testing.T) {
	client := releaseFixture(map[string][]byte{"demo.tar.gz": tarGzip(t, map[string]string{"demo/LICENSE": "Apache-2.0"})})
	result, err := Verify(context.Background(), Options{Repository: "openhoo/demo", Tag: "v1.2.3", RequireLicense: true, client: client})
	if err != nil || result.Statement.Predicate.Extension.ArchivesWithLicense != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	client = releaseFixture(map[string][]byte{"demo.tar.gz": tarGzip(t, map[string]string{"demo/README.md": "missing"})})
	if _, err := Verify(context.Background(), Options{Repository: "openhoo/demo", Tag: "v1.2.3", RequireLicense: true, client: client}); err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("missing license accepted: %v", err)
	}
}

func TestStrictVerifierOptionsFailBeforeNetwork(t *testing.T) {
	for _, options := range []Options{
		{Repository: "openhoo/demo", RequireSignatures: true},
		{Repository: "openhoo/demo", RequireAttestations: true},
	} {
		if _, err := Verify(context.Background(), options); err == nil {
			t.Fatalf("incomplete strict policy accepted: %#v", options)
		}
	}
}

func releaseFixture(payloadData map[string][]byte) *fakeGitHub {
	data := map[string][]byte{}
	var checksumLines []string
	var assets []githubapi.Asset
	id := int64(1)
	names := make([]string, 0, len(payloadData))
	for name := range payloadData {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		payload := payloadData[name]
		data[name] = payload
		checksumLines = append(checksumLines, strings.TrimPrefix(digest(payload), "sha256:")+"  "+name)
		assets = append(assets, githubapi.Asset{ID: id, Name: name, State: "uploaded", Size: int64(len(payload))})
		id++
	}
	checksums := []byte(strings.Join(checksumLines, "\n") + "\n")
	data["SHA256SUMS"] = checksums
	assets = append(assets, githubapi.Asset{ID: id, Name: "SHA256SUMS", State: "uploaded", Size: int64(len(checksums))})
	return &fakeGitHub{
		release:  &githubapi.Release{ID: 42, TagName: "v1.2.3", HTMLURL: "https://github.com/openhoo/demo/releases/tag/v1.2.3", Assets: assets},
		resolved: githubapi.ResolvedTag{CommitSHA: strings.Repeat("a", 40), Annotated: true, AllSignaturesVerified: true},
		data:     data,
	}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func containsPair(values []string, first, second string) bool {
	for index := 0; index+1 < len(values); index++ {
		if values[index] == first && values[index+1] == second {
			return true
		}
	}
	return false
}

func containsValue(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for position := index; position > 0 && values[position] < values[position-1]; position-- {
			values[position], values[position-1] = values[position-1], values[position]
		}
	}
}

func tarGzip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range files {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestSafeArchivePathRejectsTraversal(t *testing.T) {
	for _, name := range []string{"../LICENSE", "a/../../LICENSE", "/LICENSE"} {
		if err := safeArchivePath(name); err == nil {
			t.Fatalf("unsafe path accepted: %q", name)
		}
	}
	if err := safeArchivePath(filepath.ToSlash("demo/LICENSE")); err != nil {
		t.Fatal(err)
	}
}
