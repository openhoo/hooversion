// Package verifyrelease independently verifies published GitHub release
// artifacts and emits a SLSA Verification Summary Attestation (VSA).
package verifyrelease

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
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
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openhoo/hooversion/internal/githubapi"
)

const (
	DefaultVerifierID = "https://openhoo.dev/hooversion/verify-release/v1"
	DefaultPolicyURI  = "https://openhoo.dev/hooversion/release-verification/v1"
	VSAPredicateType  = "https://slsa.dev/verification_summary/v1"
	maxAssetBytes     = int64(256 << 20)
	maxTotalBytes     = int64(1 << 30)
	maxCommandOutput  = 1 << 20
)

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	digestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type githubClient interface {
	LatestRelease(string) (*githubapi.Release, error)
	ReleaseByTag(string, string) (*githubapi.Release, error)
	ResolveTag(string, string) (githubapi.ResolvedTag, error)
	DownloadAsset(string, githubapi.Asset, string, int64) (string, error)
}

type commandRunner interface {
	Run(context.Context, string, []string, []string) ([]byte, error)
}

type Options struct {
	Repository          string
	Tag                 string
	APIURL              string
	Token               string
	ChecksumsAsset      string
	RequireSBOM         bool
	RequireLicense      bool
	RequireSignedTag    bool
	RequireSignatures   bool
	SignatureIdentity   string
	SignatureIssuer     string
	RequireAttestations bool
	SignerWorkflow      string
	SourceRef           string
	VerifierID          string
	PolicyURI           string
	Now                 func() time.Time
	client              githubClient
	runner              commandRunner
}

type Subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

type ResourceDescriptor struct {
	URI    string            `json:"uri,omitempty"`
	Digest map[string]string `json:"digest,omitempty"`
}

type VerificationExtension struct {
	Repository           string `json:"repository"`
	Tag                  string `json:"tag"`
	TagCommit            string `json:"tagCommit"`
	ReleaseID            int64  `json:"releaseId"`
	ChecksumsAsset       string `json:"checksumsAsset"`
	ChecksumSubjects     int    `json:"checksumSubjects"`
	SignaturesVerified   int    `json:"signaturesVerified"`
	AttestationsVerified int    `json:"attestationsVerified"`
	SBOMsVerified        int    `json:"sbomsVerified"`
	ArchivesWithLicense  int    `json:"archivesWithLicense"`
}

type VSAPredicate struct {
	Verifier           map[string]string     `json:"verifier"`
	TimeVerified       string                `json:"timeVerified"`
	ResourceURI        string                `json:"resourceUri"`
	Policy             ResourceDescriptor    `json:"policy"`
	VerificationResult string                `json:"verificationResult"`
	VerifiedLevels     []string              `json:"verifiedLevels"`
	SLSAVersion        string                `json:"slsaVersion"`
	Extension          VerificationExtension `json:"https://openhoo.dev/hooversion/release-verification/v1"`
}

type Statement struct {
	Type          string       `json:"_type"`
	Subject       []Subject    `json:"subject"`
	PredicateType string       `json:"predicateType"`
	Predicate     VSAPredicate `json:"predicate"`
}

type Result struct {
	Repository string
	Tag        string
	TagCommit  string
	ReleaseURL string
	Statement  Statement
}

type policyDigestInput struct {
	Version             int    `json:"version"`
	ChecksumsAsset      string `json:"checksumsAsset"`
	RequireAllPayloads  bool   `json:"requireAllPayloads"`
	RequireSBOM         bool   `json:"requireSBOM"`
	RequireLicense      bool   `json:"requireLicense"`
	RequireSignedTag    bool   `json:"requireSignedTag"`
	RequireSignatures   bool   `json:"requireSignatures"`
	SignatureIdentity   string `json:"signatureIdentity,omitempty"`
	SignatureIssuer     string `json:"signatureIssuer,omitempty"`
	RequireAttestations bool   `json:"requireAttestations"`
	SignerWorkflow      string `json:"signerWorkflow,omitempty"`
	SourceRef           string `json:"sourceRef,omitempty"`
}

