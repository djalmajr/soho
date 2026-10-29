package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	textutil "github.com/djalmajr/soho/internal/text"
)

const mutationGuardUsage = "usage: mutation-guard <copy-dir> [--source <dir>] [--env NAME]..."

var (
	validEnvName      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	jsSpaceClass      = `[\s\v\x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]`
	tomlTargetBasic   = regexp.MustCompile(`^` + jsSpaceClass + `*target-dir` + jsSpaceClass + `*=` + jsSpaceClass + `*"((?:\\[^\r\x{2028}\x{2029}]|[^"\\])*)"` + jsSpaceClass + `*(?:#[^\r\x{2028}\x{2029}]*)?$`)
	tomlTargetLiteral = regexp.MustCompile(`^` + jsSpaceClass + `*target-dir` + jsSpaceClass + `*=` + jsSpaceClass + `*'([^']*)'` + jsSpaceClass + `*(?:#[^\r\x{2028}\x{2029}]*)?$`)
)

type mutationGuardArgs struct {
	copyPath string
	source   string
	envNames []string
}

func parseMutationGuardArgs(args []string) (mutationGuardArgs, bool) {
	var parsed mutationGuardArgs
	sourceSeen := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--source" || arg == "--env" {
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return mutationGuardArgs{}, false
			}
			i++
			if arg == "--source" {
				if sourceSeen {
					return mutationGuardArgs{}, false
				}
				sourceSeen = true
				parsed.source = args[i]
			} else {
				if !validEnvName.MatchString(args[i]) {
					return mutationGuardArgs{}, false
				}
				parsed.envNames = append(parsed.envNames, args[i])
			}
			continue
		}
		if strings.HasPrefix(arg, "--") || parsed.copyPath != "" {
			return mutationGuardArgs{}, false
		}
		parsed.copyPath = arg
	}
	if parsed.copyPath == "" {
		return mutationGuardArgs{}, false
	}
	return parsed, true
}

func runMutationGuard(args []string, env platform.Env) int {
	cwd, err := os.Getwd()
	if err != nil {
		return mutationGuardUsageError()
	}
	return runMutationGuardAt(args, env, cwd)
}

func runMutationGuardAt(args []string, env platform.Env, cwd string) int {
	parsed, ok := parseMutationGuardArgs(args)
	if !ok {
		return mutationGuardUsageError()
	}

	copyPath, err := filepath.Abs(resolveRelative(cwd, parsed.copyPath))
	if err != nil {
		return mutationGuardUsageError()
	}
	copyInfo, err := os.Stat(copyPath)
	if err != nil || !copyInfo.IsDir() {
		return mutationGuardUsageError()
	}
	copyPath, err = filepath.EvalSymlinks(copyPath)
	if err != nil {
		return mutationGuardUsageError()
	}

	sourcePath := parsed.source
	if sourcePath == "" {
		sourcePath = platform.ProjectRoot(env, cwd)
	} else {
		sourcePath = resolveRelative(cwd, sourcePath)
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil || !sourceInfo.IsDir() {
		return mutationGuardUsageError()
	}
	sourcePath, err = filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return mutationGuardUsageError()
	}
	if parsed.source == "" && inside(copyPath, sourcePath) {
		_, _ = fmt.Fprintf(platform.Stderr, "mutation-guard: without --source the source is the project of the current directory, which is the copy itself (%s); run it from the source worktree or pass --source <dir>\n", copyPath)
		return 2
	}

	report := mutationGuardChecks(copyPath, sourcePath, env, parsed.envNames)
	failed := printMutationCheck("copy-outside-source", "", report.Overlap)
	failed = printMutationCheck("copy-outside-repository", report.Repository, false) || failed
	failed = printMutationCheck("no-symlink-into-source", report.Symlink, false) || failed
	failed = printMutationCheck("build-env", report.BuildEnv, false) || failed
	failed = printMutationCheck("cargo-config", report.Cargo, false) || failed
	if failed {
		return 1
	}
	return 0
}

