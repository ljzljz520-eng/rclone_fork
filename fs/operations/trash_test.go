// Integration tests for the trash staging area.

package operations_test

import (
	"context"
	"path"
	"sort"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/fs/rc"
	"github.com/rclone/rclone/fs/sync"
	"github.com/rclone/rclone/fstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trashTestCtx returns a context with the trash enabled and automatic
// cleanup disabled so tests are deterministic.
func trashTestCtx(t *testing.T) (context.Context, *fs.ConfigInfo) {
	ctx, ci := fs.AddConfig(context.Background())
	ci.Trash = true
	ci.TrashCleanupInterval = 0
	return ctx, ci
}

// visiblePaths lists all objects visible under f (the trash directory
// is filtered out).
func visiblePaths(t *testing.T, ctx context.Context, f fs.Fs) []string {
	paths := []string{}
	err := operations.ListFn(operations.TrashFilterCtx(ctx), f, func(o fs.Object) {
		paths = append(paths, o.Remote())
	})
	require.NoError(t, err)
	sort.Strings(paths)
	return paths
}

func trashItemPaths(t *testing.T, items []operations.TrashItem) []string {
	paths := make([]string, 0, len(items))
	for _, item := range items {
		paths = append(paths, item.OrigPath)
	}
	sort.Strings(paths)
	return paths
}

func readObject(t *testing.T, ctx context.Context, f fs.Fs, remote string) string {
	o, err := f.NewObject(ctx, remote)
	require.NoError(t, err)
	body, err := operations.ReadFile(ctx, o)
	require.NoError(t, err)
	return string(body)
}

// craftTrashEntryPath returns the trash layout remote path for orig at
// time t (the 16 character random suffix is fixed for determinism).
func craftTrashEntryPath(t time.Time, orig string) string {
	dateDir := t.UTC().Format("2006-01-02")
	idDir := t.UTC().Format("150405") + "-abcdef0123456789"
	return path.Join(".rclone-trash", dateDir, idDir, orig)
}

func TestTrashDeleteAndList(t *testing.T) {
	ctx, _ := trashTestCtx(t)
	r := fstest.NewRun(t)
	r.WriteObject(ctx, "sub/file1.txt", "content1", t1)
	r.WriteObject(ctx, "file2.txt", "content22", t1)

	require.NoError(t, operations.Delete(ctx, r.Fremote))
	assert.Equal(t, []string{}, visiblePaths(t, ctx, r.Fremote))

	items, err := operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, []string{"file2.txt", "sub/file1.txt"}, trashItemPaths(t, items))
	for _, item := range items {
		assert.WithinDuration(t, time.Now(), item.DelTime, time.Hour)
	}
	sizeByName := map[string]int64{}
	for _, item := range items {
		sizeByName[item.OrigPath] = item.Size
	}
	assert.Equal(t, int64(9), sizeByName["file2.txt"])
	assert.Equal(t, int64(8), sizeByName["sub/file1.txt"])
}

func TestTrashRestore(t *testing.T) {
	ctx, _ := trashTestCtx(t)
	r := fstest.NewRun(t)
	r.WriteObject(ctx, "file2.txt", "content22", t1)
	r.WriteObject(ctx, "sub/file1.txt", "content1", t1)
	require.NoError(t, operations.Delete(ctx, r.Fremote))

	// Restore everything.
	restored, skipped, err := operations.TrashRestore(ctx, r.Fremote, nil, false, false)
	require.NoError(t, err)
	assert.Equal(t, 2, restored)
	assert.Equal(t, 0, skipped)
	assert.Equal(t, "content22", readObject(t, ctx, r.Fremote, "file2.txt"))
	assert.Equal(t, "content1", readObject(t, ctx, r.Fremote, "sub/file1.txt"))
	items, err := operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Len(t, items, 0)

	// Delete again then restore with a conflict on one file.
	require.NoError(t, operations.Delete(ctx, r.Fremote))
	r.WriteObject(ctx, "file2.txt", "replacement", t2)
	restored, skipped, err = operations.TrashRestore(ctx, r.Fremote, nil, false, false)
	require.NoError(t, err)
	assert.Equal(t, 1, restored)
	assert.Equal(t, 1, skipped)
	// Conflict file is left untouched and kept in the trash.
	assert.Equal(t, "replacement", readObject(t, ctx, r.Fremote, "file2.txt"))
	items, err = operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, []string{"file2.txt"}, trashItemPaths(t, items))

	// Overwrite replaces the existing file.
	restored, skipped, err = operations.TrashRestore(ctx, r.Fremote, nil, false, true)
	require.NoError(t, err)
	assert.Equal(t, 1, restored)
	assert.Equal(t, 0, skipped)
	assert.Equal(t, "content22", readObject(t, ctx, r.Fremote, "file2.txt"))
}

