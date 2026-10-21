package main

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/term"
)

// exampleProfile is claude-skills' process/profiles/acme.md, the profile `proc init` fills in: it keeps the example's
// phases, models and allowlists and replaces only what differs per project. Copy it over unchanged when it changes.
//
//go:embed profiles/acme.md
var exampleProfile string

// initializer is `proc init`: it links claude-skills' process skills into ~/.claude/skills, puts `process` on PATH and
// writes a project profile, so `proc install` has something to run.
type initializer struct {
	home       string
	configHome string
	path       string    // PATH, to say when the directory `process` goes in is not on it
	in         io.Reader // the answers, when the forms are accessible: one per line
	out        io.Writer
	accessible bool // plain prompts over in and out instead of the TUI forms, for screen readers and scripts
	editor     func(path string) error
	now        func() time.Time
	zone       string // my IANA time zone, for the profile; empty keeps the example's
}

// newInitializer sets up for me, asking through the TUI forms, or through plain prompts for a screen reader
// (ACCESSIBLE set) or when the answers come from a pipe.
func newInitializer() (*initializer, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	return &initializer{
		home:       home,
		configHome: configHome,
		path:       os.Getenv("PATH"),
		in:         os.Stdin,
		out:        os.Stdout,
		accessible: os.Getenv("ACCESSIBLE") != "" || !term.IsTerminal(os.Stdin.Fd()),
		editor: func(path string) error {
			cmd := editorCommand(path)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			return cmd.Run()
		},
		now:  time.Now,
		zone: localZone(),
	}, nil
}

// localZone is this machine's IANA time zone: $TZ, else what /etc/localtime links to; empty when neither says.
func localZone() string {
	if zone := strings.TrimPrefix(os.Getenv("TZ"), ":"); zone != "" && !filepath.IsAbs(zone) {
		return zone
	}
	target, err := os.Readlink("/etc/localtime")
	if err != nil {
		return ""
	}
	_, zone, _ := strings.Cut(target, "zoneinfo/")
	return zone
}

// processSkills are the skills of claude-skills' process pipeline, besides each of its sweep-* sweepers.
var processSkills = []string{"brief", "execute", "next-work", "process", "triage"}

func (i *initializer) init() error {
	if i.accessible {
		i.in = &lineReader{r: bufio.NewReader(i.in)}
	}
	checkout, err := i.checkout()
	if err != nil {
		return err
	}
	if err := i.installPipeline(checkout); err != nil {
		return err
	}
	a, err := i.createProfile()
	if err != nil {
		return err
	}
	if err := i.dryRun(checkout, a.name); err != nil {
		return err
	}
	i.nextSteps(a)
	return nil
}

// nextSteps names what a live run of the profile needs that proc cannot set up, then proc install.
func (i *initializer) nextSteps(a answers) {
	fmt.Fprintln(i.out, "proc: before a live run:")
	if slices.Contains(a.sources, "gmail") {
		fmt.Fprintln(i.out, "  - connect Gmail with modify scope: triage archives noise")
		if a.noiseLabel != "" {
			fmt.Fprintf(i.out, "  - create the %q label in Gmail: triage files noise under it, and cannot create it\n", a.noiseLabel)
		}
	}
	// triage, next-work and the executor read Linear whatever the sources.
	fmt.Fprintf(i.out, "  - add the Linear MCP server (linear-server) to %s\n", path.Join(a.workdir, ".mcp.json"))
	fmt.Fprintln(i.out, "proc: then run proc install to sweep on a timer")
}