func Verify(ctx context.Context, options Options) (Result, error) {
	if !repositoryPattern.MatchString(options.Repository) {
		return Result{}, fmt.Errorf("repository must be owner/name, got %q", options.Repository)
	}
	if options.ChecksumsAsset == "" {
		options.ChecksumsAsset = "SHA256SUMS"
	}
	if err := safeAssetName(options.ChecksumsAsset); err != nil {
		return Result{}, err
	}
	if options.VerifierID == "" {
		options.VerifierID = DefaultVerifierID
	}
	if options.PolicyURI == "" {
		options.PolicyURI = DefaultPolicyURI
	}
	if err := absoluteURI("verifier id", options.VerifierID); err != nil {
		return Result{}, err
	}
	if err := absoluteURI("policy URI", options.PolicyURI); err != nil {
		return Result{}, err
	}
	if options.APIURL == "" {
		options.APIURL = "https://api.github.com"
	}
	if options.RequireSignatures && (options.SignatureIdentity == "" || options.SignatureIssuer == "") {
		return Result{}, errors.New("signature verification requires exact identity and issuer")
	}
	if options.RequireAttestations && options.SignerWorkflow == "" {
		return Result{}, errors.New("attestation verification requires signer workflow")
	}
	client := options.client
	if client == nil {
		github := githubapi.New(options.APIURL, options.Token)
		github.HTTP = &http.Client{Timeout: 30 * time.Second}
		client = github
	}
	runner := options.runner
	if runner == nil {
		runner = executableRunner{}
	}

	release, err := lookupRelease(client, options.Repository, options.Tag)
	if err != nil {
		return Result{}, err
	}
	if release == nil {
		return Result{}, fmt.Errorf("release %s was not found", options.Tag)
	}
	if release.Draft {
		return Result{}, fmt.Errorf("release %s is still a draft", release.TagName)
	}
	if options.Tag != "" && release.TagName != options.Tag {
		return Result{}, fmt.Errorf("release endpoint returned tag %q, expected %q", release.TagName, options.Tag)
	}
	resolved, err := client.ResolveTag(options.Repository, release.TagName)
	if err != nil {
		return Result{}, fmt.Errorf("resolve release tag %s: %w", release.TagName, err)
	}
	if options.RequireSignedTag && (!resolved.Annotated || !resolved.AllSignaturesVerified) {
		return Result{}, fmt.Errorf("release tag %s lacks a verified annotated-tag signature", release.TagName)
	}
	assets, err := indexAssets(release.Assets)
	if err != nil {
		return Result{}, err
	}
	checksumAsset, ok := assets[options.ChecksumsAsset]
	if !ok {
		return Result{}, fmt.Errorf("release is missing checksum asset %s", options.ChecksumsAsset)
	}
	if checksumAsset.Size > 1<<20 {
		return Result{}, fmt.Errorf("checksum asset %s exceeds 1 MiB", options.ChecksumsAsset)
	}

	temporary, err := os.MkdirTemp("", "hooversion-verify-release-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(temporary)
	downloaded := map[string]downloadedAsset{}
	download := func(asset githubapi.Asset) (downloadedAsset, error) {
		if found, ok := downloaded[asset.Name]; ok {
			return found, nil
		}
		path := filepath.Join(temporary, asset.Name)
		digest, err := client.DownloadAsset(options.Repository, asset, path, maxAssetBytes)
		if err != nil {
			return downloadedAsset{}, err
		}
		if asset.Digest != "" && asset.Digest != digest {
			return downloadedAsset{}, fmt.Errorf("GitHub digest for %s is %s, downloaded %s", asset.Name, asset.Digest, digest)
		}
		found := downloadedAsset{asset: asset, path: path, digest: digest}
		downloaded[asset.Name] = found
		return found, nil
	}
	checksumsFile, err := download(checksumAsset)
	if err != nil {
		return Result{}, fmt.Errorf("download checksum asset: %w", err)
	}
	checksumsData, err := os.ReadFile(checksumsFile.path)
	if err != nil {
		return Result{}, err
	}
	checksums, err := parseChecksums(checksumsData)
	if err != nil {
		return Result{}, err
	}
	payloads, err := checksumPayloads(assets, options.ChecksumsAsset, checksums)
	if err != nil {
		return Result{}, err
	}
	if err := enforceDownloadBudget(payloads, assets, options); err != nil {
		return Result{}, err
	}

	verified := make([]downloadedAsset, 0, len(payloads)+1)
	sbomCount := 0
	licenseCount := 0
	for _, name := range payloads {
		artifact, err := download(assets[name])
		if err != nil {
			return Result{}, fmt.Errorf("download %s: %w", name, err)
		}
		expected := "sha256:" + checksums[name]
		if artifact.digest != expected {
			return Result{}, fmt.Errorf("checksum mismatch for %s: expected %s, got %s", name, expected, artifact.digest)
		}
		if isSBOM(name) {
			if err := validateSBOM(artifact.path, name); err != nil {
				return Result{}, err
			}
			sbomCount++
		}
		if isArchive(name) && options.RequireLicense {
			if err := archiveContainsLicense(artifact.path, name); err != nil {
				return Result{}, err
			}
			licenseCount++
		}
		verified = append(verified, artifact)
	}
	if options.RequireSBOM && sbomCount == 0 {
		return Result{}, errors.New("release policy requires an SPDX or CycloneDX SBOM asset")
	}
	verified = append(verified, checksumsFile)
	sort.Slice(verified, func(i, j int) bool { return verified[i].asset.Name < verified[j].asset.Name })

	signatureCount := 0
	if options.RequireSignatures {
		for _, artifact := range verified {
			bundleAsset, ok := assets[artifact.asset.Name+".sigstore.json"]
			if !ok {
				return Result{}, fmt.Errorf("release is missing Sigstore bundle for %s", artifact.asset.Name)
			}
			bundle, err := download(bundleAsset)
			if err != nil {
				return Result{}, err
			}
			arguments := []string{
				"verify-blob", artifact.path, "--bundle", bundle.path,
				"--certificate-identity", options.SignatureIdentity,
				"--certificate-oidc-issuer", options.SignatureIssuer,
			}
			if _, err := runner.Run(ctx, "cosign", arguments, verificationEnvironment(options.Token)); err != nil {
				return Result{}, fmt.Errorf("verify Sigstore bundle for %s: %w", artifact.asset.Name, err)
			}
			signatureCount++
		}
	}

	attestationCount := 0
	if options.RequireAttestations {
		for _, artifact := range verified {
			arguments := []string{
				"attestation", "verify", artifact.path,
				"--repo", options.Repository,
				"--signer-workflow", options.SignerWorkflow,
				"--source-digest", resolved.CommitSHA,
				"--predicate-type", "https://slsa.dev/provenance/v1",
				"--deny-self-hosted-runners", "--format", "json",
			}
			if options.SourceRef != "" {
				arguments = append(arguments, "--source-ref", options.SourceRef)
			}
			if _, err := runner.Run(ctx, "gh", arguments, verificationEnvironment(options.Token)); err != nil {
				return Result{}, fmt.Errorf("verify GitHub attestation for %s: %w", artifact.asset.Name, err)
			}
			attestationCount++
		}
	}

	policy := policyDigestInput{
		Version: 1, ChecksumsAsset: options.ChecksumsAsset, RequireAllPayloads: true,
		RequireSBOM: options.RequireSBOM, RequireLicense: options.RequireLicense,
		RequireSignedTag: options.RequireSignedTag, RequireSignatures: options.RequireSignatures,
		SignatureIdentity: options.SignatureIdentity, SignatureIssuer: options.SignatureIssuer,
		RequireAttestations: options.RequireAttestations, SignerWorkflow: options.SignerWorkflow,
		SourceRef: options.SourceRef,
	}
	policyData, err := json.Marshal(policy)
	if err != nil {
		return Result{}, err
	}
	policyDigest := sha256.Sum256(policyData)
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	releaseURL := release.HTMLURL
	if releaseURL == "" {
		releaseURL = "https://github.com/" + options.Repository + "/releases/tag/" + release.TagName
	}
	subjects := make([]Subject, 0, len(verified))
	for _, artifact := range verified {
		subjects = append(subjects, Subject{
			Name:   artifact.asset.Name,
			Digest: map[string]string{"sha256": strings.TrimPrefix(artifact.digest, "sha256:")},
		})
	}
	statement := Statement{
		Type: "https://in-toto.io/Statement/v1", Subject: subjects, PredicateType: VSAPredicateType,
		Predicate: VSAPredicate{
			Verifier:     map[string]string{"id": options.VerifierID},
			TimeVerified: now().UTC().Format(time.RFC3339Nano), ResourceURI: releaseURL,
			Policy:             ResourceDescriptor{URI: options.PolicyURI, Digest: map[string]string{"sha256": hex.EncodeToString(policyDigest[:])}},
			VerificationResult: "PASSED", VerifiedLevels: []string{}, SLSAVersion: "1.2",
			Extension: VerificationExtension{
				Repository: options.Repository, Tag: release.TagName, TagCommit: resolved.CommitSHA,
				ReleaseID: release.ID, ChecksumsAsset: options.ChecksumsAsset,
				ChecksumSubjects: len(payloads), SignaturesVerified: signatureCount,
				AttestationsVerified: attestationCount, SBOMsVerified: sbomCount,
				ArchivesWithLicense: licenseCount,
			},
		},
	}
	return Result{
		Repository: options.Repository, Tag: release.TagName, TagCommit: resolved.CommitSHA,
		ReleaseURL: releaseURL, Statement: statement,
	}, nil
}

type downloadedAsset struct {
	asset  githubapi.Asset
	path   string
	digest string
}

func lookupRelease(client githubClient, repository, tag string) (*githubapi.Release, error) {
	if tag == "" {
		return client.LatestRelease(repository)
	}
	return client.ReleaseByTag(repository, tag)
}

func indexAssets(list []githubapi.Asset) (map[string]githubapi.Asset, error) {
	assets := make(map[string]githubapi.Asset, len(list))
	for _, asset := range list {
		if err := safeAssetName(asset.Name); err != nil {
			return nil, err
		}
		if asset.State != "uploaded" {
			return nil, fmt.Errorf("release asset %s is not uploaded", asset.Name)
		}
		if asset.ID <= 0 || asset.Size < 0 || asset.Size > maxAssetBytes {
			return nil, fmt.Errorf("release asset %s has invalid id or size", asset.Name)
		}
		if _, exists := assets[asset.Name]; exists {
			return nil, fmt.Errorf("duplicate release asset %s", asset.Name)
		}
		assets[asset.Name] = asset
	}
	return assets, nil
}

func absoluteURI(name, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%s must be an absolute URI", name)
	}
	return nil
}