func TestTrashRestorePatterns(t *testing.T) {
	ctx, _ := trashTestCtx(t)
	r := fstest.NewRun(t)
	r.WriteObject(ctx, "keep/file1.txt", "content1", t1)
	r.WriteObject(ctx, "other/file2.txt", "content22", t1)
	require.NoError(t, operations.Delete(ctx, r.Fremote))

	restored, skipped, err := operations.TrashRestore(ctx, r.Fremote, []string{"keep/**"}, false, false)
	require.NoError(t, err)
	assert.Equal(t, 1, restored)
	assert.Equal(t, 0, skipped)
	assert.Equal(t, "content1", readObject(t, ctx, r.Fremote, "keep/file1.txt"))
	_, err = r.Fremote.NewObject(ctx, "other/file2.txt")
	assert.Equal(t, fs.ErrorObjectNotFound, err)
}

func TestTrashCleanupRetention(t *testing.T) {
	ctx, ci := trashTestCtx(t)
	r := fstest.NewRun(t)
	// One old entry (2001), one recent entry (1 day ago).
	oldPath := craftTrashEntryPath(t1, "old.txt")
	newPath := craftTrashEntryPath(time.Now().Add(-24*time.Hour), "new.txt")
	r.WriteObject(ctx, oldPath, "old-content", t1)
	r.WriteObject(ctx, newPath, "new-content", t1)

	ci.TrashRetention = fs.Duration(7 * 24 * time.Hour)
	removed, bytes, err := operations.TrashCleanup(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	assert.Equal(t, int64(len("old-content")), bytes)
	items, err := operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, []string{"new.txt"}, trashItemPaths(t, items))

	// Retention 0 keeps everything.
	r.WriteObject(ctx, oldPath, "old-content", t1)
	ci.TrashRetention = 0
	removed, _, err = operations.TrashCleanup(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, 0, removed)
	items, err = operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Len(t, items, 2)
}

func TestTrashCleanupMaxSize(t *testing.T) {
	ctx, ci := trashTestCtx(t)
	r := fstest.NewRun(t)
	ci.TrashRetention = 0
	// Three entries of 5, 10 and 15 bytes with increasing deletion
	// times.
	r.WriteObject(ctx, craftTrashEntryPath(t1, "small.txt"), "12345", t1)
	r.WriteObject(ctx, craftTrashEntryPath(t2, "medium.txt"), "1234567890", t1)
	r.WriteObject(ctx, craftTrashEntryPath(t3, "large.txt"), "123456789012345", t1)

	ci.TrashMaxSize = fs.SizeSuffix(20)
	removed, _, err := operations.TrashCleanup(ctx, r.Fremote)
	require.NoError(t, err)
	// 5+10+15=30; evict oldest two: 30-5=25>20, 25-10=15<=20.
	assert.Equal(t, 2, removed)
	items, err := operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, []string{"large.txt"}, trashItemPaths(t, items))

	// Unlimited size evicts nothing.
	r.WriteObject(ctx, craftTrashEntryPath(t1, "small.txt"), "12345", t1)
	ci.TrashMaxSize = fs.SizeSuffix(-1)
	removed, _, err = operations.TrashCleanup(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, 0, removed)
}