// checkout is my claude-skills checkout: the one I named last time, else where I say it is, remembered for next time.
func (i *initializer) checkout() (string, error) {
	remembered := filepath.Join(i.configHome, "proc", "claude-skills")
	if data, err := os.ReadFile(remembered); err == nil {
		dir := strings.TrimSpace(string(data))
		if isCheckout(dir) == nil {
			fmt.Fprintln(i.out, "proc: using the claude-skills checkout at", dir)
			return dir, nil
		}
	}
	var dir string
	field := huh.NewInput().Title("Where is your claude-skills checkout?").Value(&dir).Validate(func(s string) error {
		return isCheckout(i.expand(s))
	})
	if err := i.ask(huh.NewGroup(field)); err != nil {
		return "", err
	}
	dir, err := filepath.Abs(i.expand(dir))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(remembered), 0o755); err != nil {
		return "", err
	}
	return dir, os.WriteFile(remembered, []byte(dir+"\n"), 0o644)
}

func isCheckout(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "process", "process.py")); err != nil {
		return errors.New("no process/process.py there")
	}
	return nil
}

// installPipeline links each process skill to the checkout, so a `git pull` there upgrades it, and `process` onto
// PATH through the linked process skill. It leaves alone whatever is already there, and says what it did.
func (i *initializer) installPipeline(checkout string) error {
	sweepers, err := filepath.Glob(filepath.Join(checkout, "sweep-*"))
	if err != nil {
		return err
	}
	skills := slices.Clone(processSkills)
	for _, dir := range sweepers {
		skills = append(skills, filepath.Base(dir))
	}
	skillsDir := filepath.Join(i.home, ".claude", "skills")
	for _, skill := range skills {
		if err := i.link(filepath.Join(checkout, skill), filepath.Join(skillsDir, skill)); err != nil {
			return err
		}
	}
	bin := filepath.Join(i.home, ".local", "bin")
	if err := i.link(filepath.Join(skillsDir, "process", "process.py"), filepath.Join(bin, "process")); err != nil {
		return err
	}
	if !slices.Contains(filepath.SplitList(i.path), bin) {
		fmt.Fprintf(i.out, "proc: warning: %s is not on your PATH; add it, or process and the timed runs will not be found\n", bin)
	}
	return nil
}