// mutationGuardReport is one guard run's results: each field is the detail
// printed for its check ("" passes), Overlap the copy/source overlap flag.
type mutationGuardReport struct {
	Overlap    bool
	Repository string
	Symlink    string
	BuildEnv   string
	Cargo      string
}

func (r mutationGuardReport) failed() bool {
	return r.Overlap || r.Repository != "" || r.Symlink != "" || r.BuildEnv != "" || r.Cargo != ""
}

// mutationGuardChecks runs the guard's checks for copyPath against sourcePath
// without printing. extraEnvNames are the `--env NAME` values the guard CLI
// accepted; mutation-copy passes none.
func mutationGuardChecks(copyPath, sourcePath string, env platform.Env, extraEnvNames []string) mutationGuardReport {
	report := mutationGuardReport{
		Overlap:    insideFilesystem(sourcePath, copyPath) || insideFilesystem(copyPath, sourcePath),
		Repository: mutationRepositoryRefusal(copyPath, sourcePath, env),
	}
	if detail, scanErr := symlinkIntoSource(copyPath, sourcePath); scanErr != nil {
		report.Symlink = "unable to inspect symlinks safely"
	} else {
		report.Symlink = detail
	}
	envNames := []string{"CARGO_TARGET_DIR", "CARGO_BUILD_TARGET_DIR"}
	seen := map[string]bool{envNames[0]: true, envNames[1]: true}
	for _, name := range extraEnvNames {
		if !seen[name] {
			seen[name] = true
			envNames = append(envNames, name)
		}
	}
	report.BuildEnv = envPointingIntoSource(env, envNames, copyPath, sourcePath)
	report.Cargo = cargoTargetInsideSource(copyPath, sourcePath, env)
	return report
}

// failingLines is the guard's `fail` output, one line per failed check, for
// callers that need the reasons without the `ok` lines.
func (r mutationGuardReport) failingLines() []string {
	var lines []string
	if r.Overlap {
		lines = append(lines, "fail copy-outside-source: copy and source directories overlap")
	}
	if r.Repository != "" {
		lines = append(lines, "fail copy-outside-repository: "+r.Repository)
	}
	if r.Symlink != "" {
		lines = append(lines, "fail no-symlink-into-source: "+r.Symlink)
	}
	if r.BuildEnv != "" {
		lines = append(lines, "fail build-env: "+r.BuildEnv)
	}
	if r.Cargo != "" {
		lines = append(lines, "fail cargo-config: "+r.Cargo)
	}
	return lines
}

func mutationGuardUsageError() int {
	_, _ = fmt.Fprintf(platform.Stderr, "soho: %s\n", mutationGuardUsage)
	return 2
}

// mutationRepositoryTop finds the main checkout even when source is a linked
// worktree. Nonstandard Git layouts fall back to the source's worktree top.
func mutationRepositoryTop(source string, env platform.Env) string {
	gitEnv := env.Clone()
	for key := range gitEnv {
		switch strings.ToUpper(key) {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE":
			delete(gitEnv, key)
		}
	}
	resolve := func(args ...string) string {
		result := platform.RunCli("git", append([]string{"-C", source, "rev-parse"}, args...), platform.RunOptions{Env: gitEnv, Cwd: source, TimeoutMs: 5_000})
		if result.NotFound || result.Status == nil || *result.Status != 0 {
			return ""
		}
		value := strings.TrimSuffix(strings.TrimSuffix(result.Stdout, "\n"), "\r")
		if value == "" {
			return ""
		}
		resolved, err := filepath.EvalSymlinks(resolveRelative(source, filepath.FromSlash(value)))
		if err != nil {
			return ""
		}
		return resolved
	}
	if common := resolve("--path-format=absolute", "--git-common-dir"); common != "" && filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	return resolve("--show-toplevel")
}

