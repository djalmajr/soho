package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/platform"
)

const referenceDiffCap = 2 * 1024 * 1024

type referenceBlob struct {
	path string
	size int64
	mode string
}

func dispatchReferencePrompt(prompt, body, role, sd, report string, env platform.Env) string {
	refs := dispatch.DeclaredReferences(body)
	tree := dispatch.DeclaredWorktree(body)
	if !core.IsReviewRole(role) || len(refs) == 0 || tree == "" {
		return prompt
	}
	content, listOnly, err := buildReferenceDiff(refs, tree, env)
	if err != nil {
		core.DieFriction("dispatch: reference diff: "+err.Error(), 4, frictionLogPath, "dispatch")
	}
	dir := filepath.Join(sd, "briefs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		core.DieFriction("dispatch: reference diff: "+err.Error(), 4, frictionLogPath, "dispatch")
	}
	file := filepath.Join(dir, strings.TrimSuffix(filepath.Base(report), ".md")+".reference.diff")
	if err := core.WriteScratchFile(file, content); err != nil {
		core.DieFriction("dispatch: reference diff: "+err.Error(), 4, frictionLogPath, "dispatch")
	}
	note := ""
	if listOnly {
		note = " (file list: whole reference tree exceeds 2 MB)"
	}
	return prompt + "\nReference diff for this review: " + file + note + "\n"
}

func referenceGitOutput(env platform.Env, args ...string) (string, error) {
	r := dispatch.ReferenceGit(env, args...)
	if r.Status == nil || *r.Status != 0 {
		return "", fmt.Errorf("git reference read failed (%s)", strings.Join(args, " "))
	}
	return r.Stdout, nil
}

func buildReferenceDiff(refs []dispatch.Reference, worktree string, env platform.Env) (string, bool, error) {
	tmp, err := os.MkdirTemp("", "soho-reference-")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(tmp)
	var out strings.Builder
	listOnly := false
	for index, ref := range refs {
		if _, err := referenceGitOutput(env, "-C", ref.Repo, "cat-file", "-e", ref.Commit+"^{commit}"); err != nil {
			return "", false, err
		}
		for _, p := range ref.Paths {
			if !dispatch.ReferencePathOK(p) {
				return "", false, fmt.Errorf("invalid reference path")
			}
			if _, err := referenceGitOutput(env, "-C", ref.Repo, "cat-file", "-e", ref.Commit+":"+p); err != nil {
				return "", false, err
			}
		}
		raw, err := referenceGitOutput(env, "-C", ref.Repo, "ls-tree", "-r", "-l", "-z", ref.Commit)
		if err != nil {
			return "", false, err
		}
		var blobs []referenceBlob
		var skippedGitlinks []string
		var total int64
		for _, record := range strings.Split(raw, "\x00") {
			if record == "" {
				continue
			}
			meta, p, ok := strings.Cut(record, "\t")
			fields := strings.Fields(meta)
			if !ok || len(fields) != 4 || !dispatch.ReferencePathOK(p) {
				return "", false, fmt.Errorf("unsupported reference tree entry")
			}
			selected := len(ref.Paths) == 0
			for _, wanted := range ref.Paths {
				if p == wanted || strings.HasPrefix(p, wanted+"/") {
					selected = true
				}
			}
			if selected {
				if fields[0] == "160000" {
					skippedGitlinks = append(skippedGitlinks, p)
					continue
				}
				if fields[1] != "blob" {
					return "", false, fmt.Errorf("unsupported reference tree entry")
				}
				size, err := strconv.ParseInt(fields[3], 10, 64)
				if err != nil || size < 0 {
					return "", false, fmt.Errorf("invalid reference blob size")
				}
				blobs = append(blobs, referenceBlob{p, size, fields[0]})
				total += size
			}
		}
		fmt.Fprintf(&out, "Reference: %s@%s\n", ref.Repo, ref.Commit)
		for _, p := range skippedGitlinks {
			fmt.Fprintf(&out, "Skipped gitlink: %s\n", p)
		}
		if len(ref.Paths) == 0 && total > referenceDiffCap {
			listOnly = true
			for _, blob := range blobs {
				fmt.Fprintf(&out, "%s\n", blob.path)
			}
			continue
		}
		base := filepath.Join(tmp, strconv.Itoa(index))
		if err := os.MkdirAll(base, 0700); err != nil {
			return "", false, err
		}
		// Directory selections also show additions. Whole-tree comparisons
		// visit the reference's files, excluding worktree .git and unrelated files.
		left := filepath.Join(base, "reference")
		right := worktree
		if err := os.MkdirAll(left, 0700); err != nil {
			return "", false, err
		}
		for _, blob := range blobs {
			contents, err := referenceGitOutput(env, "-C", ref.Repo, "show", ref.Commit+":"+blob.path)
			if err != nil {
				return "", false, err
			}
			file := filepath.Join(left, filepath.FromSlash(blob.path))
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				return "", false, err
			}
			if blob.mode == "120000" {
				if err := os.Symlink(contents, file); err != nil {
					return "", false, err
				}
			} else {
				mode := os.FileMode(0600)
				if blob.mode == "100755" {
					mode = 0700
				}
				if err := os.WriteFile(file, []byte(contents), mode); err != nil {
					return "", false, err
				}
			}
		}
		paths := ref.Paths
		if len(paths) == 0 {
			for _, blob := range blobs {
				paths = append(paths, blob.path)
			}
		}
		for _, p := range paths {
			isGitlink := false
			for _, skipped := range skippedGitlinks {
				if p == skipped {
					isGitlink = true
				}
			}
			if isGitlink {
				continue
			}
			a, b := filepath.Join(left, filepath.FromSlash(p)), filepath.Join(right, filepath.FromSlash(p))
			if _, err := os.Lstat(a); os.IsNotExist(err) {
				a = os.DevNull
			}
			if _, err := os.Lstat(b); os.IsNotExist(err) {
				b = os.DevNull
			}
			r := dispatch.ReferenceGit(env, "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--", a, b)
			if r.Status == nil || (*r.Status != 0 && *r.Status != 1) {
				return "", false, fmt.Errorf("git diff failed for %s", p)
			}
			out.WriteString(r.Stdout)
		}
	}
	result := out.String()
	if len(result) > referenceDiffCap {
		const notice = "\n[reference diff truncated at 2 MB]\n"
		result = result[:referenceDiffCap-len(notice)] + notice
	}
	return result, listOnly, nil
}