func TestTrashEmpty(t *testing.T) {
	ctx, _ := trashTestCtx(t)
	r := fstest.NewRun(t)
	r.WriteObject(ctx, "a.txt", "aaa", t1)
	r.WriteObject(ctx, "b.txt", "bbb", t1)
	require.NoError(t, operations.Delete(ctx, r.Fremote))
	items, err := operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	require.Len(t, items, 2)

	removed, bytes, err := operations.TrashEmpty(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, 2, removed)
	assert.Equal(t, int64(6), bytes)
	items, err = operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Len(t, items, 0)
}

func TestTrashPurge(t *testing.T) {
	ctx, _ := trashTestCtx(t)
	r := fstest.NewRun(t)
	r.WriteObject(ctx, "a.txt", "aaa", t1)
	r.WriteObject(ctx, "sub/b.txt", "bbb", t1)

	require.NoError(t, operations.Purge(ctx, r.Fremote, ""))
	assert.Equal(t, []string{}, visiblePaths(t, ctx, r.Fremote))
	items, err := operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.txt", "sub/b.txt"}, trashItemPaths(t, items))
}

func TestTrashPurgeKeepsExistingTrash(t *testing.T) {
	ctx, _ := trashTestCtx(t)
	r := fstest.NewRun(t)
	// First deletion establishes a trash directory with content.
	r.WriteObject(ctx, "old.txt", "old", t1)
	require.NoError(t, operations.Delete(ctx, r.Fremote))
	items, err := operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	require.Len(t, items, 1)
	oldItem := items[0]

	// Purging the same root must move new files into the trash
	// without deleting the trash already there.
	r.WriteObject(ctx, "new.txt", "new", t2)
	require.NoError(t, operations.Purge(ctx, r.Fremote, ""))
	items, err = operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, []string{"new.txt", "old.txt"}, trashItemPaths(t, items))
	// The earlier trashed file is untouched.
	for _, item := range items {
		if item.OrigPath == "old.txt" {
			assert.WithinDuration(t, oldItem.DelTime, item.DelTime, time.Second)
		}
	}
}

func TestTrashPurgeWithNestedTrash(t *testing.T) {
	ctx, _ := trashTestCtx(t)
	r := fstest.NewRun(t)
	// A trash bin created while operating on a sub directory appears
	// nested from the root; purging the root must preserve it and
	// not fail.
	nestedTrashPath := path.Join("sub", craftTrashEntryPath(t1, "old.txt"))
	r.WriteObject(ctx, nestedTrashPath, "old", t1)
	r.WriteObject(ctx, "sub/new.txt", "new", t2)

	require.NoError(t, operations.Purge(ctx, r.Fremote, ""))
	assert.Equal(t, []string{}, visiblePaths(t, ctx, r.Fremote))
	// The nested trash entry is still present.
	_, err := r.Fremote.NewObject(ctx, nestedTrashPath)
	require.NoError(t, err)
	// The new file has been moved into the root trash.
	items, err := operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, []string{"sub/new.txt"}, trashItemPaths(t, items))
}

func TestTrashSyncDeletions(t *testing.T) {
	ctx, _ := trashTestCtx(t)
	r := fstest.NewRun(t)
	// File only on the destination; sync from an empty source deletes
	// it and must move it into the trash.
	r.WriteObject(ctx, "old.txt", "old-content", t1)
	require.NoError(t, operations.Mkdir(ctx, r.Flocal, ""))
	// Sync refuses to delete if it sees errors accumulated in the
	// global stats by earlier tests.
	accounting.GlobalStats().ResetCounters()

	require.NoError(t, sync.Sync(ctx, r.Fremote, r.Flocal, false))
	assert.Equal(t, []string{}, visiblePaths(t, ctx, r.Fremote))
	items, err := operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, []string{"old.txt"}, trashItemPaths(t, items))
}