func mutationRepositoryRefusal(copyPath, sourcePath string, env platform.Env) string {
	if top := mutationRepositoryTop(sourcePath, env); top != "" && insideFilesystem(top, copyPath) {
		return "the copy is inside the repository at " + top
	}
	return ""
}

// insideFilesystem retains lexical containment and also recognizes aliases
// by comparing the parent with each existing candidate ancestor. Missing
// leaves are skipped until their nearest existing ancestor is reached.
func insideFilesystem(parent, candidate string) bool {
	if inside(parent, candidate) {
		return true
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return false
	}
	for path := filepath.Clean(candidate); ; path = filepath.Dir(path) {
		if info, err := os.Stat(path); err == nil && os.SameFile(parentInfo, info) {
			return true
		}
		if filepath.Dir(path) == path {
			return false
		}
	}
}

func printMutationCheck(name, detail string, overlap bool) bool {
	if overlap {
		detail = "copy and source directories overlap"
	}
	if detail != "" {
		_, _ = fmt.Fprintf(platform.Stdout, "fail %s: %s\n", name, detail)
		return true
	}
	_, _ = fmt.Fprintf(platform.Stdout, "ok %s\n", name)
	return false
}

func resolveRelative(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
}

func inside(parent, candidate string) bool {
	relative, err := filepath.Rel(parent, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}

func canonicalPath(input string) (string, error) {
	unresolved, err := filepath.Abs(input)
	if err != nil {
		return "", err
	}
	var tail []string
	for {
		resolved, resolveErr := filepath.EvalSymlinks(unresolved)
		if resolveErr == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(resolveErr, fs.ErrNotExist) && !errors.Is(resolveErr, syscall.ENOTDIR) {
			return "", resolveErr
		}
		parent := filepath.Dir(unresolved)
		if parent == unresolved {
			return "", resolveErr
		}
		tail = append(tail, filepath.Base(unresolved))
		unresolved = parent
	}
}

func symlinkIntoSource(copyPath, sourcePath string) (string, error) {
	pending := []string{copyPath}
	for len(pending) > 0 {
		dir := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", err
		}
		sort.Slice(entries, func(i, j int) bool { return textutil.CompareUTF16(entries[i].Name(), entries[j].Name()) > 0 })
		for _, entry := range entries {
			full := filepath.Join(dir, entry.Name())
			relative, err := filepath.Rel(copyPath, full)
			if err != nil {
				return "", err
			}
			info, err := os.Lstat(full)
			if err != nil {
				return "", err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := filepath.EvalSymlinks(full)
				if err != nil {
					if errors.Is(err, fs.ErrNotExist) {
						continue
					}
					return "", err
				}
				// A target that contains the copy is a link loop: the copy holds
				// a link back to itself or an ancestor of itself, so the scan
				// cannot prove the copy is isolated and the guard refuses it.
				if mutationPathContains(sourcePath, target) || mutationPathContains(target, sourcePath) || mutationPathContains(target, copyPath) {
					return fmt.Sprintf("symlink %s -> %s", relative, target), nil
				}
			} else if info.IsDir() && entry.Name() != ".git" {
				pending = append(pending, full)
			}
		}
	}
	return "", nil
}

func envPointingIntoSource(env platform.Env, names []string, copyPath, sourcePath string) string {
	var offenders []string
	for _, name := range names {
		value := env.Get(name)
		if value == "" {
			continue
		}
		resolved, err := canonicalPath(resolveRelative(copyPath, value))
		if err != nil {
			offenders = append(offenders, name+" cannot be resolved safely")
		} else if inside(sourcePath, resolved) {
			offenders = append(offenders, name+" points into the source tree")
		}
	}
	return strings.Join(offenders, "; ")
}

type mutationGuardConfigPaths struct {
	Directory, TargetBase string
	Files                 []string
}

type mutationGuardReadSet struct {
	Manifest  string
	Ancestors []mutationGuardConfigPaths
	CargoHome mutationGuardConfigPaths
}

