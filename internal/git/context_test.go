package git

import (
	"context"
	"errors"
	"testing"
)

func TestCanceledGitOperationsDoNotMasqueradeAsAbsentRefs(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, operation := range []func() error{
		func() error { _, err := RefShaWithEnv(dir, "HEAD", nil, ctx); return err },
		func() error { _, err := TagExistsWithEnv(dir, "v1.0.0", nil, ctx); return err },
		func() error { _, err := LatestTagWithEnv(dir, "v*", nil, ctx); return err },
		func() error { _, err := RunWithContext(ctx, dir, nil, "status"); return err },
		func() error { _, err := CommitsWithEnv(dir, "", "HEAD", nil, ctx); return err },
	} {
		if err := operation(); !errors.Is(err, context.Canceled) {
			t.Fatalf("Git cancellation lost: %v", err)
		}
	}
}
