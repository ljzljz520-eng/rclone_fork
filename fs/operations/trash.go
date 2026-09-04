// Trash staging area: when --trash is set, deleted files are moved
// into a trash directory under the Fs root instead of being deleted
// permanently. The trashed files can be listed, restored, cleaned up
// according to retention/size limits or emptied completely.
//
// Layout inside the trash directory:
//
//	<trash-dir>/<YYYY-MM-DD>/<HHMMSS>-<random>/<original path>
//
// The date and entry directory encode the deletion time, the rest of
// the path is the original path of the file relative to the Fs root.
// Each deleted object gets its own entry directory so deleting the
// same path more than once never overwrites an earlier trashed copy.

package operations

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/filter"
	"github.com/rclone/rclone/fs/fserrors"
	"github.com/rclone/rclone/fs/fspath"
	"github.com/rclone/rclone/fs/rc"
	"github.com/rclone/rclone/fs/walk"
	"github.com/rclone/rclone/lib/random"
)

const (
	trashDateLayout  = "2006-01-02"
	trashEntryLayout = "150405"
	trashRandomLen   = 16
)

// Context key marking operations internal to the trash which must
// delete permanently rather than moving files back into the trash.
type trashBypassKeyType struct{}

var trashBypassKey trashBypassKeyType

// trashBypassCtx returns a ctx in which deletes are permanent even
// when --trash is enabled.
func trashBypassCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, trashBypassKey, true)
}

// TrashBypassCtx returns a context in which deletes are permanent even
// when --trash is enabled. It should be used for deletions which are
// part of another operation such as deleting the source after a move.
func TrashBypassCtx(ctx context.Context) context.Context {
	return trashBypassCtx(ctx)
}

// isTrashBypassed returns true if the ctx is marked for permanent
// deletes.
func isTrashBypassed(ctx context.Context) bool {
	bypassed, _ := ctx.Value(trashBypassKey).(bool)
	return bypassed
}

// trashActive returns true if deleted files should be moved into the
// trash instead of being deleted permanently.
func trashActive(ctx context.Context) bool {
	ci := fs.GetConfig(ctx)
	return ci.Trash && ci.TrashDir != "" && !isTrashBypassed(ctx)
}

// isTrashFs returns true if f is itself rooted at the trash directory,
// in which case deletes must never be moved into the trash again.
func isTrashFs(ctx context.Context, f fs.Info) bool {
	ci := fs.GetConfig(ctx)
	root := strings.Trim(filepath.ToSlash(f.Root()), "/")
	dir := strings.Trim(filepath.ToSlash(ci.TrashDir), "/")
	if dir == "" {
		return false
	}
	// The Fs is rooted at the trash directory or inside it.
	return root == dir ||
		strings.HasSuffix(root, "/"+dir) ||
		strings.HasPrefix(root, dir+"/") ||
		strings.Contains(root, "/"+dir+"/")
}

// trashExcluded returns true if deletes of remote on f must not be
// moved into the trash directory, either because f is rooted at the
// trash directory or because the object lives inside it.
func trashExcluded(ctx context.Context, f fs.Info, remote string) bool {
	ci := fs.GetConfig(ctx)
	if !ci.Trash {
		return false
	}
	dir := strings.Trim(filepath.ToSlash(ci.TrashDir), "/")
	if dir == "" {
		return false
	}
	if isTrashFs(ctx, f) {
		return true
	}
	r := strings.Trim(filepath.ToSlash(remote), "/")
	return r == dir || strings.HasPrefix(r, dir+"/")
}

// trashFilterRule returns the exclude filter rule which hides the
// trash directory from delete/sync walks, or "" when the trash is not
// enabled.
func trashFilterRule(ctx context.Context) string {
	ci := fs.GetConfig(ctx)
	if !ci.Trash {
		return ""
	}
	dir := strings.Trim(ci.TrashDir, "/")
	if dir == "" {
		return ""
	}
	return "- " + dir + "/**"
}

// trashExcludeRules returns the filter rules which hide the trash
// directory (top level and nested) from walks.
func trashExcludeRules(ctx context.Context) []string {
	ci := fs.GetConfig(ctx)
	if !ci.Trash {
		return nil
	}
	dir := strings.Trim(filepath.ToSlash(ci.TrashDir), "/")
	if dir == "" {
		return nil
	}
	return []string{"- " + dir + "/**", "- **/" + dir + "/**"}
}

