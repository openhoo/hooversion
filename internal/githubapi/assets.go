package githubapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const maxReleaseInventory = 10000

// ListReleaseAssets reads a complete, bounded release inventory. Page URLs
// are generated from the configured API origin, never from response links.
func (c *Client) ListReleaseAssets(repo string, releaseID int64) ([]Asset, error) {
	if releaseID <= 0 {
		return nil, fmt.Errorf("invalid release ID %d", releaseID)
	}
	assets := []Asset{}
	ids, names := map[int64]bool{}, map[string]bool{}
	for page := 1; page <= maxReleaseInventory/100+1; page++ {
		endpoint := fmt.Sprintf("%s/repos/%s/releases/%d/assets?per_page=100&page=%d", c.BaseURL, repo, releaseID, page)
		req, err := c.newRequest(http.MethodGet, endpoint, "", nil)
		if err != nil {
			return nil, err
		}
		response, err := c.do(req, false)
		if err != nil {
			return nil, err
		}
		var batch []Asset
		if err := decodeJSONBody(response, &batch); err != nil {
			return nil, err
		}
		if len(batch) > 100 {
			return nil, fmt.Errorf("release asset page exceeds 100 entries")
		}
		for _, asset := range batch {
			if asset.ID <= 0 || asset.Name == "" || asset.Size < 0 || ids[asset.ID] || names[asset.Name] {
				return nil, fmt.Errorf("invalid or duplicate release asset %q (ID %d)", asset.Name, asset.ID)
			}
			ids[asset.ID], names[asset.Name] = true, true
			assets = append(assets, asset)
			if len(assets) > maxReleaseInventory {
				return nil, fmt.Errorf("release asset inventory exceeds %d entries", maxReleaseInventory)
			}
		}
		if len(batch) < 100 {
			return assets, nil
		}
	}
	return nil, fmt.Errorf("release asset pagination exceeds limit")
}

// VerifyExistingAssetFrom binds retry recovery to local content. Servers
// without digest metadata are supported by downloading and hashing the asset.
func (c *Client) VerifyExistingAssetFrom(root, repo string, asset Asset, localPath string) error {
	if asset.ID <= 0 || asset.State != "uploaded" {
		return fmt.Errorf("release asset %s is not a valid uploaded asset", asset.Name)
	}
	data, err := readValidatedReleaseAssetFrom(root, localPath)
	if err != nil {
		return err
	}
	if asset.Size != int64(len(data)) {
		return fmt.Errorf("release asset %s size differs from local file", asset.Name)
	}
	expected := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	actual := asset.Digest
	if actual != "" {
		if !strings.HasPrefix(actual, "sha256:") || len(actual) != len("sha256:")+64 {
			return fmt.Errorf("release asset %s has unsupported or malformed digest", asset.Name)
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(actual, "sha256:")); err != nil {
			return fmt.Errorf("release asset %s has malformed digest", asset.Name)
		}
		actual = strings.ToLower(actual)
	} else {
		dir, err := os.MkdirTemp("", "hooversion-asset-verify-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		actual, err = c.DownloadAsset(repo, asset, filepath.Join(dir, "asset"), maxReleaseAssetSizeBytes)
		if err != nil {
			return err
		}
	}
	if actual != expected {
		return fmt.Errorf("release asset %s digest differs from local file", asset.Name)
	}
	return nil
}