func safeAssetName(name string) error {
	if name == "" || name == "." || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\\\x00\r\n") {
		return fmt.Errorf("unsafe release asset name %q", name)
	}
	return nil
}

func parseChecksums(data []byte) (map[string]string, error) {
	if len(data) == 0 || len(data) > 1<<20 {
		return nil, errors.New("checksum asset must be between 1 byte and 1 MiB")
	}
	result := map[string]string{}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for index, line := range lines {
		if line == "" && index == len(lines)-1 {
			continue
		}
		if len(line) < 67 || !digestPattern.MatchString(line[:64]) || line[64] != ' ' || (line[65] != ' ' && line[65] != '*') {
			return nil, fmt.Errorf("invalid SHA256SUMS line %d", index+1)
		}
		name := line[66:]
		if err := safeAssetName(name); err != nil {
			return nil, fmt.Errorf("invalid SHA256SUMS line %d: %w", index+1, err)
		}
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("duplicate checksum entry %s", name)
		}
		result[name] = line[:64]
	}
	if len(result) == 0 {
		return nil, errors.New("checksum asset has no entries")
	}
	return result, nil
}

func checksumPayloads(assets map[string]githubapi.Asset, checksumName string, checksums map[string]string) ([]string, error) {
	var payloads []string
	for name := range assets {
		if name == checksumName || strings.HasSuffix(name, ".sigstore.json") {
			continue
		}
		payloads = append(payloads, name)
	}
	sort.Strings(payloads)
	if len(payloads) == 0 {
		return nil, errors.New("release has no payload assets")
	}
	for _, name := range payloads {
		if _, ok := checksums[name]; !ok {
			return nil, fmt.Errorf("checksum asset does not cover release payload %s", name)
		}
	}
	for name := range checksums {
		if name == checksumName {
			return nil, errors.New("checksum asset cannot checksum itself")
		}
		if _, ok := assets[name]; !ok {
			return nil, fmt.Errorf("checksum entry %s has no release asset", name)
		}
		if strings.HasSuffix(name, ".sigstore.json") {
			return nil, fmt.Errorf("checksum entry %s is verification material, not a payload", name)
		}
	}
	if len(checksums) != len(payloads) {
		return nil, errors.New("checksum entries and release payload set differ")
	}
	return payloads, nil
}