// TrashFilterCtx returns a ctx with a copy of the current filter with
// rules added which exclude the trash directory. Walks using the
// returned ctx skip the trash directory while still applying the
// user's own filters. It is a no-op when the trash is not enabled.
func TrashFilterCtx(ctx context.Context) context.Context {
	rules := trashExcludeRules(ctx)
	if len(rules) == 0 {
		return ctx
	}
	newCtx, fi := filter.AddConfig(ctx)
	for _, rule := range rules {
		if err := fi.AddRule(rule); err != nil {
			fs.Debugf(nil, "Failed to add trash directory exclude rule: %v", err)
		}
	}
	return newCtx
}

// trashPurgeCtx returns a ctx with an include-all filter which still
// excludes the trash directory (top level and nested). Purge ignores
// user filters but must never purge trash directories.
func trashPurgeCtx(ctx context.Context) context.Context {
	fi, err := filter.NewFilter(nil)
	if err == nil {
		ctx = filter.ReplaceConfig(ctx, fi)
	}
	if fi != nil {
		for _, rule := range trashExcludeRules(ctx) {
			_ = fi.AddRule(rule)
		}
	}
	return ctx
}

// trashInternalCtx returns a ctx for operations inside the trash
// directory: deletes are permanent and the user's filters are replaced
// with an include-all filter so trash contents are listed regardless.
func trashInternalCtx(ctx context.Context) context.Context {
	ctx = trashBypassCtx(ctx)
	if fi, err := filter.NewFilter(nil); err == nil {
		ctx = filter.ReplaceConfig(ctx, fi)
	}
	return ctx
}

// trashFsFor returns the Fs rooted at the trash directory of f.
func trashFsFor(ctx context.Context, f fs.Info) (fs.Fs, error) {
	ci := fs.GetConfig(ctx)
	trashString := fspath.JoinRootPath(fs.ConfigString(f), ci.TrashDir)
	return cache.Get(ctx, trashString)
}

// trashEntryRemote returns the remote within the trash Fs for an
// object deleted at origRemote at time now.
func trashEntryRemote(now time.Time, origRemote string) string {
	dateDir := now.UTC().Format(trashDateLayout)
	entryDir := now.UTC().Format(trashEntryLayout) + "-" + random.String(trashRandomLen)
	return path.Join(dateDir, entryDir, origRemote)
}

// parseTrashEntry returns the deletion time and original path
// (relative to the parent Fs root) for an object stored in the trash,
// or ok=false if the remote does not live inside a trash entry.
func parseTrashEntry(remote string) (delTime time.Time, origRemote string, ok bool) {
	parts := strings.SplitN(strings.Trim(remote, "/"), "/", 3)
	if len(parts) != 3 || parts[2] == "" {
		return time.Time{}, "", false
	}
	dateDir, entryDir, origRemote := parts[0], parts[1], parts[2]
	date, err := time.ParseInLocation(trashDateLayout, dateDir, time.UTC)
	if err != nil {
		return time.Time{}, "", false
	}
	timePart, _, found := strings.Cut(entryDir, "-")
	if !found {
		return time.Time{}, "", false
	}
	clock, err := time.ParseInLocation(trashEntryLayout, timePart, time.UTC)
	if err != nil {
		return time.Time{}, "", false
	}
	delTime = time.Date(date.Year(), date.Month(), date.Day(),
		clock.Hour(), clock.Minute(), clock.Second(), 0, time.UTC)
	return delTime, origRemote, true
}

// TrashItem describes a single object held in the trash directory.
type TrashItem struct {
	OrigPath string    `json:"origPath"` // original path relative to the Fs root
	DelTime  time.Time `json:"delTime"`  // when the object was deleted
	Size     int64     `json:"size"`     // size of the object
	obj      fs.Object // the object in the trash Fs
}

// trashEntry groups the objects of one trash entry (one per deleted
// object) together with their deletion time.
type trashEntry struct {
	dateDir string
	idDir   string
	delTime time.Time
	size    int64
	objs    []fs.Object
}