func TestTrashMoveSemantics(t *testing.T) {
	ctx, _ := trashTestCtx(t)
	r := fstest.NewRun(t)
	src := r.WriteObject(ctx, "a/m.txt", "new-moved", t1)
	// Existing destination file should be moved into the trash by the
	// overwrite during move.
	r.WriteObject(ctx, "b/m.txt", "old-dest", t2)

	o, err := r.Fremote.NewObject(ctx, src.Path)
	require.NoError(t, err)
	dst, err := r.Fremote.NewObject(ctx, "b/m.txt")
	require.NoError(t, err)
	_, err = operations.Move(ctx, r.Fremote, dst, "b/m.txt", o)
	require.NoError(t, err)

	// Source is gone permanently (not in trash).
	assert.Equal(t, []string{"b/m.txt"}, visiblePaths(t, ctx, r.Fremote))
	assert.Equal(t, "new-moved", readObject(t, ctx, r.Fremote, "b/m.txt"))
	items, err := operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, []string{"b/m.txt"}, trashItemPaths(t, items))
	require.Len(t, items, 1)
	assert.Equal(t, int64(len("old-dest")), items[0].Size)
}

func TestTrashDeleteInsideTrashIsPermanent(t *testing.T) {
	ctx, _ := trashTestCtx(t)
	r := fstest.NewRun(t)
	trashPath := craftTrashEntryPath(t1, "x.txt")
	r.WriteObject(ctx, trashPath, "xxx", t1)
	o, err := r.Fremote.NewObject(ctx, trashPath)
	require.NoError(t, err)
	require.NoError(t, operations.DeleteFile(ctx, o))
	_, err = r.Fremote.NewObject(ctx, trashPath)
	assert.ErrorIs(t, err, fs.ErrorObjectNotFound)
	items, err := operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Len(t, items, 0)
}

func TestTrashAntiLoop(t *testing.T) {
	ctx, _ := trashTestCtx(t)
	r := fstest.NewRun(t)
	r.WriteObject(ctx, "a.txt", "aaa", t1)
	require.NoError(t, operations.Delete(ctx, r.Fremote))
	items, err := operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	require.Len(t, items, 1)
	// Deleting the now empty tree again must not re-trash anything.
	require.NoError(t, operations.Delete(ctx, r.Fremote))
	items, err = operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Len(t, items, 1)
}

func TestTrashDisabled(t *testing.T) {
	ctx := context.Background()
	r := fstest.NewRun(t)
	r.WriteObject(ctx, "a.txt", "aaa", t1)
	// Files in the trash named directory must be deleted/synced as
	// normal when the trash is disabled (no hidden exclude rule).
	r.WriteObject(ctx, ".rclone-trash/2020-01-01/010100-abcdef0123456789/old.txt", "old", t1)
	require.NoError(t, operations.Delete(ctx, r.Fremote))
	_, err := r.Fremote.NewObject(ctx, "a.txt")
	assert.ErrorIs(t, err, fs.ErrorObjectNotFound)
	_, err = r.Fremote.NewObject(ctx, ".rclone-trash/2020-01-01/010100-abcdef0123456789/old.txt")
	assert.ErrorIs(t, err, fs.ErrorObjectNotFound)
	items, err := operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Len(t, items, 0)
}

func TestTrashEmptyMissingAndForeign(t *testing.T) {
	ctx, _ := trashTestCtx(t)
	r := fstest.NewRun(t)

	// Empty on a remote without a trash directory is a no-op.
	removed, bytes, err := operations.TrashEmpty(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, 0, removed)
	assert.Equal(t, int64(0), bytes)

	// Empty deletes trashed files and also any files which don't
	// follow the trash layout (e.g. placed there manually).
	r.WriteObject(ctx, craftTrashEntryPath(t1, "trashed.txt"), "trashed", t1)
	r.WriteObject(ctx, ".rclone-trash/foreign.txt", "foreign", t1)
	removed, bytes, err = operations.TrashEmpty(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, 2, removed)
	assert.Equal(t, int64(len("trashed")+len("foreign")), bytes)
	_, err = r.Fremote.NewObject(ctx, craftTrashEntryPath(t1, "trashed.txt"))
	assert.ErrorIs(t, err, fs.ErrorObjectNotFound)
	_, err = r.Fremote.NewObject(ctx, ".rclone-trash/foreign.txt")
	assert.ErrorIs(t, err, fs.ErrorObjectNotFound)

	// Empty is idempotent on the now existing but empty directory.
	removed, _, err = operations.TrashEmpty(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, 0, removed)
}