func enforceDownloadBudget(payloads []string, assets map[string]githubapi.Asset, options Options) error {
	total := assets[options.ChecksumsAsset].Size
	for _, name := range payloads {
		total += assets[name].Size
		if options.RequireSignatures {
			bundle, ok := assets[name+".sigstore.json"]
			if ok {
				total += bundle.Size
			}
		}
	}
	if options.RequireSignatures {
		if bundle, ok := assets[options.ChecksumsAsset+".sigstore.json"]; ok {
			total += bundle.Size
		}
	}
	if total > maxTotalBytes {
		return fmt.Errorf("release verification download budget exceeds 1 GiB: %d bytes", total)
	}
	return nil
}

func isSBOM(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".spdx.json") || strings.HasSuffix(lower, ".cdx.json") || strings.HasSuffix(lower, ".cyclonedx.json")
}

func validateSBOM(path, name string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 64<<20 {
		return fmt.Errorf("SBOM %s must be a non-empty regular file no larger than 64 MiB", name)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("parse SBOM %s: %w", name, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("SBOM %s must contain exactly one JSON value", name)
	}
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".spdx.json") {
		return validateSPDX(document, name)
	}
	return validateCycloneDX(document, name)
}

func validateSPDX(document map[string]any, name string) error {
	version, _ := document["spdxVersion"].(string)
	identifier, _ := document["SPDXID"].(string)
	license, _ := document["dataLicense"].(string)
	namespace, _ := document["documentNamespace"].(string)
	creation, creationOK := document["creationInfo"].(map[string]any)
	created, _ := creation["created"].(string)
	creators, creatorsOK := creation["creators"].([]any)
	packages, packagesOK := document["packages"].([]any)
	files, filesOK := document["files"].([]any)
	if (version != "SPDX-2.2" && version != "SPDX-2.3") || identifier == "" || license == "" || namespace == "" ||
		!creationOK || !creatorsOK || len(creators) == 0 || (!packagesOK && !filesOK) || len(packages)+len(files) == 0 {
		return fmt.Errorf("SBOM %s lacks required SPDX 2.2/2.3 document fields", name)
	}
	if _, err := time.Parse(time.RFC3339, created); err != nil {
		return fmt.Errorf("SBOM %s has invalid SPDX creation time", name)
	}
	return nil
}

