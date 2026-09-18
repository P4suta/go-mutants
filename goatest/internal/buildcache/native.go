// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package buildcache

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/P4suta/go-mutants/goatest/internal/filemode"
)

const (
	nativeCacheIdentifierBytes = 32
	nativeCachePrefixCount     = 1 << 8
	nativeCachePrefixHexDigits = 2
	nativeActionFieldCount     = 5
	nativeActionFormat         = "v1"
	nativeActionDecimalRadix   = 10
	nativeActionIntegerBits    = 64
	nativeActionDecimalWidth   = 20
)

const (
	nativeActionFormatField = iota
	nativeActionKeyField
	nativeActionOutputField
	nativeActionSizeField
	nativeActionTimestampField
)

const (
	NativeDirectoryPrefix = "goatest-native-cache-"

	NativeSharedDirectoryPrefix = "goatest-native-shared-"

	nativeSharedNameHexDigits = 16
)

func NativeSharedDirectoryName(base string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(base)))
	return NativeSharedDirectoryPrefix + hex.EncodeToString(sum[:])[:nativeSharedNameHexDigits]
}

const NativeCollectInterval = time.Minute

type NativeSeed struct {
	Actions int
	Objects int
	Bytes   int64
	Skipped int
	actions map[string]bool
}

type NativePersisted struct {
	Actions  int
	Objects  int
	Bytes    int64
	Skipped  int
	Deferred bool
}

type NativeCollected struct {
	BeforeBytes    int64
	AfterBytes     int64
	RemovedObjects int
	RemovedActions int
	RemovedBytes   int64
}

