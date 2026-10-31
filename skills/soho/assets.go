// Package skill contains the portable resources bundled into the Go CLI.
package skill

import (
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
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
	if _, err := root.Lstat(destination); err != nil {
		// A missing cache or bundle is a not-exist error. This existence
		// probe is the only extra read of the lookup; the verdict on what
		// exists stays with verifyPublished.
		return "", err
	}
	if err := verifyPublished(root, destination, files); err != nil {
		return "", err
	}
	return filepath.Join(cache, destination), nil
}

func verifyPublished(root *os.Root, name string, files []resource) error {
	info, err := root.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("bundled skill: unsafe published bundle")
	}
	bundle, err := root.OpenRoot(name)
	if err != nil {
		return err
	}
	defer bundle.Close()
	expected := make(map[string][32]byte, len(files))
	for _, file := range files {
		expected[file.name] = sha256.Sum256(file.data)
	}
	err = fs.WalkDir(bundle.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundled skill: symlink in published bundle: %s", name)
		}
		if entry.IsDir() {
			if name != "." {
				prefix := name + "/"
				found := false
				for _, file := range files {
					found = found || strings.HasPrefix(file.name, prefix)
				}
				if !found {
					return fmt.Errorf("bundled skill: unknown directory: %s", name)
				}
			}
			return nil
		}
		digest, exists := expected[name]
		if !exists || !entry.Type().IsRegular() {
			return fmt.Errorf("bundled skill: unknown or nonregular file: %s", name)
		}
		data, err := bundle.ReadFile(filepath.FromSlash(name))
		if err != nil || sha256.Sum256(data) != digest {
			return fmt.Errorf("bundled skill: invalid bytes: %s", name)
		}
		delete(expected, name)
		return nil
	})
	if err != nil {
		return err
	}
	if len(expected) != 0 {
		return fmt.Errorf("bundled skill: missing resource files")
	}
	return nil
}
