package filecopy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
)

type Progress struct {
	Phase       string
	CurrentFile string
	BytesCopied int64
	TotalBytes  int64
	FilesCopied int
	TotalFiles  int
}

type Reporter func(Progress)

type FileSystem struct {
	ID     string
	Local  bool
	remote *sftp.Client
}

func LocalFileSystem() *FileSystem {
	return &FileSystem{ID: "local", Local: true}
}

func RemoteFileSystem(hostName string, client *sftp.Client) *FileSystem {
	return &FileSystem{ID: "remote:" + hostName, remote: client}
}

// Complete returns path candidates matching the final component of value.
func (f *FileSystem) Complete(ctx context.Context, value string) ([]string, error) {
	directory, prefix, displayDirectory, separator := f.completionParts(value)
	normalizedDirectory, err := f.normalize(directory)
	if err != nil {
		return nil, err
	}
	entries, err := f.readDir(ctx, normalizedDirectory)
	if err != nil {
		return nil, err
	}
	candidates := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasPrefix(strings.ToLower(entry.Name()), strings.ToLower(prefix)) {
			continue
		}
		candidate := displayDirectory + entry.Name()
		if entry.IsDir() {
			candidate += separator
		}
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return strings.ToLower(candidates[i]) < strings.ToLower(candidates[j])
	})
	return candidates, nil
}

type Copier struct {
	Source      *FileSystem
	Destination *FileSystem
}

type manifestItem struct {
	sourcePath string
	relative   string
	info       os.FileInfo
}

func (c Copier) Copy(ctx context.Context, sourcePath, destinationPath string, report Reporter) error {
	if c.Source == nil || c.Destination == nil {
		return errors.New("source and destination are required")
	}
	sourcePath, err := c.Source.normalize(sourcePath)
	if err != nil {
		return fmt.Errorf("source path: %w", err)
	}
	destinationPath, err = c.Destination.normalize(destinationPath)
	if err != nil {
		return fmt.Errorf("destination path: %w", err)
	}
	sourceInfo, err := c.Source.stat(sourcePath)
	if err != nil {
		return fmt.Errorf("stat source %s: %w", sourcePath, err)
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("symbolic-link sources are not supported")
	}
	targetPath, err := resolveTarget(c.Source, c.Destination, sourcePath, destinationPath, sourceInfo)
	if err != nil {
		return err
	}
	if err := validateTarget(c.Source, c.Destination, sourcePath, targetPath, sourceInfo.IsDir()); err != nil {
		return err
	}

	progress := Progress{Phase: "Scanning", CurrentFile: sourcePath}
	notify(report, progress)
	manifest, err := c.scan(ctx, sourcePath, sourceInfo, &progress, report)
	if err != nil {
		return err
	}
	progress.Phase = "Copying"
	notify(report, progress)

	var directories []manifestItem
	for _, item := range manifest {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := targetPath
		if item.relative != "." {
			target = c.Destination.join(targetPath, item.relative)
		}
		progress.CurrentFile = item.relative
		notify(report, progress)
		if item.info.IsDir() {
			if err := c.Destination.mkdirAll(target); err != nil {
				return fmt.Errorf("create directory %s: %w", target, err)
			}
			directories = append(directories, manifestItem{sourcePath: target, info: item.info})
			continue
		}
		if err := c.copyFile(ctx, item.sourcePath, target, item.info, &progress, report); err != nil {
			return err
		}
	}
	for index := len(directories) - 1; index >= 0; index-- {
		_ = c.Destination.chmod(directories[index].sourcePath, directories[index].info.Mode().Perm())
		_ = c.Destination.chtimes(directories[index].sourcePath, directories[index].info.ModTime())
	}
	progress.Phase = "Complete"
	progress.CurrentFile = ""
	notify(report, progress)
	return nil
}

