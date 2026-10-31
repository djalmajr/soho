// Package skill contains the portable resources bundled into the Go CLI.
package skill

import (
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Scripts are deliberately excluded: the executable contains the only engine.
//
//go:embed SKILL.md README.md config.defaults roles references templates agents
var resources embed.FS

// ReadConfigDefaults reads the bundled defaults without materializing a skill cache.
func ReadConfigDefaults() ([]byte, error) {
	return resources.ReadFile("config.defaults")
}

type resource struct {
	name string
	data []byte
}

func bundledFiles() ([]resource, string, error) {
	var files []resource
	hash := sha256.New()
	err := fs.WalkDir(resources, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := resources.ReadFile(name)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		fmt.Fprintf(hash, "%s\x00%x\n", name, digest)
		files = append(files, resource{name: name, data: data})
		return nil
	})
	return files, hex.EncodeToString(hash.Sum(nil)), err
}

// Materialize publishes immutable, verified bundled resources into a cache.
// Existing corrupt or unknown content is refused rather than repaired or
// overwritten. Concurrent callers may share a fully verified completed bundle.
func Materialize(cache string) (string, error) {
	if !filepath.IsAbs(cache) {
		return "", fmt.Errorf("bundled skill: cache must be an absolute path")
	}
	files, digest, err := bundledFiles()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(cache)
	if err != nil {
		return "", err
	}
	defer root.Close()
	for _, dir := range []string{"soho", filepath.Join("soho", "bundled")} {
		if err := root.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
			return "", err
		}
		info, err := root.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("bundled skill: unsafe cache directory %s", dir)
		}
	}
	parent := filepath.Join("soho", "bundled")
	destination := filepath.Join(parent, digest)
	path := filepath.Join(cache, destination)
	if _, err := root.Lstat(destination); err == nil {
		return path, verifyPublished(root, destination, files)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	stage := filepath.Join(parent, ".staging-"+hex.EncodeToString(nonce))
	if err := root.Mkdir(stage, 0o700); err != nil {
		return "", err
	}
	defer root.RemoveAll(stage)
	staging, err := root.OpenRoot(stage)
	if err != nil {
		return "", err
	}
	for _, file := range files {
		name := filepath.FromSlash(file.name)
		if err := staging.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			staging.Close()
			return "", err
		}
		out, err := staging.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			staging.Close()
			return "", err
		}
		_, writeErr := out.Write(file.data)
		closeErr := out.Close()
		if writeErr != nil || closeErr != nil {
			staging.Close()
			if writeErr != nil {
				return "", writeErr
			}
			return "", closeErr
		}
	}
	if err := staging.Close(); err != nil {
		return "", err
	}
	if err := root.Rename(stage, destination); err != nil {
		// A concurrent publisher may have won; accept only exact known bytes.
		if verifyErr := verifyPublished(root, destination, files); verifyErr != nil {
			return "", fmt.Errorf("bundled skill: publish failed: %w", err)
		}
	}
	return path, verifyPublished(root, destination, files)
}

// Cached looks up the existing verified bundle under cache strictly
// read-only: it creates nothing and repairs nothing. The returned path is
// the bundle root whose contents verify against the bundled digest. A
// missing cache or bundle is an error satisfying os.IsNotExist; corrupt,
// unknown or symlinked content fails closed.
func Cached(cache string) (string, error) {
	files, digest, err := bundledFiles()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(cache) {
		return "", fmt.Errorf("bundled skill: cache must be an absolute path")
	}
	root, err := os.OpenRoot(cache)
	if err != nil {
		return "", err
	}
	defer root.Close()
	parent := filepath.Join("soho", "bundled")
	destination := filepath.Join(parent, digest)
	if err := verifyPublished(root, destination, files); err != nil {
		return "", err
	}
	return filepath.Join(cache, destination), nil
}

// bundleTraversal is private to one verification call. Production supplies
// the native nonblocking opener; tests may schedule a replacement during
// open or after binding. There is no process-wide hook or mutable default.
type bundleTraversal struct {
	open     func(*os.Root, string) (*os.Root, error)
	verified func(string)
}

// verifyPublished acquires every appended directory separately and keeps
// each checked handle for its immediate children. The trusted base may be
// an alias; appended links, replacements and non-directories are refused.
// Each directory is enumerated once, bounded to known+1 entries; resource
// bodies retain their known-length and digest checks without path reopens.
func verifyPublished(root *os.Root, name string, files []resource) error {
	return verifyPublishedUsing(root, name, files, bundleTraversal{open: openBundleDirectory})
}

func verifyPublishedUsing(root *os.Root, name string, files []resource, traversal bundleTraversal) error {
	current := root
	label := ""
	for _, component := range strings.Split(filepath.ToSlash(name), "/") {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("bundled skill: invalid bundle component")
		}
		if label != "" {
			label += "/"
		}
		label += component
		child, err := bindPublishedDirectory(current, component, label, traversal)
		if err != nil {
			return err
		}
		defer child.Close()
		current = child
	}
	dirs, _ := knownTree(files)
	bodies := make(map[string][]byte, len(files))
	for _, file := range files {
		bodies[file.name] = file.data
	}
	return walkPublishedDirectory(current, ".", label, dirs, bodies, traversal)
}

func bindPublishedDirectory(parent *os.Root, name, label string, traversal bundleTraversal) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, err // Preserve not-exist for a missing cache component.
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("bundled skill: unsafe published directory: %s", label)
	}
	child, err := traversal.open(parent, name)
	if err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			child.Close()
		}
	}()
	opened, openErr := child.Stat(".")
	post, postErr := parent.Lstat(name)
	if openErr != nil || !opened.IsDir() || !os.SameFile(before, opened) ||
		postErr != nil || !post.IsDir() || post.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, post) {
		return nil, fmt.Errorf("bundled skill: published directory identity changed: %s", label)
	}
	if traversal.verified != nil {
		traversal.verified(label)
	}
	retained = true
	return child, nil
}

