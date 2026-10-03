package githubapi

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestListReleaseAssetsPagination(t *testing.T) {
	var pages []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/releases/42/assets" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("unexpected request %s", r.URL)
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		pages = append(pages, page)
		count := 100
		if page == 3 {
			count = 1
		}
		assets := []Asset{}
		for i := 0; i < count; i++ {
			id := int64((page-1)*100 + i + 1)
			assets = append(assets, Asset{ID: id, Name: fmt.Sprintf("asset-%d", id), State: "uploaded"})
		}
		json.NewEncoder(w).Encode(assets)
	}))
	defer server.Close()
	assets, err := New(server.URL, "token").ListReleaseAssets("o/r", 42)
	if err != nil || len(assets) != 201 || fmt.Sprint(pages) != "[1 2 3]" {
		t.Fatalf("assets=%d pages=%v err=%v", len(assets), pages, err)
	}
}

func TestListReleaseAssetsRejectsBrokenInventory(t *testing.T) {
	for _, mode := range []string{"duplicate-id", "duplicate-name", "negative-size", "invalid-id", "oversized-page", "limit", "server-error", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "server-error" {
					w.WriteHeader(500)
					return
				}
				if mode == "malformed" {
					fmt.Fprint(w, "{}")
					return
				}
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				count := 100
				if mode == "oversized-page" {
					count = 101
				}
				batch := []Asset{}
				for i := 0; i < count; i++ {
					id := int64((page-1)*100 + i + 1)
					a := Asset{ID: id, Name: fmt.Sprintf("a-%d", id)}
					if i == 0 && page == 2 {
						switch mode {
						case "duplicate-id":
							a.ID = 1
						case "duplicate-name":
							a.Name = "a-1"
						case "negative-size":
							a.Size = -1
						case "invalid-id":
							a.ID = 0
						}
					}
					batch = append(batch, a)
				}
				json.NewEncoder(w).Encode(batch)
			}))
			defer server.Close()
			if _, err := New(server.URL, "").ListReleaseAssets("o/r", 42); err == nil {
				t.Fatal("broken inventory accepted")
			}
		})
	}
}

func TestVerifyExistingAssetBindsContent(t *testing.T) {
	root := t.TempDir()
	payload := []byte("expected")
	if err := os.WriteFile(filepath.Join(root, "asset"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
	for _, mode := range []string{"digest", "fallback", "wrong-digest", "wrong-bytes", "size", "state", "id", "malformed", "unsupported", "short"} {
		t.Run(mode, func(t *testing.T) {
			downloads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downloads++
				data := payload
				if mode == "wrong-bytes" {
					data = []byte("replaced")
				}
				if mode == "short" {
					data = data[:2]
				}
				w.Write(data)
			}))
			defer server.Close()
			asset := Asset{ID: 1, Name: "asset", State: "uploaded", Size: int64(len(payload)), Digest: hash}
			switch mode {
			case "fallback", "wrong-bytes", "short":
				asset.Digest = ""
			case "wrong-digest":
				asset.Digest = "sha256:" + strings.Repeat("0", 64)
			case "size":
				asset.Size++
			case "state":
				asset.State = "starter"
			case "id":
				asset.ID = 0
			case "malformed":
				asset.Digest = "sha256:" + strings.Repeat("z", 64)
			case "unsupported":
				asset.Digest = "md5:abcd"
			}
			err := New(server.URL, "token").VerifyExistingAssetFrom(root, "o/r", asset, "asset")
			success := mode == "digest" || mode == "fallback"
			if (err == nil) != success {
				t.Fatalf("err=%v success=%v", err, success)
			}
			if mode == "digest" && downloads != 0 {
				t.Fatal("digest match unexpectedly downloaded")
			}
			if mode == "fallback" && downloads != 1 {
				t.Fatalf("fallback downloads=%d", downloads)
			}
		})
	}
}