func (reads mutationGuardReadSet) paths() []string {
	paths := []string{reads.Manifest}
	for _, config := range reads.Ancestors {
		paths = append(paths, config.Files...)
	}
	return append(paths, reads.CargoHome.Files...)
}

// mutationGuardReadPaths returns the guard's absolute content-read paths without
// resolving symlinks. Relative CARGO_HOME is relative to the calling process's
// cwd, just like the guard's original ReadTextFile calls; TargetBase preserves
// its existing relative target-dir interpretation and diagnostic spelling.
func mutationGuardReadPaths(copyPath string, env platform.Env) (mutationGuardReadSet, error) {
	var reads mutationGuardReadSet
	root, err := filepath.Abs(copyPath)
	if err != nil {
		return reads, err
	}
	reads.Manifest = filepath.Join(root, "Cargo.toml")
	configPaths := func(directory, targetBase string) (mutationGuardConfigPaths, error) {
		config := mutationGuardConfigPaths{Directory: directory, TargetBase: targetBase}
		for _, name := range []string{"config", "config.toml"} {
			path, err := filepath.Abs(filepath.Join(directory, name))
			if err != nil {
				return config, err
			}
			config.Files = append(config.Files, path)
		}
		return config, nil
	}
	for dir := root; ; dir = filepath.Dir(dir) {
		config, err := configPaths(filepath.Join(dir, ".cargo"), dir)
		if err != nil {
			return reads, err
		}
		reads.Ancestors = append(reads.Ancestors, config)
		if filepath.Dir(dir) == dir {
			break
		}
	}
	cargoHome := env.Get("CARGO_HOME")
	if cargoHome == "" {
		home := env.Get("HOME")
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		cargoHome = filepath.Join(home, ".cargo")
	}
	reads.CargoHome, err = configPaths(cargoHome, filepath.Dir(cargoHome))
	return reads, err
}

func cargoTargetInsideSource(copyPath, sourcePath string, env platform.Env) string {
	reads, err := mutationGuardReadPaths(copyPath, env)
	if err != nil {
		return "cargo metadata failed; target-dir cannot be resolved safely"
	}
	if _, err := os.Stat(reads.Manifest); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ""
		}
		return "cargo metadata failed; target-dir cannot be resolved safely"
	}
	if cargo, ok := cargoExecutable(env); ok {
		return cargoMetadataTargetInsideSource(cargo, copyPath, sourcePath, env)
	}
	return textualCargoTargetInsideSource(copyPath, sourcePath, reads)
}

func cargoExecutable(env platform.Env) (string, bool) {
	pathValue := platform.PathValue(env)
	if pathValue == "" {
		return "", false
	}
	for _, dir := range filepath.SplitList(pathValue) {
		candidate := filepath.Join(dir, "cargo")
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() {
			return candidate, true
		}
		if runtime.GOOS == "windows" {
			for _, ext := range []string{".exe", ".cmd", ".bat"} {
				candidate = filepath.Join(dir, "cargo"+ext)
				info, err = os.Stat(candidate)
				if err == nil && !info.IsDir() {
					return candidate, true
				}
			}
		}
	}
	return "", false
}

func cargoMetadataTargetInsideSource(cargo, copyPath, sourcePath string, env platform.Env) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cargo, "metadata", "--offline", "--no-deps", "--format-version", "1")
	cmd.Dir = copyPath
	cmd.Env = env.List()
	output, err := cmd.Output()
	if err != nil {
		return "cargo metadata failed; target-dir cannot be resolved safely"
	}
	var metadata struct {
		TargetDirectory string `json:"target_directory"`
	}
	if json.Unmarshal(output, &metadata) != nil || metadata.TargetDirectory == "" {
		return "cargo metadata failed; target-dir cannot be resolved safely"
	}
	resolved, err := canonicalPath(resolveRelative(copyPath, metadata.TargetDirectory))
	if err != nil {
		return "cargo metadata failed; target-dir cannot be resolved safely"
	}
	if inside(sourcePath, resolved) {
		return "cargo metadata puts target_directory inside the source tree"
	}
	return ""
}