// link makes path a symlink to target, unless it is one already. Anything else at path stays: it may be a copy I made
// by hand, and it is mine to remove.
func (i *initializer) link(target, path string) error {
	if current, err := os.Readlink(path); err == nil && current == target {
		fmt.Fprintf(i.out, "proc: already linked: %s\n", path)
		return nil
	} else if _, err := os.Lstat(path); err == nil {
		fmt.Fprintf(i.out, "proc: left %s as it is; remove it and run proc init again to link it to %s\n", path, target)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.Symlink(target, path); err != nil {
		return err
	}
	fmt.Fprintf(i.out, "proc: linked %s -> %s\n", path, target)
	return nil
}

// answers are what differs per project; every list is comma-separated.
type answers struct {
	name, workdir, timezone                                    string
	sources                                                    []string
	repo, team, workspace, watchlist                           string
	account, manager, noiseLabel                               string
	keywords, matchChannels, senders, people, channels, labels string
}

var sources = []string{"github", "linear", "slack", "gmail", "calendar", "meetings"}

// createProfile asks for a project's values and writes its profile from the example's.
func (i *initializer) createProfile() (answers, error) {
	a := answers{sources: sources, noiseLabel: "Noise", timezone: i.zone}
	name := huh.NewInput().Title("Project name").Value(&a.name).Validate(func(s string) error {
		if !profileName.MatchString(s) {
			return errors.New("lowercase letters, digits and dashes, starting with a letter or digit")
		}
		return nil
	})
	path := ""
	for replace := false; !replace; {
		if err := i.ask(huh.NewGroup(name)); err != nil {
			return a, err
		}
		path = filepath.Join(i.configHome, "process", "profiles", a.name+".md")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		confirm := huh.NewConfirm().Title(fmt.Sprintf("%s already exists. Replace it?", path)).Value(&replace)
		if err := i.ask(huh.NewGroup(confirm)); err != nil {
			return a, err
		}
	}
	a.workdir = "~/code/" + a.name
	// The profile waits in a scratch config directory until process accepts it: no invalid profile reaches mine.
	scratch, err := os.MkdirTemp("", "proc-init-")
	if err != nil {
		return a, err
	}
	defer os.RemoveAll(scratch)
	draft := filepath.Join(scratch, "process", "profiles", a.name+".md")
	for {
		if err := i.ask(huh.NewGroup(
			huh.NewInput().Title("Workdir: headless phases run here, so its .mcp.json servers load").Value(&a.workdir),
			huh.NewMultiSelect[string]().Title("Sources").Options(huh.NewOptions(sources...)...).Value(&a.sources),
		)); err != nil {
			return a, err
		}
		if err := i.ask(i.questions(&a)...); err != nil {
			return a, err
		}
		text, err := renderProfile(a, i.now())
		if err != nil {
			return a, err
		}
		if err := os.MkdirAll(filepath.Dir(draft), 0o755); err != nil {
			return a, err
		}
		if err := os.WriteFile(draft, []byte(text), 0o644); err != nil {
			return a, err
		}
		problem, err := i.check(scratch)
		if err != nil {
			return a, err
		}
		if problem == "" {
			break
		}
		fmt.Fprintf(i.out, "%s\nproc: process refused the profile; answer again\n", strings.ReplaceAll(problem, draft, path))
	}
	for {
		fmt.Fprintln(i.out, "proc: opening the profile in $EDITOR for a final look")
		if err := i.editor(draft); err != nil {
			return a, fmt.Errorf("the editor failed (%w); %s was not written", err, path)
		}
		problem, err := i.check(scratch)
		if err != nil {
			return a, err
		}
		if problem == "" {
			break
		}
		fmt.Fprintf(i.out, "%s\nproc: process refused the edit; fix it in the editor\n", strings.ReplaceAll(problem, draft, path))
	}
	text, err := os.ReadFile(draft)
	if err != nil {
		return a, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return a, err
	}
	if err := os.WriteFile(path, text, 0o644); err != nil {
		return a, err
	}
	fmt.Fprintln(i.out, "proc: wrote", path)
	return a, nil
}

// process runs the process CLI proc init put on PATH, as the shell will, whether or not PATH has it yet: its headless
// phases call it too.
func (i *initializer) process(env []string, args ...string) *exec.Cmd {
	bin := filepath.Join(i.home, ".local", "bin")
	cmd := exec.Command(filepath.Join(bin, "process"), args...)
	cmd.Env = append(os.Environ(), append([]string{"PATH=" + bin + string(filepath.ListSeparator) + i.path}, env...)...)
	return cmd
}

// dryRun offers a `process run` of the new profile on claude-skills' fixtures, in a scratch state directory that it
// removes afterwards: the queue in ~/.local/state/process stays as it was.
func (i *initializer) dryRun(checkout, name string) error {
	run := false
	confirm := huh.NewConfirm().Value(&run).Title("Dry-run the profile on claude-skills' fixtures now?").
		Description("No service is called, but each phase runs as headless Claude, which takes a few minutes.")
	if err := i.ask(huh.NewGroup(confirm)); err != nil || !run {
		return err
	}
	state, err := os.MkdirTemp("", "proc-dry-run-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(state)
	cmd := i.process([]string{"XDG_STATE_HOME=" + state, "XDG_CONFIG_HOME=" + i.configHome},
		"run", "--fixtures", filepath.Join(checkout, "process", "fixtures"), "--project", name)
	cmd.Stdout, cmd.Stderr = i.out, i.out
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(i.out, "proc: the dry run failed: %v\n", err)
	}
	brief := filepath.Join(state, "process", "brief.md")
	if data, err := os.ReadFile(brief); err == nil {
		fmt.Fprintf(i.out, "proc: the dry run's brief, %s:\n\n%s\n", brief, data)
	}
	done := huh.NewNote().Title("Its scratch state directory goes next.").Next(true).NextLabel("Remove it")
	return i.ask(huh.NewGroup(done))
}

// check runs `process profiles` over the profiles in configHome, returning what it refuses, if anything.
func (i *initializer) check(configHome string) (string, error) {
	out, err := i.process([]string{"XDG_CONFIG_HOME=" + configHome}, "profiles").CombinedOutput()
	if exit := (*exec.ExitError)(nil); errors.As(err, &exit) {
		return strings.TrimSpace(string(out)), nil
	}
	return "", err
}

var profileName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// questions are the values the chosen sources need, then the match and urgency rules.
func (i *initializer) questions(a *answers) []*huh.Group {
	list := func(title string, value *string) huh.Field {
		return huh.NewInput().Title(title).Description("comma-separated").Value(value)
	}
	var groups []*huh.Group
	if slices.Contains(a.sources, "github") {
		groups = append(groups, huh.NewGroup(huh.NewInput().Title("GitHub repo (owner/name)").Value(&a.repo)))
	}
	if slices.Contains(a.sources, "linear") {
		groups = append(groups, huh.NewGroup(huh.NewInput().Title("Linear team key").Value(&a.team)))
	}
	if slices.Contains(a.sources, "slack") {
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title("Slack workspace").Value(&a.workspace),
			list("Slack channels swept even without a mention (#name)", &a.watchlist),
		))
	}
	if usesGoogle(a.sources) {
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title("Google account (Gmail, Calendar, Meetings)").Value(&a.account),
			huh.NewInput().Title("Your manager's email: meetings with them are never declined").Value(&a.manager),
			huh.NewInput().Title("Gmail label noise is archived under").Value(&a.noiseLabel),
		))
	}
	return append(groups,
		huh.NewGroup(
			list("Match: keywords that mark a shared Slack or Google item as this project's", &a.keywords),
			list("Match: Slack channels (#name)", &a.matchChannels),
			list("Match: senders, by name, address or suffix (@acme.example)", &a.senders),
		),
		huh.NewGroup(
			list("Urgent: people whose DMs and mentions interrupt you", &a.people),
			list("Urgent: Slack channels where a mention interrupts you (#incident-*)", &a.channels),
			list("Urgent: PR labels that make a review request interrupt you", &a.labels),
		),
	)
}