func validateCycloneDX(document map[string]any, name string) error {
	format, _ := document["bomFormat"].(string)
	version, _ := document["specVersion"].(string)
	metadata, metadataOK := document["metadata"].(map[string]any)
	_, componentsOK := document["components"].([]any)
	if format != "CycloneDX" || !strings.HasPrefix(version, "1.") || !metadataOK || !componentsOK {
		return fmt.Errorf("SBOM %s lacks required CycloneDX 1.x document fields", name)
	}
	if timestamp, ok := metadata["timestamp"].(string); ok && timestamp != "" {
		if _, err := time.Parse(time.RFC3339, timestamp); err != nil {
			return fmt.Errorf("SBOM %s has invalid CycloneDX metadata timestamp", name)
		}
	}
	return nil
}

func isArchive(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") || strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".nupkg")
}

func archiveContainsLicense(path, name string) error {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".nupkg") {
		return zipContainsLicense(path, name)
	}
	return tarContainsLicense(path, name)
}

func tarContainsLicense(path, name string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", name, err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	var total int64
	for entries := 0; entries < 10000; entries++ {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("inspect %s: %w", name, err)
		}
		if err := safeArchivePath(header.Name); err != nil {
			return fmt.Errorf("inspect %s: %w", name, err)
		}
		total += header.Size
		if header.Size < 0 || total > 512<<20 {
			return fmt.Errorf("inspect %s: uncompressed archive exceeds 512 MiB", name)
		}
		if header.Typeflag == tar.TypeReg && strings.EqualFold(filepath.Base(header.Name), "LICENSE") {
			if header.Size <= 0 || header.Size > 1<<20 {
				return fmt.Errorf("inspect %s: LICENSE has invalid size", name)
			}
			return nil
		}
	}
	return fmt.Errorf("archive %s does not contain a regular LICENSE file", name)
}

