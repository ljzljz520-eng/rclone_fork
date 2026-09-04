// Internal unit tests for the trash staging area.

package operations

import (
	"context"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrashEntryLayout(t *testing.T) {
	now := time.Date(2026, 9, 4, 15, 4, 5, 0, time.UTC)
	for _, origRemote := range []string{
		"file.txt",
		"dir/sub/file.txt",
		"file with space.txt",
	} {
		entry := trashEntryRemote(now, origRemote)
		delTime, gotOrig, ok := parseTrashEntry(entry)
		require.True(t, ok, entry)
		assert.Equal(t, origRemote, gotOrig)
		assert.True(t, delTime.Equal(now), "got %v want %v", delTime, now)
	}
}

func TestTrashEntryLayoutRejects(t *testing.T) {
	for _, remote := range []string{
		"",
		"file.txt",
		"2026-09-04/file.txt",
		"2026-09-04/",
		"bad-date/150405-abcdef0123456789/file.txt",
		"2026-09-04/badentry/file.txt",
		"2026-09-04/150405-abcdef0123456789/",
	} {
		_, _, ok := parseTrashEntry(remote)
		assert.False(t, ok, remote)
	}
}

func TestTrashExcluded(t *testing.T) {
	ctx, ci := fs.AddConfig(context.Background())
	ci.Trash = true
	ci.TrashDir = ".rclone-trash"

	// Object living inside the trash directory from a parent Fs.
	assert.True(t, trashExcluded(ctx, newInfo("root"), ".rclone-trash/2026-09-04/150405-abc/file.txt"))
	assert.True(t, trashExcluded(ctx, newInfo("root"), ".rclone-trash"))
	// Object from a normal path.
	assert.False(t, trashExcluded(ctx, newInfo("root"), "dir/file.txt"))

	// Fs rooted at the trash directory itself (e.g. explicit delete
	// of the trash).
	assert.True(t, trashExcluded(ctx, newInfo("root/.rclone-trash"), "file.txt"))
	assert.False(t, trashExcluded(ctx, newInfo("root/.rclone-trash-backup"), "file.txt"))

	// Trash disabled.
	ci.Trash = false
	assert.False(t, trashExcluded(ctx, newInfo("root"), ".rclone-trash/file.txt"))
}

func TestTrashFilterRule(t *testing.T) {
	ctx, ci := fs.AddConfig(context.Background())
	// No rule when the trash is disabled even if TrashDir is set.
	assert.Equal(t, "", trashFilterRule(ctx))
	ci.Trash = true
	assert.Equal(t, "- .rclone-trash/**", trashFilterRule(ctx))
	ci.TrashDir = "my-trash"
	assert.Equal(t, "- my-trash/**", trashFilterRule(ctx))
	ci.TrashDir = ""
	assert.Equal(t, "", trashFilterRule(ctx))
}

// info is a minimal fs.Info implementation for the trash tests.
type info struct {
	root string
}

func newInfo(root string) *info { return &info{root: root} }

func (i *info) Name() string             { return "" }
func (i *info) Root() string             { return i.root }
func (i *info) String() string           { return "test:" + i.root }
func (i *info) Precision() time.Duration { return fs.ModTimeNotSupported }
func (i *info) Hashes() hash.Set         { return hash.NewHashSet() }
func (i *info) Features() *fs.Features   { return nil }