func usesGoogle(sources []string) bool {
	return slices.ContainsFunc(sources, func(s string) bool { return s == "gmail" || s == "calendar" || s == "meetings" })
}

// ask runs one form: the TUI, or plain prompts when accessible.
func (i *initializer) ask(groups ...*huh.Group) error {
	form := huh.NewForm(groups...).WithAccessible(i.accessible)
	if i.accessible {
		form = form.WithInput(i.in).WithOutput(i.out)
	}
	if err := form.Run(); err != nil {
		return err
	}
	if r, ok := i.in.(*lineReader); ok && r.eof {
		return errors.New("ran out of answers")
	}
	return nil
}

func (i *initializer) expand(path string) string {
	if rest, ok := strings.CutPrefix(path, "~"); ok && (rest == "" || rest[0] == '/') {
		return i.home + rest
	}
	return path
}

// lineReader hands out one line per Read. huh's accessible prompts each read through a new bufio.Scanner, which would
// otherwise swallow the answers to the prompts after it.
type lineReader struct {
	r   *bufio.Reader
	eof bool
}

func (l *lineReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		b, err := l.r.ReadByte()
		if err != nil {
			l.eof = true
			if n > 0 {
				return n, nil
			}
			return 0, err
		}
		p[n] = b
		n++
		if b == '\n' {
			break
		}
	}
	return n, nil
}