func textualCargoTargetInsideSource(copyPath, sourcePath string, reads mutationGuardReadSet) string {
	parse := func(contents string) (string, bool, bool) {
		inBuild := false
		foundUnreadable := false
		for _, line := range strings.Split(contents, "\n") {
			withoutComment := stripTomlComment(line)
			if match := regexp.MustCompile(`^` + jsSpaceClass + `*\[` + jsSpaceClass + `*([^\]]+)\]` + jsSpaceClass + `*$`).FindStringSubmatch(withoutComment); match != nil {
				inBuild = strings.TrimSpace(match[1]) == "build"
				continue
			}
			if !inBuild {
				if unresolvedCargoTarget.MatchString(withoutComment) {
					foundUnreadable = true
				}
				continue
			}
			assignment := `target-dir`
			basic := regexp.MustCompile(`^` + jsSpaceClass + `*` + assignment + jsSpaceClass + `*=` + jsSpaceClass + `*"((?:\\[^\r\x{2028}\x{2029}]|[^"\\])*)"` + jsSpaceClass + `*$`).FindStringSubmatch(withoutComment)
			literal := regexp.MustCompile(`^` + jsSpaceClass + `*` + assignment + jsSpaceClass + `*=` + jsSpaceClass + `*'([^']*)'` + jsSpaceClass + `*$`).FindStringSubmatch(withoutComment)
			if basic != nil {
				var value string
				if json.Unmarshal([]byte(`"`+basic[1]+`"`), &value) != nil {
					return "", true, false
				}
				return value, true, true
			}
			if literal != nil {
				return literal[1], true, true
			}
			if unresolvedCargoTarget.MatchString(withoutComment) {
				foundUnreadable = true
			}
		}
		if foundUnreadable {
			return "", true, false
		}
		return "", false, true
	}
	for _, config := range reads.Ancestors {
		dir := config.TargetBase
		for _, file := range config.Files {
			relative, _ := filepath.Rel(copyPath, file)
			contents, err := platform.ReadTextFile(file)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return relative + " cannot be read"
			}
			value, found, valid := parse(contents)
			if !found {
				break
			}
			if !valid {
				return relative + " target-dir cannot be resolved safely"
			}
			if value == "" {
				break
			}
			resolved, err := canonicalPath(resolveRelative(dir, value))
			if err != nil {
				return relative + " target-dir cannot be resolved safely"
			}
			if inside(sourcePath, resolved) {
				return relative + " sets target-dir inside the source tree"
			}
			return ""
		}
	}
	for _, path := range reads.CargoHome.Files {
		file := filepath.Join(reads.CargoHome.Directory, filepath.Base(path))
		contents, err := platform.ReadTextFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return file + " cannot be read"
		}
		value, found, valid := parse(contents)
		if !found {
			break
		}
		if !valid {
			return file + " target-dir cannot be resolved safely"
		}
		if value == "" {
			break
		}
		resolved, err := canonicalPath(resolveRelative(reads.CargoHome.TargetBase, value))
		if err != nil {
			return file + " target-dir cannot be resolved safely"
		}
		if inside(sourcePath, resolved) {
			return file + " sets target-dir inside the source tree"
		}
		return ""
	}
	return ""
}

var unresolvedCargoTarget = regexp.MustCompile(`(?i)target-dir|target\\u|"target`)

func stripTomlComment(line string) string {
	quote := byte(0)
	escaped := false
	for i := 0; i < len(line); i++ {
		char := line[i]
		if quote == '"' && escaped {
			escaped = false
			continue
		}
		if quote == '"' && char == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			}
			continue
		}
		if char == '"' || char == '\'' {
			quote = char
		} else if char == '#' {
			return line[:i]
		}
	}
	return line
}
