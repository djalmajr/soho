package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/djalmajr/soho/internal/install"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/update"
)

// updateInstallTimeout bounds the install half of soho update (both downloads
// and the local replacement), on top of the core's per-request timeout.
const updateInstallTimeout = 10 * time.Minute

// selfExecutable resolves the running binary: update installs into its
// directory, the same destination `soho install` uses by default. Tests
// replace it with a path inside their fixture.
var selfExecutable = os.Executable

// cmdUpdate checks for and installs the newest release of the running
// binary's channel. The channel comes from the binary's own version string
// (a pre-release suffix follows rc, everything else stable) unless --channel
// says otherwise; the resolver and its tests live in internal/update.
//
// --check installs nothing and prints one text line (or one JSON object with
// --json), exiting 0 when the check completed and 1 when it could not.
// Without --check it installs the newest of the channel through the reviewed
// install core (SHA256SUMS verified, into the running binary's directory),
// refusing a version lower than the running one unless --version names it.
// The running executable must be the canonical destination install writes:
// a renamed binary is refused before any request or download.
func cmdUpdate(args []string, env platform.Env) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	check := fs.Bool("check", false, "check for a newer release and install nothing")
	jsonOut := fs.Bool("json", false, "print the check as one JSON object (with --check)")
	channel := fs.String("channel", "", "release channel: stable or rc (default: the running version's)")
	versionFlag := fs.String("version", "", "install this release tag (v-prefixed) instead of the channel's newest")
	if err := fs.Parse(args); err != nil {
		platform.Die("update: "+err.Error(), 2)
	}
	if fs.NArg() > 0 {
		platform.Die(fmt.Sprintf("update: unexpected arguments: %v", fs.Args()), 2)
	}
	if *jsonOut && !*check {
		platform.Die("update: --json applies to --check", 2)
	}
	if *check && *versionFlag != "" {
		platform.Die("update: --check installs nothing; drop --version", 2)
	}
	if *versionFlag != "" && *channel != "" {
		platform.Die("update: --version names the release; --channel applies to the channel check", 2)
	}
	selected, err := update.ParseChannel(*channel)
	if err != nil {
		platform.Die(err.Error(), 2)
	}
	effective := selected
	if effective == "" {
		effective = update.ChannelFor(version)
	}

	if *check {
		return updateCheck(effective, *jsonOut, env)
	}

	// The update replaces the running binary in its own directory, so the
	// running executable must be the canonical name install writes there: a
	// renamed sibling would otherwise replace a different installation.
	// Refused before the release list and before any download.
	running, err := selfExecutable()
	if err != nil {
		platform.Die("update: cannot resolve the running binary: "+err.Error(), 1)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(running); resolveErr == nil {
		running = resolved
	}
	dir := filepath.Dir(running)
	if canonical := install.BinaryName(); filepath.Base(running) != canonical {
		platform.Die(fmt.Sprintf("update: the running binary is %s, not %s; run soho install --dir %s or rename it", running, filepath.Join(dir, canonical), dir), 2)
	}

	tag := *versionFlag
	if tag == "" {
		ctx, cancel := context.WithTimeout(context.Background(), update.RequestTimeout)
		res, err := update.CheckChannel(ctx, update.CheckOptions{Current: version, Channel: effective, API: env.Get(install.EnvReleasesAPI)})
		cancel()
		if err != nil {
			platform.Die("update: could not check for updates: "+err.Error(), 1)
		}
		tag = res.Newest.Tag
		if update.Same(tag, version) {
			fmt.Fprintf(platform.Stdout, "soho %s · already up to date (%s)\n", version, res.Channel)
			return 0
		}
		if update.Lower(tag, version) {
			platform.Die(fmt.Sprintf("update: the newest %s release %s is lower than the running %s; install it with: soho update --version %s", res.Channel, tag, version, tag), 2)
		}
	} else if err := install.ValidateVersion(tag); err != nil {
		platform.Die(err.Error(), 2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), updateInstallTimeout)
	res, err := install.Run(ctx, install.Options{
		Version:     tag,
		InstallDir:  dir,
		ReleaseBase: env.Get(install.EnvReleaseBase),
	})
	cancel()
	if err != nil {
		platform.Die(err.Error(), 1)
	}
	fmt.Fprintf(platform.Stdout, "installed %s\n", res.Destination)
	if line := movedAsideLine(res); line != "" {
		fmt.Fprintln(platform.Stdout, line)
	}
	for _, line := range strandedLines(res) {
		fmt.Fprintln(platform.Stdout, line)
	}
	fmt.Fprintf(platform.Stdout, "sha256 %s\n", res.Digest)
	fmt.Fprintf(platform.Stdout, "release %s\n", res.ReleaseURL)
	fmt.Fprintln(platform.Stdout, "next: run 'soho setup --local' in each project to refresh the instruction block")
	if line := skillUpdateAdvice(env, tag); line != "" {
		fmt.Fprintln(platform.Stdout, line)
	}
	return 0
}