func (c Copier) scan(ctx context.Context, sourcePath string, sourceInfo os.FileInfo, progress *Progress, report Reporter) ([]manifestItem, error) {
	manifest := []manifestItem{{sourcePath: sourcePath, relative: ".", info: sourceInfo}}
	if !sourceInfo.IsDir() {
		if !sourceInfo.Mode().IsRegular() {
			return nil, fmt.Errorf("unsupported source type: %s", sourceInfo.Mode())
		}
		progress.TotalFiles = 1
		progress.TotalBytes = sourceInfo.Size()
		notify(report, *progress)
		return manifest, nil
	}

	var walk func(string, string) error
	walk = func(directory, relative string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := c.Source.readDir(ctx, directory)
		if err != nil {
			return fmt.Errorf("read directory %s: %w", directory, err)
		}
		for _, entry := range entries {
			if entry.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symbolic link %s is not supported", c.Source.join(directory, entry.Name()))
			}
			childPath := c.Source.join(directory, entry.Name())
			childRelative := entry.Name()
			if relative != "." {
				childRelative = c.Source.join(relative, entry.Name())
			}
			manifest = append(manifest, manifestItem{sourcePath: childPath, relative: childRelative, info: entry})
			if entry.IsDir() {
				if err := walk(childPath, childRelative); err != nil {
					return err
				}
			} else if entry.Mode().IsRegular() {
				progress.TotalFiles++
				progress.TotalBytes += entry.Size()
				progress.CurrentFile = childRelative
				notify(report, *progress)
			} else {
				return fmt.Errorf("unsupported file type %s: %s", childPath, entry.Mode())
			}
		}
		return nil
	}
	if err := walk(sourcePath, "."); err != nil {
		return nil, err
	}
	return manifest, nil
}

func (c Copier) copyFile(ctx context.Context, sourcePath, targetPath string, info os.FileInfo, progress *Progress, report Reporter) error {
	if err := c.Destination.mkdirAll(c.Destination.dir(targetPath)); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	source, err := c.Source.open(sourcePath)
	if err != nil {
		return fmt.Errorf("open source %s: %w", sourcePath, err)
	}
	defer source.Close()
	temporary := c.Destination.temporaryName(targetPath)
	destination, err := c.Destination.create(temporary)
	if err != nil {
		return fmt.Errorf("create destination %s: %w", targetPath, err)
	}
	completed := false
	defer func() {
		_ = destination.Close()
		if !completed {
			_ = c.Destination.remove(temporary)
		}
	}()

	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			if _, err := destination.Write(buffer[:count]); err != nil {
				return fmt.Errorf("write destination %s: %w", targetPath, err)
			}
			progress.BytesCopied += int64(count)
			notify(report, *progress)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read source %s: %w", sourcePath, readErr)
		}
	}
	if err := destination.Close(); err != nil {
		return fmt.Errorf("close destination %s: %w", targetPath, err)
	}
	if err := c.Destination.chmod(temporary, info.Mode().Perm()); err != nil {
		return fmt.Errorf("set permissions on %s: %w", targetPath, err)
	}
	if err := c.Destination.renameReplace(temporary, targetPath); err != nil {
		return fmt.Errorf("replace destination %s: %w", targetPath, err)
	}
	_ = c.Destination.chtimes(targetPath, info.ModTime())
	completed = true
	progress.FilesCopied++
	notify(report, *progress)
	return nil
}

func resolveTarget(source, destination *FileSystem, sourcePath, destinationPath string, sourceInfo os.FileInfo) (string, error) {
	destinationInfo, err := destination.stat(destinationPath)
	destinationExists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("stat destination %s: %w", destinationPath, err)
	}
	if sourceInfo.IsDir() {
		if destinationExists {
			if !destinationInfo.IsDir() {
				return "", errors.New("cannot copy a directory onto a file")
			}
			return destination.join(destinationPath, source.base(sourcePath)), nil
		}
		return destinationPath, nil
	}
	if destinationExists && destinationInfo.IsDir() {
		return destination.join(destinationPath, source.base(sourcePath)), nil
	}
	return destinationPath, nil
}

func validateTarget(source, destination *FileSystem, sourcePath, targetPath string, directory bool) error {
	if source.ID != destination.ID {
		return nil
	}
	if source.clean(sourcePath) == destination.clean(targetPath) {
		return errors.New("source and destination are the same path")
	}
	if directory && source.contains(sourcePath, targetPath) {
		return errors.New("destination cannot be inside the source directory")
	}
	return nil
}

func (f *FileSystem) normalize(value string) (string, error) {
	if f.Local {
		if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			if value == "~" {
				value = home
			} else {
				value = filepath.Join(home, value[2:])
			}
		}
		return filepath.Abs(value)
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := f.remote.Getwd()
		if err != nil {
			return "", err
		}
		if value == "~" {
			value = home
		} else {
			value = path.Join(home, value[2:])
		}
	}
	return path.Clean(value), nil
}

