// Package trash provides the trash command.
package trash

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rclone/rclone/cmd"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/flags"
	"github.com/rclone/rclone/fs/operations"
	"github.com/spf13/cobra"
)

// Options
var (
	listJSON         bool
	restoreAll       bool
	restoreOverwrite bool
)

func init() {
	cmd.Root.AddCommand(commandDefinition)
	commandDefinition.AddCommand(listCommand, restoreCommand, cleanupCommand, emptyCommand)

	listFlags := listCommand.Flags()
	flags.BoolVarP(listFlags, &listJSON, "json", "", false, "Output in JSON format", "")

	restoreFlags := restoreCommand.Flags()
	flags.BoolVarP(restoreFlags, &restoreAll, "all", "", false, "Restore all files in the trash directory (default if no patterns are given)", "")
	flags.BoolVarP(restoreFlags, &restoreOverwrite, "overwrite", "", false, "Overwrite existing files when restoring", "")
}

var commandDefinition = &cobra.Command{
	Use:   "trash <command> remote:path [opts] [pattern...]",
	Short: `Manage the trash directory.`,
	Long: strings.ReplaceAll(`The trash command group manages the trash directory.

When the |--trash| flag is set, deleted files are moved into a trash
directory under the root of the remote (|.rclone-trash| by default,
configurable with |--trash-dir|) instead of being deleted permanently.
The trash directory is excluded from delete and sync walks
automatically so files moved there are not deleted again.

Files are kept in the trash directory for |--trash-retention| (7 days by
default) and the trash directory is limited by |--trash-max-size|
(unlimited by default); files are deleted permanently, oldest first,
when either limit is exceeded. Cleanup happens automatically (throttled
by |--trash-cleanup-interval|, 1h by default) or can be run explicitly
with |rclone trash cleanup|.

The subcommands are:

- |rclone trash list| - show files in the trash directory
- |rclone trash restore| - restore files from the trash directory
- |rclone trash cleanup| - apply the retention and size limits now
- |rclone trash empty| - delete everything in the trash directory

Deleting files in the trash directory itself (e.g. with |rclone delete
remote:.rclone-trash|) deletes them permanently.

Note that bucket based remotes (e.g. s3) need a path rooted in a bucket
for the trash directory to be created in.

|rclone sync| deletions are moved into the trash as well; the
|--backup-dir| flag takes precedence if both are set. Deletions done as
part of |rclone move| are permanent as the files only changed location.`, "|", "`"),
	Annotations: map[string]string{
		"versionIntroduced": "v1.77",
		"groups":            "Important",
	},
	Run: func(command *cobra.Command, args []string) {
		_ = command.Help()
	},
}

var listCommand = &cobra.Command{
	Use:   "list remote:path",
	Short: `List the files in the trash directory.`,
	Long: strings.ReplaceAll(`Lists the files currently held in the trash directory of the remote.

Each file is shown with the time it was deleted, its size and its
original path relative to the remote root. Use |--json| for machine
readable output.`, "|", "`"),
	Annotations: map[string]string{
		"versionIntroduced": "v1.77",
	},
	RunE: func(command *cobra.Command, args []string) error {
		cmd.CheckArgs(1, 1, command, args)
		f := cmd.NewFsSrc(args)
		cmd.Run(true, false, command, func() error {
			items, err := operations.TrashList(context.Background(), f)
			if err != nil {
				return err
			}
			if listJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "\t")
				return enc.Encode(items)
			}
			operations.StdoutMutex.Lock()
			defer operations.StdoutMutex.Unlock()
			for _, item := range items {
				fmt.Printf("%s  %10d  %s\n", item.DelTime.Local().Format("2006-01-02 15:04:05"), item.Size, item.OrigPath)
			}
			return nil
		})
		return nil
	},
}

var restoreCommand = &cobra.Command{
	Use:   "restore remote:path [pattern...]",
	Short: `Restore files from the trash directory to their original paths.`,
	Long: strings.ReplaceAll(`Restores files from the trash directory to the paths they were
deleted from.

By default all files in the trash directory are restored (one click
restore). If pattern arguments are supplied only files whose original
paths match one of the rsync style glob patterns are restored, e.g.

|rclone trash restore remote:path "photos/2026/**"|

Files whose original path already exists are left in the trash
directory and reported as skipped. With |--overwrite| the existing files
are replaced permanently.

This command respects |--dry-run| and |-i|/|--interactive|.`, "|", "`"),
	Annotations: map[string]string{
		"versionIntroduced": "v1.77",
	},
	RunE: func(command *cobra.Command, args []string) error {
		cmd.CheckArgs(1, 1e6, command, args)
		f := cmd.NewFsSrc(args)
		patterns := args[1:]
		cmd.Run(true, false, command, func() error {
			restored, skipped, err := operations.TrashRestore(context.Background(), f, patterns, restoreAll, restoreOverwrite)
			if err != nil {
				return err
			}
			fs.Logf(nil, "Restored %d files from trash, skipped %d", restored, skipped)
			return nil
		})
		return nil
	},
}

var cleanupCommand = &cobra.Command{
	Use:   "cleanup remote:path",
	Short: `Permanently delete old and over-quota files from the trash directory.`,
	Long: strings.ReplaceAll(`Applies the |--trash-retention| and |--trash-max-size| limits to the
trash directory immediately: files deleted longer ago than the
retention time are deleted permanently, and if the trash directory is
still larger than |--trash-max-size| the oldest files are deleted
permanently until it fits.

This command respects |--dry-run| and |-i|/|--interactive|.`, "|", "`"),
	Annotations: map[string]string{
		"versionIntroduced": "v1.77",
	},
	RunE: func(command *cobra.Command, args []string) error {
		cmd.CheckArgs(1, 1, command, args)
		f := cmd.NewFsSrc(args)
		cmd.Run(true, false, command, func() error {
			removed, bytes, err := operations.TrashCleanup(context.Background(), f)
			if err != nil {
				return err
			}
			fs.Logf(nil, "Trash cleanup removed %d files (%v)", removed, fs.SizeSuffix(bytes))
			return nil
		})
		return nil
	},
}

var emptyCommand = &cobra.Command{
	Use:   "empty remote:path",
	Short: `Permanently delete all files from the trash directory.`,
	Long: strings.ReplaceAll(`Deletes every file in the trash directory permanently, regardless of
the |--trash-retention| and |--trash-max-size| settings.

This command respects |--dry-run| and |-i|/|--interactive|.`, "|", "`"),
	Annotations: map[string]string{
		"versionIntroduced": "v1.77",
	},
	RunE: func(command *cobra.Command, args []string) error {
		cmd.CheckArgs(1, 1, command, args)
		f := cmd.NewFsSrc(args)
		cmd.Run(true, false, command, func() error {
			removed, bytes, err := operations.TrashEmpty(context.Background(), f)
			if err != nil {
				return err
			}
			fs.Logf(nil, "Trash emptied: removed %d files (%v)", removed, fs.SizeSuffix(bytes))
			return nil
		})
		return nil
	},
}