func TestTrashDryRun(t *testing.T) {
	ctx, ci := trashTestCtx(t)
	ci.DryRun = true
	r := fstest.NewRun(t)
	r.WriteObject(ctx, "a.txt", "aaa", t1)

	require.NoError(t, operations.Delete(ctx, r.Fremote))
	assert.Equal(t, "aaa", readObject(t, ctx, r.Fremote, "a.txt"))

	ci.DryRun = false
	require.NoError(t, operations.Delete(ctx, r.Fremote))
	items, err := operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	require.Len(t, items, 1)

	ci.DryRun = true
	restored, _, err := operations.TrashRestore(ctx, r.Fremote, nil, false, false)
	require.NoError(t, err)
	assert.Equal(t, 1, restored)
	_, err = r.Fremote.NewObject(ctx, "a.txt")
	assert.ErrorIs(t, err, fs.ErrorObjectNotFound)
	removed, _, err := operations.TrashEmpty(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	items, err = operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Len(t, items, 1)
}

// rc endpoints: list, restore, cleanup and empty behave like the
// operations functions.
func TestRcTrash(t *testing.T) {
	ctx := context.Background()
	r, listCall := rcNewRun(t, "operations/trashlist")
	restoreCall := rc.Calls.Get("operations/trashrestore")
	assert.NotNil(t, restoreCall)
	cleanupCall := rc.Calls.Get("operations/trashcleanup")
	assert.NotNil(t, cleanupCall)
	emptyCall := rc.Calls.Get("operations/trashempty")
	assert.NotNil(t, emptyCall)

	// Two trash entries: one old (expired by the default 7d
	// retention), one recent.
	oldPath := craftTrashEntryPath(t1, "old.txt")
	newPath := craftTrashEntryPath(time.Now().Add(-time.Hour), "new.txt")
	restorePath := craftTrashEntryPath(time.Now().Add(-time.Hour), "rc.txt")
	r.WriteObject(ctx, oldPath, "old-content", t1)
	r.WriteObject(ctx, newPath, "new-content", t1)
	r.WriteObject(ctx, restorePath, "rc-content", t1)

	// list
	out, err := listCall.Fn(ctx, rc.Params{"fs": r.FremoteName})
	require.NoError(t, err)
	items := out["items"].([]operations.TrashItem)
	assert.Len(t, items, 3)

	// restore only the rc.txt entry via a pattern
	out, err = restoreCall.Fn(ctx, rc.Params{
		"fs":       r.FremoteName,
		"patterns": []string{"rc.txt"},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, out["restored"])
	assert.Equal(t, 0, out["skipped"])
	assert.Equal(t, "rc-content", readObject(t, ctx, r.Fremote, "rc.txt"))

	// cleanup evicts the old expired entry, leaves the recent one
	out, err = cleanupCall.Fn(ctx, rc.Params{"fs": r.FremoteName})
	require.NoError(t, err)
	assert.Equal(t, 1, out["removed"])

	// empty removes everything left
	out, err = emptyCall.Fn(ctx, rc.Params{"fs": r.FremoteName})
	require.NoError(t, err)
	assert.Equal(t, 1, out["removed"])
	trashItems, err := operations.TrashList(ctx, r.Fremote)
	require.NoError(t, err)
	assert.Len(t, trashItems, 0)

	// missing fs parameter is an error
	_, err = listCall.Fn(ctx, rc.Params{})
	require.Error(t, err)
}