func (f *FileSystem) completionParts(value string) (directory, prefix, displayDirectory, separator string) {
	if f.Local {
		separator = string(filepath.Separator)
		if value == "" {
			return ".", "", "", separator
		}
		if strings.HasSuffix(value, "/") || strings.HasSuffix(value, `\`) {
			return value, "", value, separator
		}
		directory = filepath.Dir(value)
		prefix = filepath.Base(value)
		if directory == "." {
			if strings.HasPrefix(value, "."+separator) {
				displayDirectory = "." + separator
			}
			return directory, prefix, displayDirectory, separator
		}
		return directory, prefix, value[:len(value)-len(prefix)], separator
	}
	separator = "/"
	if value == "" {
		return ".", "", "", separator
	}
	if strings.HasSuffix(value, separator) {
		return value, "", value, separator
	}
	directory = path.Dir(value)
	prefix = path.Base(value)
	if directory == "." {
		if strings.HasPrefix(value, "./") {
			displayDirectory = "./"
		}
		return directory, prefix, displayDirectory, separator
	}
	return directory, prefix, value[:len(value)-len(prefix)], separator
}

func (f *FileSystem) stat(name string) (os.FileInfo, error) {
	if f.Local {
		return os.Lstat(name)
	}
	return f.remote.Lstat(name)
}

func (f *FileSystem) readDir(ctx context.Context, name string) ([]os.FileInfo, error) {
	if f.Local {
		entries, err := os.ReadDir(name)
		if err != nil {
			return nil, err
		}
		result := make([]os.FileInfo, 0, len(entries))
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			info, err := entry.Info()
			if err != nil {
				return nil, err
			}
			result = append(result, info)
		}
		return result, nil
	}
	return f.remote.ReadDirContext(ctx, name)
}

func (f *FileSystem) open(name string) (io.ReadCloser, error) {
	if f.Local {
		return os.Open(name)
	}
	return f.remote.Open(name)
}

func (f *FileSystem) create(name string) (io.WriteCloser, error) {
	if f.Local {
		return os.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	}
	return f.remote.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
}

func (f *FileSystem) mkdirAll(name string) error {
	if name == "" || name == "." || name == "/" {
		return nil
	}
	if f.Local {
		return os.MkdirAll(name, 0o755)
	}
	return f.remote.MkdirAll(name)
}

func (f *FileSystem) chmod(name string, mode os.FileMode) error {
	if f.Local {
		return os.Chmod(name, mode)
	}
	return f.remote.Chmod(name, mode)
}

func (f *FileSystem) chtimes(name string, modified time.Time) error {
	if f.Local {
		return os.Chtimes(name, modified, modified)
	}
	return f.remote.Chtimes(name, modified, modified)
}

func (f *FileSystem) remove(name string) error {
	if f.Local {
		return os.Remove(name)
	}
	return f.remote.Remove(name)
}

func (f *FileSystem) renameReplace(oldName, newName string) error {
	if f.Local {
		if _, err := os.Lstat(newName); err == nil {
			if err := os.Remove(newName); err != nil {
				return err
			}
		}
		return os.Rename(oldName, newName)
	}
	if err := f.remote.PosixRename(oldName, newName); err == nil {
		return nil
	}
	if _, err := f.remote.Lstat(newName); err == nil {
		if err := f.remote.Remove(newName); err != nil {
			return err
		}
	}
	return f.remote.Rename(oldName, newName)
}

func (f *FileSystem) join(parts ...string) string {
	if f.Local {
		return filepath.Join(parts...)
	}
	return path.Join(parts...)
}

func (f *FileSystem) dir(name string) string {
	if f.Local {
		return filepath.Dir(name)
	}
	return path.Dir(name)
}

func (f *FileSystem) base(name string) string {
	if f.Local {
		return filepath.Base(name)
	}
	return path.Base(name)
}

func (f *FileSystem) clean(name string) string {
	if f.Local {
		name = filepath.Clean(name)
		if runtime.GOOS == "windows" {
			name = strings.ToLower(name)
		}
		return name
	}
	return path.Clean(name)
}

func (f *FileSystem) contains(parent, child string) bool {
	if f.Local {
		relative, err := filepath.Rel(parent, child)
		return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	parent = strings.TrimSuffix(path.Clean(parent), "/") + "/"
	return strings.HasPrefix(path.Clean(child)+"/", parent)
}

func (f *FileSystem) temporaryName(target string) string {
	random := make([]byte, 6)
	_, _ = rand.Read(random)
	return f.join(f.dir(target), ".xsh-part-"+f.base(target)+"-"+hex.EncodeToString(random))
}

func notify(report Reporter, progress Progress) {
	if report != nil {
		report(progress)
	}
}