func (layers Layers) importNative(actionID []byte, now time.Time, hooks layerHooks) (Entry, bool, error) {
	if layers.NativeSource == "" || len(actionID) != nativeCacheIdentifierBytes {
		return Entry{}, false, nil
	}
	actionName := hex.EncodeToString(actionID)
	action, valid := readNativeAction(nativeCachePath(layers.NativeSource, actionName, "a"), actionName, hooks)
	if !valid {
		return Entry{}, false, nil
	}
	outputID, valid := nativeIdentifier(action.output)
	if !valid {
		return Entry{}, false, nil
	}
	for _, holder := range layers.holders() {
		path, stored, err := layers.layer(holder).object(outputID, hooks)
		if err != nil {
			return Entry{}, false, err
		}
		if path != "" && stored == action.size {
			entry, err := layers.target().putAction(actionID, outputID, action.size, now, path, hooks)
			return entry, err == nil, err
		}
	}
	object, valid := openNativeObject(nativeCachePath(layers.NativeSource, action.output, "d"), action.size)
	if !valid {
		return Entry{}, false, nil
	}
	defer func() { _ = object.Close() }()
	verified := &verifiedNativeReader{source: object, digest: sha256.New(), expected: slices.Clone(outputID)}
	entry, err := layers.target().putWithHooks(actionID, outputID, verified, action.size, now, hooks)
	if verified.invalid {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	return entry, true, nil
}

func openNativeObject(path string, size int64) (*os.File, bool) {
	linked, err := os.Lstat(path)
	if err != nil || !linked.Mode().IsRegular() || linked.Size() != size {
		return nil, false
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() != size || !os.SameFile(linked, opened) {
		_ = file.Close()
		return nil, false
	}
	return file, true
}

type verifiedNativeReader struct {
	source   io.Reader
	digest   hash.Hash
	expected []byte
	invalid  bool
}

func (reader *verifiedNativeReader) Read(destination []byte) (int, error) {
	read, err := reader.source.Read(destination)
	if read > 0 {
		_, _ = reader.digest.Write(destination[:read])
	}
	if err == nil {
		return read, nil
	}
	if errors.Is(err, io.EOF) && bytes.Equal(reader.digest.Sum(nil), reader.expected) {
		return read, io.EOF
	}
	reader.invalid = true
	return read, fmt.Errorf("goatest: invalid native build cache object: %w", err)
}

func SeedNative(base, destination string, now time.Time) (NativeSeed, error) {
	return projectNative(base, destination, now, false, layerHooks{})
}

func RefreshNative(base, destination string, now time.Time) (NativeSeed, error) {
	return projectNative(base, destination, now, true, layerHooks{})
}

func projectNative(base, destination string, now time.Time, repair bool, hooks layerHooks) (NativeSeed, error) {
	if base == "" || destination == "" {
		return NativeSeed{}, errors.New("goatest: native build cache seed requires source and destination")
	}
	basePath, err := filepath.Abs(base)
	if err != nil {
		return NativeSeed{}, fmt.Errorf("goatest: resolve native build cache source: %w", err)
	}
	destinationPath, err := filepath.Abs(destination)
	if err != nil {
		return NativeSeed{}, fmt.Errorf("goatest: resolve native build cache destination: %w", err)
	}
	if basePath == destinationPath {
		return NativeSeed{}, errors.New("goatest: native build cache source and destination are the same directory")
	}
	hooks = hooks.resolved()
	if err := prepareNativeCache(destinationPath, now, hooks); err != nil {
		return NativeSeed{}, err
	}
	actions, _, err := (Layer{Dir: basePath}).list(hooks)
	if err != nil {
		return NativeSeed{}, err
	}
	seed := NativeSeed{actions: make(map[string]bool, len(actions))}
	linked := make(map[string]bool)
	for _, action := range actions {
		_, actionOK := nativeIdentifier(action.name)
		outputID, outputOK := nativeIdentifier(action.output)
		if !actionOK || !outputOK || action.size < 0 {
			seed.Skipped++
			continue
		}
		source, size, objectErr := (Layer{Dir: basePath}).object(outputID, hooks)
		if objectErr != nil {
			return NativeSeed{}, objectErr
		}
		if source == "" || size != action.size {
			seed.Skipped++
			continue
		}
		outputPath := nativeCachePath(destinationPath, action.output, "d")
		if !linked[action.output] {
			if err := linkNativeObject(source, outputPath, size, repair, hooks); err != nil {
				return NativeSeed{}, err
			}
			seed.Objects++
			seed.Bytes += size
			linked[action.output] = true
		}
		stamp := action.modified.UnixNano()
		if stamp < 0 {
			stamp = 0
		}
		actionPath := nativeCachePath(destinationPath, action.name, "a")
		if current, valid := readNativeAction(actionPath, action.name, hooks); valid &&
			current.output == action.output && current.size == size {
			seed.Actions++
			seed.actions[action.name] = true
			continue
		}
		if _, err := hooks.lstat(actionPath); err == nil {
			if !repair {
				return NativeSeed{}, fmt.Errorf("goatest: native build cache action %s is not the expected projection", actionPath)
			}
			if err := hooks.removeAll(actionPath); err != nil {
				return NativeSeed{}, fmt.Errorf("goatest: replace native build cache action: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return NativeSeed{}, fmt.Errorf("goatest: inspect native build cache action: %w", err)
		}
		record := fmt.Appendf(nil, "%s %s %s %*d %*d\n", nativeActionFormat, action.name, action.output,
			nativeActionDecimalWidth, size, nativeActionDecimalWidth, stamp)

		if err := hooks.writeFile(actionPath, record, filemode.PrivateFile); err != nil {
			return NativeSeed{}, fmt.Errorf("goatest: write native build cache action: %w", err)
		}
		seed.Actions++
		seed.actions[action.name] = true
	}
	return seed, nil
}

func PersistNative(base, source string, baseline NativeSeed, now time.Time) (NativePersisted, error) {
	return persistNativeWithHooks(base, source, baseline, now, layerHooks{})
}

func persistNativeWithHooks(base, source string, baseline NativeSeed, now time.Time, hooks layerHooks) (result NativePersisted, resultErr error) {
	hooks = hooks.resolved()
	if base == "" || source == "" {
		return NativePersisted{}, errors.New("goatest: native build cache persistence requires source and destination")
	}
	basePath, err := filepath.Abs(base)
	if err != nil {
		return NativePersisted{}, fmt.Errorf("goatest: resolve native build cache persistence destination: %w", err)
	}
	sourcePath, err := filepath.Abs(source)
	if err != nil {
		return NativePersisted{}, fmt.Errorf("goatest: resolve native build cache persistence source: %w", err)
	}
	if basePath == sourcePath {
		return NativePersisted{}, errors.New("goatest: native build cache persistence source and destination are the same directory")
	}
	root, err := hooks.lstat(sourcePath)
	if err != nil {
		return NativePersisted{}, fmt.Errorf("goatest: inspect native build cache persistence source: %w", err)
	}
	if !root.IsDir() {
		return NativePersisted{}, fmt.Errorf("goatest: native build cache persistence source %s is not a directory", sourcePath)
	}
	layer := Layer{Dir: basePath}
	release, held, err := layer.HoldCollection()
	if err != nil {
		return NativePersisted{}, err
	}
	if !held {
		return NativePersisted{Deferred: true}, nil
	}
	defer func() { resultErr = errors.Join(resultErr, release()) }()
	if now.IsZero() {
		now = time.Now()
	}
	if baseline.actions == nil {
		baseline.actions = make(map[string]bool)
	}
	for prefix := 0; prefix < nativeCachePrefixCount; prefix++ {
		directory := filepath.Join(sourcePath, fmt.Sprintf("%02x", prefix))
		info, err := hooks.lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return result, fmt.Errorf("goatest: inspect native build cache persistence source: %w", err)
		}
		if !info.IsDir() {
			return result, fmt.Errorf("goatest: native build cache persistence prefix %s is not a directory", directory)
		}
		entries, err := hooks.readDir(directory)
		if err != nil {
			return result, fmt.Errorf("goatest: inspect native build cache persistence source: %w", err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, "-a") || entry.IsDir() {
				continue
			}
			actionName := strings.TrimSuffix(name, "-a")
			if baseline.actions[actionName] {
				continue
			}
			action, valid := readNativeAction(filepath.Join(directory, name), actionName, hooks)
			if !valid {
				result.Skipped++
				continue
			}
			actionID, actionOK := nativeIdentifier(actionName)
			outputID, outputOK := nativeIdentifier(action.output)
			if !actionOK || !outputOK {
				result.Skipped++
				continue
			}
			existing, _, err := layer.readAction(actionID, hooks)
			if err != nil {
				return result, err
			}
			if existing.Output == action.output && existing.Size == action.size {
				valid, err := regularFileWithSize(layer.objectPath(outputID), action.size, hooks)
				if err != nil {
					return result, err
				}
				if valid {
					baseline.actions[actionName] = true
					continue
				}
			}
			sourceObject := nativeCachePath(sourcePath, action.output, "d")
			persisted, err := persistNativeObject(sourceObject, layer.objectPath(outputID), action.size, now, hooks)
			if err != nil {
				return result, err
			}
			if persisted == objectUnusable {
				result.Skipped++
				continue
			}
			if _, err := layer.putAction(actionID, outputID, action.size, now, layer.objectPath(outputID), hooks); err != nil {
				return result, err
			}
			result.Actions++
			if persisted == objectLinked {
				result.Objects++
				result.Bytes += action.size
			}
			baseline.actions[actionName] = true
		}
	}
	return result, nil
}

type persistedObject int

const (
	objectUnusable persistedObject = iota

	objectPresent

	objectLinked
)

func persistNativeObject(source, destination string, size int64, now time.Time, hooks layerHooks) (persistedObject, error) {
	valid, err := regularFileWithSize(source, size, hooks)
	if !valid {
		return objectUnusable, err
	}
	valid, err = regularFileWithSize(destination, size, hooks)
	if err != nil {
		return objectUnusable, err
	}
	if valid {
		return objectPresent, nil
	}
	if _, err := hooks.lstat(destination); err == nil {
		return objectUnusable, fmt.Errorf("goatest: persistent build cache object %s has unexpected contents", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return objectUnusable, fmt.Errorf("goatest: inspect persistent build cache object: %w", err)
	}
	if err := hooks.mkdirAll(filepath.Dir(destination), filemode.ReadableDirectory); err != nil {
		return objectUnusable, fmt.Errorf("goatest: create persistent build cache object directory: %w", err)
	}
	if err := hooks.chtimes(source, now, now); err != nil {
		return objectUnusable, fmt.Errorf("goatest: retain native build cache object: %w", err)
	}
	if err := hooks.link(source, destination); err != nil {
		valid, inspectErr := regularFileWithSize(destination, size, hooks)
		if inspectErr != nil {
			return objectUnusable, inspectErr
		}
		if valid {
			return objectPresent, nil
		}
		return objectUnusable, fmt.Errorf("goatest: persist native build cache object: %w", err)
	}
	return objectLinked, nil
}

func regularFileWithSize(path string, size int64, hooks layerHooks) (bool, error) {
	info, err := hooks.lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("goatest: inspect build cache object: %w", err)
	}
	return info.Mode().IsRegular() && info.Size() == size, nil
}

func prepareNativeCache(destination string, now time.Time, hooks layerHooks) error {
	if err := hooks.mkdirAll(destination, filemode.PrivateDirectory); err != nil {
		return fmt.Errorf("goatest: create native build cache: %w", err)
	}
	for prefix := 0; prefix < nativeCachePrefixCount; prefix++ {
		if err := hooks.mkdirAll(filepath.Join(destination, fmt.Sprintf("%02x", prefix)), filemode.PrivateDirectory); err != nil {
			return fmt.Errorf("goatest: create native build cache: %w", err)
		}
	}
	if now.IsZero() {
		now = time.Now()
	}

	if err := hooks.writeFile(filepath.Join(destination, "trim.txt"), fmt.Appendf(nil, "%d", now.Unix()), filemode.PrivateFile); err != nil {
		return fmt.Errorf("goatest: initialize native build cache trim record: %w", err)
	}
	return nil
}

func nativeIdentifier(value string) ([]byte, bool) {
	if len(value) != hex.EncodedLen(nativeCacheIdentifierBytes) {
		return nil, false
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != nativeCacheIdentifierBytes {
		return nil, false
	}
	return decoded, true
}

func nativeCachePath(root, identifier, kind string) string {
	return filepath.Join(root, identifier[:nativeCachePrefixHexDigits], identifier+"-"+kind)
}

func linkNativeObject(source, destination string, size int64, repair bool, hooks layerHooks) error {
	if info, err := hooks.lstat(destination); err == nil {
		sourceInfo, sourceErr := hooks.lstat(source)
		if sourceErr != nil {
			return fmt.Errorf("goatest: inspect native build cache source object: %w", sourceErr)
		}
		if !sourceInfo.Mode().IsRegular() || sourceInfo.Size() != size {
			return fmt.Errorf("goatest: native build cache source object %s is not the expected regular file", source)
		}
		if info.Mode().IsRegular() && info.Size() == size && os.SameFile(sourceInfo, info) {
			return nil
		}
		if !repair {
			return fmt.Errorf("goatest: native build cache object %s is not the expected projection", destination)
		}

		if err := hooks.removeAll(destination); err != nil {
			return fmt.Errorf("goatest: replace native build cache object: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("goatest: inspect native build cache object: %w", err)
	}
	if err := hooks.link(source, destination); err != nil {
		return fmt.Errorf("goatest: hard-link native build cache object: %w", err)
	}
	return nil
}

type nativeObject struct {
	path     string
	name     string
	size     int64
	modified time.Time
}

func CollectNative(directory string, maxBytes int64) (NativeCollected, error) {
	return collectNativeWithHooks(directory, maxBytes, layerHooks{})
}

func collectNativeWithHooks(directory string, maxBytes int64, hooks layerHooks) (NativeCollected, error) {
	hooks = hooks.resolved()
	if maxBytes < 0 {
		return NativeCollected{}, errors.New("goatest: native build cache bound must not be negative")
	}
	actions, objects, err := inspectNativeCache(directory, hooks)
	if err != nil {
		return NativeCollected{}, err
	}
	result := NativeCollected{}
	for _, object := range objects {
		result.BeforeBytes += object.size
	}
	result.AfterBytes = result.BeforeBytes
	if maxBytes == 0 || result.AfterBytes <= maxBytes {
		return result, nil
	}
	slices.SortFunc(objects, func(left, right nativeObject) int {
		if compared := left.modified.Compare(right.modified); compared != 0 {
			return compared
		}
		return strings.Compare(left.name, right.name)
	})
	for _, object := range objects {
		if result.AfterBytes <= maxBytes {
			break
		}
		if err := hooks.removeAll(object.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return NativeCollected{}, fmt.Errorf("goatest: collect native build cache object: %w", err)
		}
		result.RemovedObjects++
		result.RemovedBytes += object.size
		result.AfterBytes -= object.size
		for _, action := range actions[object.name] {
			if err := hooks.remove(action); err != nil && !errors.Is(err, os.ErrNotExist) {
				return NativeCollected{}, fmt.Errorf("goatest: collect native build cache action: %w", err)
			}
			result.RemovedActions++
		}
	}
	return result, nil
}

func inspectNativeCache(directory string, hooks layerHooks) (map[string][]string, []nativeObject, error) {
	root, err := hooks.lstat(directory)
	if err != nil {
		return nil, nil, fmt.Errorf("goatest: inspect native build cache root: %w", err)
	}
	if !root.IsDir() {
		return nil, nil, fmt.Errorf("goatest: native build cache root %s is not a directory", directory)
	}
	actions := make(map[string][]string)
	var objects []nativeObject
	for prefix := 0; prefix < nativeCachePrefixCount; prefix++ {
		subdirectory := filepath.Join(directory, fmt.Sprintf("%02x", prefix))
		info, err := hooks.lstat(subdirectory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("goatest: inspect native build cache: %w", err)
		}
		if !info.IsDir() {
			return nil, nil, fmt.Errorf("goatest: native build cache prefix %s is not a directory", subdirectory)
		}
		entries, err := hooks.readDir(subdirectory)
		if err != nil {
			return nil, nil, fmt.Errorf("goatest: inspect native build cache: %w", err)
		}
		for _, entry := range entries {
			name := entry.Name()
			path := filepath.Join(subdirectory, name)
			switch {
			case strings.HasSuffix(name, "-a") && !entry.IsDir():
				action, valid := readNativeAction(path, strings.TrimSuffix(name, "-a"), hooks)
				if valid {
					actions[action.output] = append(actions[action.output], path)
				}
			case strings.HasSuffix(name, "-d"):
				identifier := strings.TrimSuffix(name, "-d")
				if _, valid := nativeIdentifier(identifier); !valid {
					continue
				}
				info, infoErr := entry.Info()
				if infoErr != nil {
					if errors.Is(infoErr, os.ErrNotExist) {
						continue
					}
					return nil, nil, fmt.Errorf("goatest: inspect native build cache object: %w", infoErr)
				}
				size, sizeErr := nativeObjectSize(path, info)
				if sizeErr != nil {
					return nil, nil, sizeErr
				}
				objects = append(objects, nativeObject{path: path, name: identifier, size: size, modified: info.ModTime()})
			}
		}
	}

	for index := range objects {
		for _, action := range actions[objects[index].name] {
			info, err := hooks.stat(action)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, nil, fmt.Errorf("goatest: inspect native build cache action: %w", err)
			}
			if info.ModTime().After(objects[index].modified) {
				objects[index].modified = info.ModTime()
			}
		}
	}
	return actions, objects, nil
}

type nativeAction struct {
	output string
	size   int64
}

func readNativeAction(path, name string, hooks layerHooks) (nativeAction, bool) {
	if _, valid := nativeIdentifier(name); !valid {
		return nativeAction{}, false
	}
	info, err := hooks.lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nativeAction{}, false
	}
	file, err := os.Open(path)
	if err != nil {
		return nativeAction{}, false
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return nativeAction{}, false
	}
	line := scanner.Text()
	if scanner.Scan() || scanner.Err() != nil {
		return nativeAction{}, false
	}
	fields := strings.Fields(line)
	if len(fields) != nativeActionFieldCount || fields[nativeActionFormatField] != nativeActionFormat || fields[nativeActionKeyField] != name {
		return nativeAction{}, false
	}
	if _, valid := nativeIdentifier(fields[nativeActionOutputField]); !valid {
		return nativeAction{}, false
	}
	size, err := strconv.ParseInt(fields[nativeActionSizeField], nativeActionDecimalRadix, nativeActionIntegerBits)
	if err != nil || size < 0 {
		return nativeAction{}, false
	}
	if stamp, err := strconv.ParseInt(fields[nativeActionTimestampField], nativeActionDecimalRadix, nativeActionIntegerBits); err != nil || stamp < 0 {
		return nativeAction{}, false
	}
	return nativeAction{output: fields[nativeActionOutputField], size: size}, true
}

func nativeObjectSize(path string, info fs.FileInfo) (int64, error) {
	if !info.IsDir() {
		return info.Size(), nil
	}
	var size int64
	err := filepath.WalkDir(path, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		item, err := entry.Info()
		if err != nil {
			return err
		}
		size += item.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("goatest: inspect native build cache executable: %w", err)
	}
	return size, nil
}