// trashListEntries lists every object in the trash directory and
// groups them into entries. Objects that don't match the trash layout
// are ignored.
func trashListEntries(ctx context.Context, trashFs fs.Fs) ([]trashEntry, error) {
	grouped := map[string]*trashEntry{}
	listCtx := trashInternalCtx(ctx)
	// Check the trash directory exists before listing recursively
	// so a missing trash directory is treated as empty rather than
	// counted as a listing error.
	_, listErr := trashFs.List(listCtx, "")
	if listErr != nil {
		if errors.Is(listErr, fs.ErrorDirNotFound) {
			return nil, nil
		}
		return nil, listErr
	}
	err := walk.ListR(listCtx, trashFs, "", true, -1, walk.ListObjects, func(inEntries fs.DirEntries) error {
		inEntries.ForObject(func(o fs.Object) {
			delTime, _, ok := parseTrashEntry(o.Remote())
			if !ok {
				return
			}
			parts := strings.SplitN(o.Remote(), "/", 3)
			key := parts[0] + "/" + parts[1]
			entry, found := grouped[key]
			if !found {
				entry = &trashEntry{
					dateDir: parts[0],
					idDir:   parts[1],
					delTime: delTime,
				}
				grouped[key] = entry
			}
			entry.objs = append(entry.objs, o)
			entry.size += o.Size()
		})
		return nil
	})
	if err == fs.ErrorDirNotFound {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	entries := make([]trashEntry, 0, len(grouped))
	for _, e := range grouped {
		entries = append(entries, *e)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].delTime.Before(entries[j].delTime)
	})
	return entries, nil
}