func zipContainsLicense(path, name string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", name, err)
	}
	defer reader.Close()
	if len(reader.File) > 10000 {
		return fmt.Errorf("inspect %s: archive has too many entries", name)
	}
	var total uint64
	for _, entry := range reader.File {
		if err := safeArchivePath(entry.Name); err != nil {
			return fmt.Errorf("inspect %s: %w", name, err)
		}
		total += entry.UncompressedSize64
		if total > 512<<20 {
			return fmt.Errorf("inspect %s: uncompressed archive exceeds 512 MiB", name)
		}
		if !entry.FileInfo().IsDir() && strings.EqualFold(filepath.Base(entry.Name), "LICENSE") {
			if entry.UncompressedSize64 == 0 || entry.UncompressedSize64 > 1<<20 {
				return fmt.Errorf("inspect %s: LICENSE has invalid size", name)
			}
			return nil
		}
	}
	return fmt.Errorf("archive %s does not contain a regular LICENSE file", name)
}

func safeArchivePath(name string) error {
	clean := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(name, `\`, "/")))
	if name == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("unsafe archive path %q", name)
	}
	return nil
}

type executableRunner struct{}

func (executableRunner) Run(ctx context.Context, name string, arguments, environment []string) ([]byte, error) {
	executable, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("required verifier %s: %w", name, err)
	}
	commandContext, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(commandContext, executable, arguments...)
	command.Env = environment
	output := &boundedOutput{maximum: maxCommandOutput}
	command.Stdout = output
	command.Stderr = output
	err = command.Run()
	data, exceeded := output.Result()
	if exceeded {
		return nil, fmt.Errorf("%s output exceeds 1 MiB", name)
	}
	if err != nil {
		message := strings.TrimSpace(string(data))
		if message == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, message)
	}
	return data, nil
}

type boundedOutput struct {
	mu       sync.Mutex
	data     []byte
	maximum  int
	exceeded bool
}

func (output *boundedOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	remaining := output.maximum - len(output.data)
	if remaining > 0 {
		count := len(data)
		if count > remaining {
			count = remaining
		}
		output.data = append(output.data, data[:count]...)
	}
	if len(data) > remaining {
		output.exceeded = true
	}
	return len(data), nil
}

func (output *boundedOutput) Result() ([]byte, bool) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return append([]byte(nil), output.data...), output.exceeded
}

func verificationEnvironment(token string) []string {
	environment := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "GH_PROMPT_DISABLED=1"}
	if token != "" {
		environment = append(environment, "GH_TOKEN="+token)
	} else if existing := os.Getenv("GH_TOKEN"); existing != "" {
		environment = append(environment, "GH_TOKEN="+existing)
	}
	for _, name := range []string{"SSL_CERT_FILE", "SSL_CERT_DIR", "SIGSTORE_ROOT_FILE"} {
		if value := os.Getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

func MarshalStatement(statement Statement) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(statement); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