// renderProfile is the example profile with a project's values in place of the example's. It keeps the example's
// comments, phases, models and allowlists; a source the project does not use loses its table.
func renderProfile(a answers, now time.Time) (string, error) {
	front, body, ok := strings.Cut(strings.TrimPrefix(exampleProfile, "+++\n"), "\n+++\n")
	if !ok {
		return "", errors.New("the example profile has no +++ front matter")
	}
	values := map[string]any{
		".name": a.name, ".active": true, ".workdir": a.workdir, ".timezone": a.timezone, ".sources": a.sources,
		"github.repo": a.repo, "linear.team": a.team,
		"slack.workspace": a.workspace, "slack.watchlist": split(a.watchlist),
		"google.account": a.account, "google.manager": a.manager, "google.noise_label": a.noiseLabel,
		"match.keywords": split(a.keywords), "match.channels": split(a.matchChannels), "match.senders": split(a.senders),
		"urgency.people": split(a.people), "urgency.channels": split(a.channels), "urgency.labels": split(a.labels),
	}
	unused := map[string]bool{
		"github": !slices.Contains(a.sources, "github"),
		"linear": !slices.Contains(a.sources, "linear"),
		"slack":  !slices.Contains(a.sources, "slack"),
		"google": !usesGoogle(a.sources),
	}
	var out []string
	table, skipping := "", false
	lines := strings.Split(front, "\n")
	for len(lines) > 0 && strings.HasPrefix(lines[0], "#") { // how to use the example
		lines = lines[1:]
	}
	for n := 0; n < len(lines); n++ {
		line := lines[n]
		if strings.HasPrefix(line, "[") {
			table = strings.Trim(line, "[]")
			skipping = unused[table]
		}
		if skipping {
			continue
		}
		key, _, isKey := strings.Cut(line, " = ")
		value, set := values[table+"."+key]
		if !isKey || !set {
			out = append(out, line)
			continue
		}
		delete(values, table+"."+key)
		for strings.Count(lines[n], "[") > strings.Count(lines[n], "]") && n+1 < len(lines) { // a multi-line array
			n++
		}
		if value == "" && key == "timezone" {
			out = append(out, line)
			continue
		}
		if value == "" { // an optional value left blank
			continue
		}
		data, err := json.Marshal(value) // JSON strings and arrays of them are TOML too
		if err != nil {
			return "", err
		}
		out = append(out, key+" = "+strings.ReplaceAll(string(data), `","`, `", "`))
	}
	for key := range values {
		if table, _, _ := strings.Cut(key, "."); !unused[table] {
			return "", fmt.Errorf("the example profile has no %s", key)
		}
	}
	return "+++\n" + strings.Join(out, "\n") + "\n+++\n" + renderBody(a, body, now), nil
}

// example names the example's project in its body, so a line holding one is about acme, not mine.
var example = regexp.MustCompile(`(?i)acme|widgets|#incidents`)

// renderBody is the example's body for my project: its title and description, the example's sweeping guidance that
// is not about the example project, and a Changelog that starts today.
func renderBody(a answers, body string, now time.Time) string {
	var described []string
	for _, part := range []struct{ source, label, value string }{
		{"github", "GitHub", a.repo}, {"linear", "Linear team", a.team}, {"slack", "Slack workspace", a.workspace},
	} {
		if slices.Contains(a.sources, part.source) {
			described = append(described, fmt.Sprintf("%s `%s`", part.label, part.value))
		}
	}
	if usesGoogle(a.sources) {
		described = append(described, fmt.Sprintf("Google account `%s`", a.account))
	}
	guide := ""
	if _, rest, ok := strings.Cut(body, "## How to sweep\n"); ok {
		rest, _, _ = strings.Cut(rest, "## Changelog")
		for _, line := range strings.SplitAfter(rest, "\n") {
			if !example.MatchString(line) {
				guide += line
			}
		}
	}
	return fmt.Sprintf("\n# %s\n\n%s.\n\n## How to sweep\n%s## Changelog\n\n- %s — Created with proc init.\n",
		a.name, strings.Join(described, ", "), guide, now.Format("2006-01-02"))
}

func split(list string) []string {
	items := []string{}
	for _, item := range strings.Split(list, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}
