package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openhoo/hooversion/internal/git"
	"github.com/openhoo/hooversion/internal/safefs"
	"github.com/openhoo/hooversion/internal/types"
)

const transactionName = "hooversion-transaction.json"
const maxSnapshotBytes = 64 << 20
const maxJournalBytes = 96 << 20
const processRecoveryTimeout = 2 * time.Minute

type fileSnapshot struct {
	Data   []byte      `json:"data,omitempty"`
	Mode   os.FileMode `json:"mode,omitempty"`
	Link   string      `json:"link,omitempty"`
	Exists bool        `json:"exists"`
}
type transactionState struct {
	Index        []byte                  `json:"index"`
	IndexMode    os.FileMode             `json:"indexMode"`
	Version      int                     `json:"version"`
	Phase        string                  `json:"phase"`
	Checkout     string                  `json:"checkout"`
	ConfigDigest string                  `json:"configDigest"`
	Source       string                  `json:"source"`
	ReleaseHead  string                  `json:"releaseHead,omitempty"`
	HeadRef      string                  `json:"headRef"`
	Plan         *types.ReleasePlan      `json:"plan"`
	Files        map[string]fileSnapshot `json:"files"`
	Untracked    []string                `json:"untracked"`
	Tags         map[string]string       `json:"tags"`
}
type transaction struct {
	state    transactionState
	files    *safefs.Root
	metadata *safefs.Root
	cwd      string
	env      []string
}