func walkPublishedDirectory(root *os.Root, dir, label string, dirs map[string]map[string]bool, bodies map[string][]byte, traversal bundleTraversal) error {
	entries, err := verifyPublishedEntries(root, dir, dirs[dir])
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		full := name
		if dir != "." {
			full = dir + "/" + name
		}
		if _, directory := dirs[full]; directory {
			child, err := bindPublishedDirectory(root, name, label+"/"+name, traversal)
			if err != nil {
				return err
			}
			err = walkPublishedDirectory(child, full, label+"/"+name, dirs, bodies, traversal)
			child.Close()
			if err != nil {
				return err
			}
		} else if err := verifyPublishedFile(root, name, bodies[full]); err != nil {
			return err
		}
	}
	return nil
}

// knownTree derives the known resource tree from the embedded list: each
// known directory maps to the set of entry names it must hold exactly
// (known files and known subdirectories), in first-seen (embedded) order.
func knownTree(files []resource) (map[string]map[string]bool, []string) {
	dirs := map[string]map[string]bool{}
	var order []string
	add := func(dir, entry string) {
		if dirs[dir] == nil {
			dirs[dir] = map[string]bool{}
			order = append(order, dir)
		}
		dirs[dir][entry] = true
	}
	for _, file := range files {
		parts := strings.Split(file.name, "/")
		parent := "."
		for i := 0; i+1 < len(parts); i++ {
			add(parent, parts[i])
			if i == 0 {
				parent = parts[i]
			} else {
				parent = strings.Join(parts[:i+1], "/")
			}
		}
		add(parent, parts[len(parts)-1])
	}
	return dirs, order
}

// verifyPublishedEntries checks that dir holds exactly the known entries,
// reading at most len(known)+1 directory records through a retained
// directory handle: more entries than the known tree is a refusal without
// enumerating the excess, a symlink or an unknown entry is a refusal, and
// the known set (files plus subdirectories) must match exactly.
func verifyPublishedEntries(bundle *os.Root, dir string, known map[string]bool) ([]os.DirEntry, error) {
	// "." resolves from an already verified retained directory, never a
	// full-relative path through ancestors that may have been replaced.
	before, err := bundle.Stat(".")
	if err != nil || !before.IsDir() {
		return nil, fmt.Errorf("bundled skill: unsafe published bundle entry: %s", dir)
	}
	f, err := bundle.OpenFile(".", readOnlyNonBlocking, 0)
	if err != nil {
		return nil, fmt.Errorf("bundled skill: unsafe published bundle entry: %s", dir)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("bundled skill: unsafe published bundle entry: %s", dir)
	}
	entries, err := f.ReadDir(len(known) + 1)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("bundled skill: unsafe published bundle entry: %s", dir)
	}
	if len(entries) > len(known) {
		return nil, fmt.Errorf("bundled skill: unknown entry in published bundle: %s", dir)
	}
	if len(entries) != len(known) {
		return nil, fmt.Errorf("bundled skill: missing entry in published bundle: %s", dir)
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("bundled skill: symlink in published bundle: %s", entry.Name())
		}
		if !known[entry.Name()] {
			return nil, fmt.Errorf("bundled skill: unknown entry in published bundle: %s", entry.Name())
		}
		if seen[entry.Name()] {
			return nil, fmt.Errorf("bundled skill: repeated entry in published bundle: %s", entry.Name())
		}
		seen[entry.Name()] = true
	}
	return entries, nil
}

// verifyPublishedFile checks one known resource: a plain regular entry
// whose size equals the known embedded length (refused before any body
// allocation), opened through a bounded nonblocking regular handle whose
// identity matches the entry before and after a bounded read of exactly
// the known length, with the body's digest checked against the embedded
// resource's digest.
func verifyPublishedFile(bundle *os.Root, name string, data []byte) error {
	entry, err := bundle.Lstat(name)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("bundled skill: missing resource file: %s", name)
		}
		return fmt.Errorf("bundled skill: invalid bytes: %s", name)
	}
	if entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() {
		return fmt.Errorf("bundled skill: unknown or nonregular file: %s", name)
	}
	if entry.Size() != int64(len(data)) {
		return fmt.Errorf("bundled skill: invalid bytes: %s", name)
	}
	f, err := bundle.OpenFile(name, readOnlyNonBlocking, 0)
	if err != nil {
		return fmt.Errorf("bundled skill: invalid bytes: %s", name)
	}
	defer f.Close()
	hinfo, err := f.Stat()
	if err != nil || !hinfo.Mode().IsRegular() || hinfo.Size() != int64(len(data)) || !os.SameFile(entry, hinfo) {
		return fmt.Errorf("bundled skill: invalid bytes: %s", name)
	}
	body := make([]byte, len(data))
	if _, err := io.ReadFull(f, body); err != nil {
		return fmt.Errorf("bundled skill: invalid bytes: %s", name)
	}
	post, err := bundle.Lstat(name)
	if err != nil || !post.Mode().IsRegular() || post.Size() != int64(len(data)) || !os.SameFile(hinfo, post) {
		return fmt.Errorf("bundled skill: invalid bytes: %s", name)
	}
	if digest := sha256.Sum256(body); digest != sha256.Sum256(data) {
		return fmt.Errorf("bundled skill: invalid bytes: %s", name)
	}
	return nil
}