// updateCheckJSON is the --check --json document. Newest, Published and Error
// are null when they do not apply (a failed check, or no release of the
// channel).
type updateCheckJSON struct {
	Schema          int     `json:"schema"`
	Current         string  `json:"current"`
	Channel         string  `json:"channel"`
	Newest          *string `json:"newest"`
	Published       *string `json:"published"`
	UpdateAvailable bool    `json:"update_available"`
	Checked         bool    `json:"checked"`
	Error           *string `json:"error"`
}

// updateCheck prints the check in text or JSON. It installs nothing and writes
// nothing: exit 0 when the check completed (with or without an update), 1 when
// it could not run.
func updateCheck(channel string, jsonOut bool, env platform.Env) int {
	ctx, cancel := context.WithTimeout(context.Background(), update.RequestTimeout)
	res, err := update.CheckChannel(ctx, update.CheckOptions{Current: version, Channel: channel, API: env.Get(install.EnvReleasesAPI)})
	cancel()
	if jsonOut {
		doc := updateCheckJSON{Schema: 1, Current: version, Channel: channel}
		if err != nil {
			reason := err.Error()
			doc.Error = &reason
		} else {
			tag := res.Newest.Tag
			doc.Newest = &tag
			doc.UpdateAvailable = res.UpdateAvailable
			doc.Checked = true
			if date := res.Newest.PublishedDate(); date != "" {
				doc.Published = &date
			}
		}
		out, marshalErr := json.Marshal(doc)
		if marshalErr != nil {
			platform.Die("update: encoding the check: "+marshalErr.Error(), 1)
		}
		fmt.Fprintf(platform.Stdout, "%s\n", out)
		if err != nil {
			return 1
		}
		return 0
	}
	if err != nil {
		fmt.Fprintf(platform.Stdout, "soho %s · could not check for updates: %s\n", version, err)
		return 1
	}
	if res.UpdateAvailable {
		when := ""
		if date := res.Newest.PublishedDate(); date != "" {
			when = " (" + date + ")"
		}
		// The advice must re-select the channel the check used: on the
		// binary's own channel the bare command says the same thing.
		advice := "soho update"
		if res.Channel != update.ChannelFor(version) {
			advice = "soho update --channel " + res.Channel
		}
		fmt.Fprintf(platform.Stdout, "soho %s · newest %s: %s%s · run: %s\n", version, res.Channel, res.Newest.Tag, when, advice)
		return 0
	}
	fmt.Fprintf(platform.Stdout, "soho %s · up to date (%s)\n", version, res.Channel)
	return 0
}

// skillUpdateAdvice is the one-line instruction to update the home skill to
// the release just installed. It prints the command and never runs it, and it
// makes no comparison: the still-running process carries the old embedded
// skill, so only the freshly installed tag can be named truthfully. A missing
// home skill is not this line's business (setup creates it).
func skillUpdateAdvice(env platform.Env, tag string) string {
	home := platform.HomeDir(platform.Current(), env)
	if home == "" {
		return ""
	}
	dir := filepath.Join(home, ".agents", "skills", "soho")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	return fmt.Sprintf("update the home skill to this release: npx -y skills add %q --global --skill soho -y", "djalmajr/soho#"+tag)
}