func configDigest(config *types.NormalizedConfig) string {
	data, _ := json.Marshal(config)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func openTransaction(ctx context.Context, cwd string, env []string) (*transaction, *safefs.Lock, error) {
	common, err := git.RunWithContext(ctx, cwd, env, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, nil, err
	}
	lock, err := safefs.AcquireLock(filepath.Join(strings.TrimSpace(common), "hooversion-release.lock"))
	if err != nil {
		return nil, nil, err
	}
	fail := func(e error) (*transaction, *safefs.Lock, error) { lock.Close(); return nil, nil, e }
	gitdir, err := git.RunWithContext(ctx, cwd, env, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return fail(err)
	}
	metadata, err := safefs.OpenRoot(strings.TrimSpace(gitdir))
	if err != nil {
		return fail(err)
	}
	files, err := safefs.OpenRoot(cwd)
	if err != nil {
		metadata.Close()
		return fail(err)
	}
	absolute, err := filepath.Abs(cwd)
	if err != nil {
		files.Close()
		metadata.Close()
		return fail(err)
	}
	return &transaction{metadata: metadata, files: files, cwd: absolute, env: env}, lock, nil
}
func (t *transaction) close() { t.files.Close(); t.metadata.Close() }
func (t *transaction) load(config *types.NormalizedConfig) (bool, error) {
	data, err := t.metadata.ReadRegularFile(transactionName, maxJournalBytes)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t.state); err != nil {
		return false, fmt.Errorf("invalid release journal: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return false, fmt.Errorf("release journal must contain exactly one JSON document")
	}

	if t.state.Version != 1 || t.state.Checkout != t.cwd || t.state.Plan == nil || t.state.Source != t.state.Plan.SourceSha || t.state.ConfigDigest != configDigest(config) {
		return false, fmt.Errorf("release recovery journal does not match this checkout and configuration; preserve it and resolve the mismatch before retrying")
	}
	if len(t.state.Index) < 12 || len(t.state.Index) > 16<<20 || string(t.state.Index[:4]) != "DIRC" {
		return false, fmt.Errorf("invalid release journal Git index snapshot")
	}
	if t.state.HeadRef != "HEAD" && t.state.HeadRef != "refs/heads/"+t.state.Plan.Branch {
		return false, fmt.Errorf("invalid release journal branch identity")
	}
	if err := git.AssertValidGitRef("branch", t.state.Plan.Branch); err != nil {
		return false, err
	}
	switch t.state.Phase {
	case "prepared", "mutating", "committing", "tagging", "external", "published":
	default:
		return false, fmt.Errorf("invalid release journal phase %q", t.state.Phase)
	}
	// Paths are validated before recovery performs any destructive action.
	for path := range t.state.Files {
		if !filepath.IsLocal(path) || filepath.Clean(path) == "." || path == ".git" || strings.HasPrefix(filepath.ToSlash(path), ".git/") {
			return false, fmt.Errorf("unsafe journal path %q", path)
		}
	}
	for tag := range t.state.Tags {
		if err := git.AssertValidGitRef("tag", tag); err != nil {
			return false, err
		}
	}
	return true, nil
}
func (t *transaction) save() error {
	data, err := json.Marshal(t.state)
	if err != nil {
		return err
	}
	if len(data) > maxJournalBytes {
		return fmt.Errorf("release journal exceeds %d bytes", maxJournalBytes)
	}
	return t.metadata.WriteFileAtomic(transactionName, data, 0600)
}
func (t *transaction) advance(phase string) error { t.state.Phase = phase; return t.save() }
func (t *transaction) finish() error {
	if err := t.metadata.Remove(transactionName); err != nil && !os.IsNotExist(err) {
		return err
	}
	return t.metadata.Sync()
}
func machinePaths(raw string) []string {
	var paths []string
	for _, path := range strings.Split(raw, "\x00") {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}
func (t *transaction) snapshot(ctx context.Context, config *types.NormalizedConfig, plan *types.ReleasePlan, paths []string) error {
	tracked, err := git.RunWithContext(ctx, t.cwd, t.env, "ls-files", "-z")
	if err != nil {
		return err
	}
	untracked, err := git.RunWithContext(ctx, t.cwd, t.env, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	headRef, err := git.RunWithContext(ctx, t.cwd, t.env, "rev-parse", "--symbolic-full-name", "HEAD")
	if err != nil {
		return err
	}
	source, err := git.HeadShaWithEnv(t.cwd, t.env, ctx)
	if err != nil {
		return err
	}
	t.state = transactionState{Version: 1, Phase: "prepared", Checkout: t.cwd, ConfigDigest: configDigest(config), Source: plan.SourceSha, HeadRef: strings.TrimSpace(headRef), Plan: plan, Files: map[string]fileSnapshot{}, Untracked: machinePaths(untracked), Tags: map[string]string{}}
	if source != plan.SourceSha {
		t.state.ReleaseHead = source
		t.state.Phase = "external"
	} // pre-existing resumable commit is never rolled back
	index, err := t.metadata.ReadRegularFile("index", 16<<20)
	if err != nil {
		return err
	}
	t.state.Index = index
	indexInfo, err := t.metadata.Lstat("index")
	if err != nil {
		return err
	}
	t.state.IndexMode = indexInfo.Mode().Perm()
	paths = append(paths, machinePaths(tracked)...)
	total := 0
	for _, path := range paths {
		if filepath.IsAbs(path) {
			path, err = filepath.Rel(t.cwd, path)
			if err != nil {
				return err
			}
		}
		path = filepath.Clean(path)
		if _, exists := t.state.Files[path]; exists {
			continue
		}
		if !filepath.IsLocal(path) || path == "." || path == ".git" || strings.HasPrefix(filepath.ToSlash(path), ".git/") {
			return fmt.Errorf("unsafe snapshot path %q", path)
		}
		info, err := t.files.Lstat(path)
		if os.IsNotExist(err) {
			t.state.Files[path] = fileSnapshot{}
			continue
		}
		if err != nil {
			return err
		}
		snapshot := fileSnapshot{Exists: true, Mode: info.Mode().Perm()}
		switch {
		case info.Mode().IsRegular():
			snapshot.Data, err = t.files.ReadRegularFile(path, 16<<20)
		case info.Mode()&os.ModeSymlink != 0:
			snapshot.Link, err = t.files.Readlink(path)
		default:
			err = fmt.Errorf("transaction cannot snapshot special file %s", path)
		}
		if err != nil {
			return err
		}
		total += len(snapshot.Data) + len(snapshot.Link)
		if total > maxSnapshotBytes {
			return fmt.Errorf("release snapshot exceeds %d bytes", maxSnapshotBytes)
		}
		t.state.Files[path] = snapshot
	}
	for _, release := range plan.Releases {
		raw, err := git.RunWithContext(ctx, t.cwd, t.env, "show-ref", "--verify", "--hash", "refs/tags/"+release.Tag)
		if err != nil {
			peeled, e := git.RefShaWithEnv(t.cwd, "refs/tags/"+release.Tag, t.env, ctx)
			if e != nil {
				return e
			}
			if peeled != "" {
				return err
			}
			raw = ""
		}
		t.state.Tags[release.Tag] = strings.TrimSpace(raw)
	}
	return t.save()
}
func (t *transaction) releaseHead(ctx context.Context) error {
	head, err := git.HeadShaWithEnv(t.cwd, t.env, ctx)
	if err != nil {
		return err
	}
	if head == t.state.Source {
		return nil
	}
	parent, err := git.RefShaWithEnv(t.cwd, "HEAD^", t.env, ctx)
	if err != nil {
		return err
	}
	message, err := git.CommitMessageWithEnv(t.cwd, "HEAD", t.env, ctx)
	if err != nil {
		return err
	}
	if parent != t.state.Source || !releaseMessageMatches(message, CommitMessage(t.state.Plan)) {
		return fmt.Errorf("release recovery refuses changed HEAD %s; expected source %s or its exact release commit", head, t.state.Source)
	}
	if t.state.ReleaseHead != "" && t.state.ReleaseHead != head {
		return fmt.Errorf("release recovery found release commit drift")
	}
	t.state.ReleaseHead = head
	return nil
}
func (t *transaction) recover(ctx context.Context) (bool, error) {
	if err := t.checkIdentity(); err != nil {
		return false, err
	}
	if t.state.Phase == "external" || t.state.Phase == "published" {
		if err := t.releaseHead(ctx); err != nil {
			return false, err
		}
		if t.state.ReleaseHead == "" {
			return false, fmt.Errorf("published journal has no release commit")
		}
		head, err := git.HeadShaWithEnv(t.cwd, t.env, ctx)
		if err != nil {
			return false, err
		}
		if head != t.state.ReleaseHead {
			return false, fmt.Errorf("publication recovery requires the recorded release commit %s", t.state.ReleaseHead)
		}

		return false, nil // irreversible or uncertain remote effects: resume, never rollback
	}
	if err := t.rollback(ctx); err != nil {
		return false, err
	}
	return true, nil
}
func (t *transaction) rollback(ctx context.Context) error {
	if err := t.checkIdentity(); err != nil {
		return err
	}
	ref, err := git.RunWithContext(ctx, t.cwd, t.env, "rev-parse", "--symbolic-full-name", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(ref) != t.state.HeadRef {
		return fmt.Errorf("release recovery refuses a changed checkout branch")
	}
	if err := t.releaseHead(ctx); err != nil {
		return err
	}
	head, err := git.HeadShaWithEnv(t.cwd, t.env, ctx)
	if err != nil {
		return err
	}
	// Restore tag refs with compare-and-swap; never delete a tag moved by another writer.
	for tag, original := range t.state.Tags {
		peeled, err := git.RefShaWithEnv(t.cwd, "refs/tags/"+tag, t.env, ctx)
		if err != nil {
			return err
		}
		if peeled == "" {
			if original != "" {
				return fmt.Errorf("release recovery found missing pre-existing tag %s", tag)
			}
			continue
		}
		raw, err := git.RunWithContext(ctx, t.cwd, t.env, "show-ref", "--verify", "--hash", "refs/tags/"+tag)
		if err != nil {
			return err
		}
		raw = strings.TrimSpace(raw)
		if raw == original {
			continue
		}
		if original != "" || peeled != head || t.state.ReleaseHead == "" {
			return fmt.Errorf("release recovery refuses tag drift: %s", tag)
		}
		if _, err := git.RunWithContext(ctx, t.cwd, t.env, "update-ref", "-d", "refs/tags/"+tag, raw); err != nil {
			return err
		}
	}
	untracked, err := git.RunWithContext(ctx, t.cwd, t.env, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	candidates := machinePaths(untracked)
	if head != t.state.Source {
		changed, err := git.RunWithContext(ctx, t.cwd, t.env, "diff", "--name-only", "-z", t.state.Source, head, "--")
		if err != nil {
			return err
		}
		candidates = append(candidates, machinePaths(changed)...)
	}
	originalUntracked := map[string]bool{}
	for _, path := range t.state.Untracked {
		originalUntracked[path] = true
	}
	// Files created during the clean-tree transaction are removed; initial
	// untracked and ignored files outside managed destinations are preserved.
	for _, path := range candidates {
		if _, existed := t.state.Files[path]; existed || originalUntracked[path] {
			continue
		}
		info, err := t.files.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			return fmt.Errorf("recovery refuses to remove directory %s", path)
		}
		if err := t.files.Remove(path); err != nil {
			return err
		}
	}
	paths := make([]string, 0, len(t.state.Files))
	for path := range t.state.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		before := t.state.Files[path]
		if !before.Exists {
			if err := t.files.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if err := t.files.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if before.Link != "" {
			if err := t.files.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := t.files.Symlink(before.Link, path); err != nil {
				return err
			}
		} else {
			// Refuse symlink substitutions rather than following them during rollback.
			if err := t.files.RestoreFile(path, before.Data, before.Mode); err != nil {
				return err
			}
		}
	}
	if head != t.state.Source {
		if _, err := git.RunWithContext(ctx, t.cwd, t.env, "update-ref", t.state.HeadRef, t.state.Source, head); err != nil {
			return err
		}
	}
	if _, err := t.metadata.Lstat("index.lock"); err == nil {
		return fmt.Errorf("release recovery refuses to overwrite a locked Git index; preserve the journal and resolve index.lock")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := t.metadata.RestoreFile("index", t.state.Index, t.state.IndexMode); err != nil {
		return err
	}

	return t.finish()
}

// abort restores local changes with a fresh cleanup deadline even when the
// caller was canceled. External/uncertain publication remains resumable.
func (t *transaction) abort(cause error) error {
	if t.state.Phase == "external" || t.state.Phase == "published" {
		return fmt.Errorf("%w (release journal retained for publication retry)", cause)
	}
	cleanup, cancel := context.WithTimeout(context.Background(), processRecoveryTimeout)
	defer cancel()
	if err := t.rollback(cleanup); err != nil {
		return errors.Join(cause, fmt.Errorf("automatic release recovery could not finish; journal retained: %w", err))
	}
	return cause
}

func (t *transaction) checkIdentity() error {
	if err := t.files.CheckIdentity(); err != nil {
		return err
	}
	return t.metadata.CheckIdentity()
}