// trashItems turns entries into TrashItem values, one per object.
func trashItems(entries []trashEntry) []TrashItem {
	var items []TrashItem
	for _, entry := range entries {
		for _, o := range entry.objs {
			_, origPath, _ := parseTrashEntry(o.Remote())
			items = append(items, TrashItem{
				OrigPath: origPath,
				DelTime:  entry.delTime,
				Size:     o.Size(),
				obj:      o,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].DelTime.Equal(items[j].DelTime) {
			return items[i].OrigPath < items[j].OrigPath
		}
		return items[i].DelTime.Before(items[j].DelTime)
	})
	return items
}

// trashDeleteObjects permanently deletes the passed objects,
// respecting --dry-run.
func trashDeleteObjects(ctx context.Context, objs []fs.Object) error {
	ci := fs.GetConfig(ctx)
	ch := make(fs.ObjectsChan, ci.Checkers)
	var wg sync.WaitGroup
	var err error
	wg.Add(1)
	go func() {
		defer wg.Done()
		err = DeleteFiles(trashInternalCtx(ctx), ch)
	}()
	for _, o := range objs {
		ch <- o
	}
	close(ch)
	wg.Wait()
	return err
}

// trashCleanupEntries removes the objects of the given entries from
// the trash directory and best-effort removes their now empty
// directories.
func trashCleanupEntries(ctx context.Context, trashFs fs.Fs, entries []trashEntry) (int, int64, error) {
	var objs []fs.Object
	var removed int
	var bytes int64
	seenDirs := map[string]bool{}
	for _, entry := range entries {
		objs = append(objs, entry.objs...)
		removed += len(entry.objs)
		bytes += entry.size
		seenDirs[entry.dateDir+"/"+entry.idDir] = true
		seenDirs[entry.dateDir] = true
	}
	if len(objs) == 0 {
		return 0, 0, nil
	}
	err := trashDeleteObjects(ctx, objs)
	if err != nil {
		return removed, bytes, err
	}
	// Best effort removal of the empty entry/date directories.
	internalCtx := trashInternalCtx(ctx)
	for dir := range seenDirs {
		_ = TryRmdir(internalCtx, trashFs, dir)
	}
	return removed, bytes, nil
}

// TrashList returns all objects currently held in the trash
// directory of f, sorted by deletion time.
func TrashList(ctx context.Context, f fs.Fs) ([]TrashItem, error) {
	trashFs, err := trashFsFor(ctx, f)
	if err != nil {
		return nil, err
	}
	entries, err := trashListEntries(ctx, trashFs)
	if err != nil {
		return nil, err
	}
	return trashItems(entries), nil
}

// trashCleanupFs permanently deletes entries in trashFs older than
// --trash-retention and, if the trash directory exceeds
// --trash-max-size, the oldest entries until it fits. It returns the
// number of objects and bytes removed.
func trashCleanupFs(ctx context.Context, trashFs fs.Fs) (removed int, bytes int64, err error) {
	ci := fs.GetConfig(ctx)
	entries, err := trashListEntries(ctx, trashFs)
	if err != nil {
		return 0, 0, err
	}
	evict := make([]bool, len(entries))
	// Expire entries older than the retention time.
	if ci.TrashRetention > 0 {
		cutoff := time.Now().Add(-time.Duration(ci.TrashRetention))
		for i := range entries {
			if entries[i].delTime.Before(cutoff) {
				evict[i] = true
			}
		}
	}
	// If over the size limit, evict the oldest entries first.
	if ci.TrashMaxSize >= 0 {
		var total int64
		for i := range entries {
			if !evict[i] {
				total += entries[i].size
			}
		}
		limit := int64(ci.TrashMaxSize)
		for i := range entries {
			if total <= limit {
				break
			}
			if !evict[i] {
				evict[i] = true
				total -= entries[i].size
			}
		}
	}
	var toEvict []trashEntry
	for i := range entries {
		if evict[i] {
			toEvict = append(toEvict, entries[i])
		}
	}
	return trashCleanupEntries(ctx, trashFs, toEvict)
}

// TrashCleanup permanently deletes trash entries of f older than
// --trash-retention and, if the trash directory exceeds
// --trash-max-size, the oldest entries until it fits. It returns the
// number of objects and bytes removed.
func TrashCleanup(ctx context.Context, f fs.Fs) (removed int, bytes int64, err error) {
	trashFs, err := trashFsFor(ctx, f)
	if err != nil {
		return 0, 0, err
	}
	return trashCleanupFs(ctx, trashFs)
}

// TrashEmpty permanently deletes everything in the trash directory of
// f and removes the empty directories left behind. It returns the
// number of objects and bytes removed. A missing trash directory is
// treated as empty.
func TrashEmpty(ctx context.Context, f fs.Fs) (removed int, bytes int64, err error) {
	trashFs, err := trashFsFor(ctx, f)
	if err != nil {
		return 0, 0, err
	}
	internalCtx := trashInternalCtx(ctx)
	// Nothing to do if the trash directory does not exist yet.
	if _, listErr := trashFs.List(internalCtx, ""); listErr != nil {
		if errors.Is(listErr, fs.ErrorDirNotFound) {
			return 0, 0, nil
		}
		return 0, 0, listErr
	}
	// Delete every object in the trash, including files which don't
	// follow the trash layout (e.g. files placed there manually).
	var objs []fs.Object
	err = walk.ListR(internalCtx, trashFs, "", true, -1, walk.ListObjects, func(inEntries fs.DirEntries) error {
		inEntries.ForObject(func(o fs.Object) {
			objs = append(objs, o)
		})
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrorDirNotFound) {
		return 0, 0, err
	}
	for _, o := range objs {
		bytes += o.Size()
	}
	if len(objs) > 0 {
		ch := make(fs.ObjectsChan, fs.GetConfig(ctx).Checkers)
		var wg sync.WaitGroup
		var delErr error
		wg.Add(1)
		go func() {
			defer wg.Done()
			delErr = DeleteFiles(internalCtx, ch)
		}()
		for _, o := range objs {
			ch <- o
		}
		close(ch)
		wg.Wait()
		if delErr != nil {
			return len(objs), bytes, delErr
		}
		removed = len(objs)
	}
	// Best effort removal of the directories left empty.
	if err = Rmdirs(internalCtx, trashFs, "", true); err != nil {
		fs.Debugf(trashFs, "Failed to remove empty trash directories: %v", err)
	}
	return removed, bytes, nil
}

// TrashRestore moves trashed objects back to their original paths.
//
// With all set, or when no patterns are supplied, every entry is
// restored; otherwise only entries whose original path matches one of
// the (rsync style glob) patterns are restored. If a file already
// exists at the original path it is left in the trash and reported as
// skipped, unless overwrite is set in which case the existing file is
// replaced permanently. It returns the number of restored and skipped
// objects.
func TrashRestore(ctx context.Context, f fs.Fs, patterns []string, all, overwrite bool) (restored, skipped int, err error) {
	trashFs, err := trashFsFor(ctx, f)
	if err != nil {
		return 0, 0, err
	}
	entries, err := trashListEntries(ctx, trashFs)
	if err != nil {
		return 0, 0, err
	}
	items := trashItems(entries)
	var matchers []func(string) bool
	if len(patterns) > 0 && !all {
		for _, pattern := range patterns {
			re, err := filter.GlobPathToRegexp(pattern, false)
			if err != nil {
				return restored, skipped, fmt.Errorf("bad pattern %q: %w", pattern, err)
			}
			matchers = append(matchers, re.MatchString)
		}
	}
	internalCtx := trashInternalCtx(ctx)
	var cleanedDirs = map[string]bool{}
	for _, item := range items {
		if len(matchers) > 0 {
			matched := false
			for _, match := range matchers {
				if match(item.OrigPath) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		// A file already at the original path is skipped unless
		// overwrite is set, in which case it is deleted permanently.
		existing, err := f.NewObject(ctx, item.OrigPath)
		if err == nil && existing != nil {
			if !overwrite {
				skipped++
				fs.Infof(existing, "Not restoring from trash as file exists already")
				continue
			}
			err = DeleteFile(internalCtx, existing)
			if err != nil {
				fs.Errorf(existing, "Failed to remove existing file before trash restore: %v", err)
				continue
			}
		} else if err != nil && err != fs.ErrorObjectNotFound {
			fs.Errorf(item.obj, "Failed to check restore target: %v", err)
			continue
		}
		_, err = Move(internalCtx, f, nil, item.OrigPath, item.obj)
		if err != nil {
			fs.Errorf(item.obj, "Failed to restore from trash: %v", err)
			continue
		}
		restored++
		fs.Infof(item.obj, "Restored from trash")
		// Best effort cleanup of the now empty entry directories.
		parts := strings.SplitN(item.obj.Remote(), "/", 3)
		if len(parts) == 3 {
			cleanedDirs[parts[0]+"/"+parts[1]] = true
			cleanedDirs[parts[0]] = true
		}
	}
	for dir := range cleanedDirs {
		_ = TryRmdir(internalCtx, trashFs, dir)
	}
	return restored, skipped, nil
}

// trashMove moves dst into the trash directory of its Fs.
func trashMove(ctx context.Context, dst fs.Object) error {
	ci := fs.GetConfig(ctx)
	trashFs, err := trashFsFor(ctx, dst.Fs())
	if err != nil {
		return fserrors.FatalError(fmt.Errorf("failed to make fs for trash dir %q: %w", ci.TrashDir, err))
	}
	trashAutoCleanup(ctx, dst.Fs())
	entryRemote := trashEntryRemote(time.Now(), dst.Remote())
	// The move and any deletes it does must be permanent rather than
	// re-trashing the files.
	_, err = Move(trashBypassCtx(ctx), trashFs, nil, entryRemote, dst)
	if err != nil {
		return err
	}
	return nil
}

// Automatic trash cleanup throttling state, keyed by Fs config string.
var (
	trashCleanupMu   sync.Mutex
	trashLastCleanup = map[string]time.Time{}
)

// trashAutoCleanup runs TrashCleanup at most once per
// --trash-cleanup-interval for each Fs in a process. Failures are
// logged but do not stop the deletion.
func trashAutoCleanup(ctx context.Context, f fs.Info) {
	ci := fs.GetConfig(ctx)
	if ci.TrashCleanupInterval <= 0 {
		return
	}
	key := fs.ConfigString(f)
	trashCleanupMu.Lock()
	last := trashLastCleanup[key]
	now := time.Now()
	do := last.IsZero() || now.Sub(last) >= time.Duration(ci.TrashCleanupInterval)
	if do {
		trashLastCleanup[key] = now
	}
	trashCleanupMu.Unlock()
	if !do {
		return
	}
	trashFs, err := trashFsFor(ctx, f)
	if err != nil {
		fs.Debugf(f, "Automatic trash cleanup failed: %v", err)
		return
	}
	removed, bytes, err := trashCleanupFs(ctx, trashFs)
	if err != nil {
		fs.Debugf(f, "Automatic trash cleanup failed: %v", err)
		return
	}
	if removed > 0 {
		fs.Infof(f, "Trash automatic cleanup removed %d files (%v)", removed, fs.SizeSuffix(bytes))
	}
}

// rcTrashList handles the operations/trashlist call
func rcTrashList(ctx context.Context, in rc.Params) (out rc.Params, err error) {
	f, err := rc.GetFs(ctx, in)
	if err != nil {
		return nil, err
	}
	items, err := TrashList(ctx, f)
	if err != nil {
		return nil, err
	}
	out = rc.Params{
		"items": items,
	}
	return out, nil
}

// rcTrashRestore handles the operations/trashrestore call
func rcTrashRestore(ctx context.Context, in rc.Params) (out rc.Params, err error) {
	f, err := rc.GetFs(ctx, in)
	if err != nil {
		return nil, err
	}
	all, err := in.GetBool("all")
	if rc.NotErrParamNotFound(err) {
		return nil, err
	}
	overwrite, err := in.GetBool("overwrite")
	if rc.NotErrParamNotFound(err) {
		return nil, err
	}
	var patterns []string
	if v, ok := in["patterns"]; ok && v != nil {
		switch x := v.(type) {
		case []string:
			patterns = x
		case []any:
			for _, item := range x {
				if s, ok := item.(string); ok {
					patterns = append(patterns, s)
				}
			}
		case string:
			patterns = []string{x}
		default:
			return nil, fmt.Errorf("patterns must be an array of strings")
		}
	}
	restored, skipped, err := TrashRestore(ctx, f, patterns, all, overwrite)
	if err != nil {
		return nil, err
	}
	out = rc.Params{
		"restored": restored,
		"skipped":  skipped,
	}
	return out, nil
}

// rcTrashCleanup handles the operations/trashcleanup call
func rcTrashCleanup(ctx context.Context, in rc.Params) (out rc.Params, err error) {
	f, err := rc.GetFs(ctx, in)
	if err != nil {
		return nil, err
	}
	removed, bytes, err := TrashCleanup(ctx, f)
	if err != nil {
		return nil, err
	}
	out = rc.Params{
		"removed": removed,
		"bytes":   bytes,
	}
	return out, nil
}

// rcTrashEmpty handles the operations/trashempty call
func rcTrashEmpty(ctx context.Context, in rc.Params) (out rc.Params, err error) {
	f, err := rc.GetFs(ctx, in)
	if err != nil {
		return nil, err
	}
	removed, bytes, err := TrashEmpty(ctx, f)
	if err != nil {
		return nil, err
	}
	out = rc.Params{
		"removed": removed,
		"bytes":   bytes,
	}
	return out, nil
}

func init() {
	rc.Add(rc.Call{
		Path:  "operations/trashlist",
		Fn:    rcTrashList,
		Title: "List the files in the trash directory",
		Help: `This takes the following parameters:

- fs - a remote name string e.g. "drive:"

This lists the files held in the trash directory of the remote, see
the [trash list](/commands/rclone_trash_list/) command for more
information.

Returns:

- items - array of objects with "origPath", "delTime" and "size" keys
`,
	})
	rc.Add(rc.Call{
		Path:  "operations/trashrestore",
		Fn:    rcTrashRestore,
		Title: "Restore files from the trash directory to their original paths",
		Help: `This takes the following parameters:

- fs - a remote name string e.g. "drive:"
- all - boolean, restore all files (default: true if no patterns given)
- overwrite - boolean, overwrite existing files at the original paths
- patterns - array of glob patterns matching the original paths to restore

See the [trash restore](/commands/rclone_trash_restore/) command for
more information.

Returns:

- restored - number of files restored
- skipped - number of files skipped because a file exists at the path
`,
	})
	rc.Add(rc.Call{
		Path:  "operations/trashcleanup",
		Fn:    rcTrashCleanup,
		Title: "Permanently delete old and over-quota files from the trash directory",
		Help: `This takes the following parameters:

- fs - a remote name string e.g. "drive:"

This applies the --trash-retention and --trash-max-size limits to the
trash directory. See the [trash cleanup](/commands/rclone_trash_cleanup/)
command for more information.

Returns:

- removed - number of files deleted permanently
- bytes - number of bytes freed
`,
	})
	rc.Add(rc.Call{
		Path:  "operations/trashempty",
		Fn:    rcTrashEmpty,
		Title: "Permanently delete all files from the trash directory",
		Help: `This takes the following parameters:

- fs - a remote name string e.g. "drive:"

This empties the trash directory completely. See the
[trash empty](/commands/rclone_trash_empty/) command for more
information.

Returns:

- removed - number of files deleted permanently
- bytes - number of bytes freed
`,
	})
}
