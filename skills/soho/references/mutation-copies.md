# Mutation copies

## Safely enter a mutation copy

`soho mutation-copy` defaults to one JSON line with `copy`, `source`, `files`, `excluded` and `links`. The additive boolean `--print-path` prints exactly the final resolved absolute native copy path followed by one newline, the same path as the JSON `copy` field. Capture this mode when entering a copy; the default JSON is not a directory path. Warnings and registration messages stay on stderr, registration remains best effort after the same guards, and either mode has empty stdout on failure (exit 1 for a failed guard, 2 for usage/refusal, 4 for a copy I/O error). A duplicate `--print-path`, a value or an equals-form exits 2. Path mode refuses resolved paths containing CR or LF before copying contents, writing an origin marker or registering; destination preparation may already have created a directory, which is removed only when the command created it. An existing caller-owned empty destination is retained. JSON mode retains its existing path support.

Run the following sequence as a POSIX shell script from the source worktree. Every failure explicitly stops the script; it does not rely on `set -e`. Keep the variables quoted so spaces, Unicode and literal shell metacharacters survive. If the project requires a build-output setting, point it inside the captured copy before running the guard. Make edits only after the final directory change, using paths relative to the copy or rooted at `copy_dir`, never the saved source.

```sh
source_dir="$PWD"
copy_dir="$(soho mutation-copy --source "$source_dir" --print-path)" || {
    printf '%s\n' 'mutation-copy failed' >&2
    exit 1
}
if [ -z "$copy_dir" ] || [ ! -d "$copy_dir" ]; then
    printf '%s\n' 'captured copy is not a nonempty directory path' >&2
    exit 1
fi
soho mutation-guard "$copy_dir" --source "$source_dir" || {
    printf '%s\n' 'mutation-guard failed' >&2
    exit 1
}
cd -- "$copy_dir" || {
    printf '%s\n' 'entering the copy failed' >&2
    exit 1
}
# Only now edit files in the copy.
```

Use the equivalent PowerShell script on Windows. Capture stdout alone, check each native command's exit code immediately, and use literal-path operations with terminating errors so brackets and other metacharacters are preserved. Set any required build-output setting inside the captured copy before the guard, and make edits only after `Set-Location` succeeds, relative to the copy or rooted at `$copyDir`.

```powershell
$sourceDir = $PWD.Path
$copyDir = & soho mutation-copy --source $sourceDir --print-path
if ($LASTEXITCODE -ne 0) { throw 'mutation-copy failed' }
if ([string]::IsNullOrEmpty($copyDir) -or -not (Test-Path -LiteralPath $copyDir -PathType Container -ErrorAction Stop)) {
    throw 'captured copy is not a nonempty directory path'
}
& soho mutation-guard $copyDir --source $sourceDir
if ($LASTEXITCODE -ne 0) { throw 'mutation-guard failed' }
Set-Location -LiteralPath $copyDir -ErrorAction Stop
# Only now edit files in the copy.
```

The guard checks isolation when invoked; it does not reserve paths against later filesystem changes. These sequences require successful capture, directory validation, the explicit-source guard and directory entry before edits, and neither shell continues on a failed step.
