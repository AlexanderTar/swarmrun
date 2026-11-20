package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/execx"
)

// ServiceManager is the OS service manager that keeps the daemon running.
type ServiceManager string

const (
	// Launchd is macOS: a launch agent in the gui/<uid> domain. The zero value.
	Launchd ServiceManager = ""
	// Systemd is Linux: a systemd user unit in the user's manager.
	Systemd ServiceManager = "systemd"
)

// Service is the daemon's entry in the service manager. Install writes Render()
// to Path() and calls Start; Uninstall calls Stop, removes Path() and calls
// Removed. Every command goes through the injected runner, and execx refuses
// the real launchctl/systemctl/loginctl under `go test` (BUG-100).
type Service interface {
	Noun() string // "launch agent" or "systemd user service", for copy
	Path() string
	Render() []byte
	Commands() [][]string // what Start runs, for --dry-run
	Start(ctx context.Context, run execx.Runner, w io.Writer) error
	Stop(ctx context.Context, run execx.Runner) error
	Removed(ctx context.Context, run execx.Runner) error
}

// Service returns c's daemon entry in its service manager.
func (c Config) Service() Service {
	if c.Manager == Systemd {
		return systemdService{c}
	}
	return launchdService{c}
}

type launchdService struct{ c Config }

func (launchdService) Noun() string     { return "launch agent" }
func (s launchdService) Path() string   { return PlistPath(s.c) }
func (s launchdService) Render() []byte { return Plist(s.c) }

func (s launchdService) target() string { return fmt.Sprintf("gui/%d", s.c.UID) }

func (s launchdService) Commands() [][]string {
	return [][]string{
		{"launchctl", "bootout", s.target() + "/" + Label},
		{"launchctl", "bootstrap", s.target(), s.Path()},
	}
}

// launchdSleep pauses between launchctl polls; tests replace it so no test waits.
var launchdSleep = func(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

const (
	launchdPoll = 250 * time.Millisecond
	// launchdUnloadBound covers launchd's 20 s default ExitTimeOut: bootout
	// returns once the daemon has been signalled, and the job stays loaded
	// until the daemon has drained and exited.
	launchdUnloadBound = 30 * time.Second
	// launchdBootstrapBound is how long bootstrap may keep answering exit 5.
	launchdBootstrapBound = 30 * time.Second
)

// Start restarts the daemon: bootout, wait until launchd has unloaded the job,
// then bootstrap. Never `kickstart -k` for a replaced binary: launchd respawns
// it under the job's stale launch constraint and the kernel kills it with
// OS_REASON_CODESIGNING (docs/isolated-runs.md, "Restarting the daemon").
func (s launchdService) Start(ctx context.Context, run execx.Runner, _ io.Writer) error {
	cmds := s.Commands()
	bootout, bootstrap := cmds[0], cmds[1]
	_, err := run(ctx, bootout[0], bootout[1:]...)
	if err != nil && !notLoaded(err) {
		return fmt.Errorf("launchctl bootout: %w", err)
	}
	if err == nil {
		if err := s.waitUnloaded(ctx, run); err != nil {
			return err
		}
	}
	for waited := time.Duration(0); ; waited += launchdPoll {
		_, err := run(ctx, bootstrap[0], bootstrap[1:]...)
		if err == nil {
			return nil
		}
		if !bootstrapBusy(err) || waited >= launchdBootstrapBound {
			return fmt.Errorf("launchctl bootstrap: %w", err)
		}
		if err := launchdSleep(ctx, launchdPoll); err != nil {
			return err
		}
	}
}

// waitUnloaded polls `launchctl print` until the job is gone. Any print error
// counts as gone (launchctl exits 113 for an unknown service): bootstrap's own
// retry covers a misjudgement, and only a successful print proves it is loaded.
func (s launchdService) waitUnloaded(ctx context.Context, run execx.Runner) error {
	for waited := time.Duration(0); ; waited += launchdPoll {
		if _, err := run(ctx, "launchctl", "print", s.target()+"/"+Label); err != nil {
			return ctx.Err()
		}
		if waited >= launchdUnloadBound {
			return fmt.Errorf("launchctl bootout: %s is still loaded after %s", Label, launchdUnloadBound)
		}
		if err := launchdSleep(ctx, launchdPoll); err != nil {
			return err
		}
	}
}

// bootstrapBusy reports whether a bootstrap error is launchd's exit 5
// "Input/output error", which means the old job is still unloading.
func bootstrapBusy(err error) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 5 {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "exit status 5") || strings.Contains(msg, "input/output error")
}

func (s launchdService) Stop(ctx context.Context, run execx.Runner) error {
	if _, err := run(ctx, "launchctl", "bootout", s.target()+"/"+Label); err != nil && !notLoaded(err) {
		return fmt.Errorf("launchctl bootout %s: %w", Label, err)
	}
	return nil
}

func (launchdService) Removed(context.Context, execx.Runner) error { return nil }

// Unit is the systemd user unit's file name: the launchd label, so the
// isolation guards and docs keep one name for the daemon on both systems.
const Unit = Label + ".service"

type systemdService struct{ c Config }

func (systemdService) Noun() string   { return "systemd user service" }
func (s systemdService) Path() string { return filepath.Join(s.c.SystemdUserDir, Unit) }

func (s systemdService) Render() []byte { return SystemdUnit(s.c) }

func (s systemdService) Commands() [][]string {
	return [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", Unit},
		{"systemctl", "--user", "restart", Unit},
	}
}

func (s systemdService) Start(ctx context.Context, run execx.Runner, w io.Writer) error {
	for _, argv := range s.Commands() {
		if _, err := run(ctx, argv[0], argv[1:]...); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
		}
	}
	// Without linger the user manager, and every user service with it, stops
	// at logout. Turning it on is a machine setting, so it is only suggested.
	out, err := run(ctx, "loginctl", "show-user", fmt.Sprint(s.c.UID), "--property=Linger", "--value")
	if err == nil && strings.TrimSpace(string(out)) == "no" {
		fmt.Fprintln(w, "Linger is off, so systemd stops the daemon when you log out. To keep it running, run: loginctl enable-linger")
	}
	return nil
}

func (s systemdService) Stop(ctx context.Context, run execx.Runner) error {
	if _, err := run(ctx, "systemctl", "--user", "disable", "--now", Unit); err != nil && !unitMissing(err) {
		return fmt.Errorf("systemctl --user disable --now %s: %w", Unit, err)
	}
	return nil
}

func (s systemdService) Removed(ctx context.Context, run execx.Runner) error {
	if _, err := run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}
	return nil
}

// unitMissing reports whether a systemctl error only means the unit isn't
// there: "Unit file … does not exist." (disable) or "Unit … not loaded." (stop).
func unitMissing(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "does not exist") || strings.Contains(msg, "not loaded")
}

// SystemdUnit renders the user unit. Like the plist it is the only place the
// production switches SWARM_TMUX_SOCKET=swarm and SWARM_USAGE=live are set
// (S-1, S-4, R9), plus SWARM_NOTIFY=desktop: on Linux the daemon shows desktop
// notifications itself, which only the installed daemon may do (macOS leaves
// them to the menu bar app). systemd sets HOME and USER for user units itself.
//
// KillMode=process: the daemon starts the private tmux server, which stays
// in the unit's cgroup. The default (control-group) would kill tmux and every
// agent on each restart, crash or reinstall; launchd leaves them alone because
// tmux calls setsid. Probed on systemd 255: tmux and its panes survive a
// SIGKILL of the daemon, `systemctl --user restart` and `stop` with process,
// and die on `stop` without it. Restart=on-failure with RestartSec=10 is the
// plist's KeepAlive{SuccessfulExit: false, Crashed: true} with ThrottleInterval 10.
func SystemdUnit(c Config) []byte {
	logs := filepath.Join(c.Home, "logs")
	env := []string{"PATH=" + c.Path, "SWARM_HOME=" + c.Home, "SWARM_TMUX_SOCKET=swarm", "SWARM_USAGE=live", "SWARM_NOTIFY=desktop"}
	for i, kv := range env {
		env[i] = systemdQuote(kv, false)
	}
	// The program path is never variable-expanded, so its $ stays single.
	exec := []string{systemdQuote(c.Bin, false)}
	for _, arg := range []string{"daemon", "--home", c.Home} {
		exec = append(exec, systemdQuote(arg, true))
	}
	return []byte(fmt.Sprintf(`[Unit]
Description=Swarm daemon

[Service]
Type=simple
ExecStart=%s
WorkingDirectory=%s
Environment=%s
Restart=on-failure
RestartSec=10
KillMode=process
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`, strings.Join(exec, " "), systemdPath(c.Home), strings.Join(env, " "),
		systemdPath(filepath.Join(logs, "daemon.out.log")), systemdPath(filepath.Join(logs, "daemon.err.log"))))
}

// systemdQuote quotes one ExecStart= word or Environment= assignment
// (systemd.syntax, systemd.exec): backslash and double quote are escaped
// inside the quotes, % is a specifier everywhere, and $ is expanded only in
// ExecStart='s arguments (not its program path, nor Environment=). Checked
// against systemd 255 with a probe unit: "a$$1" ran as a$1, "q\"uo\\te" as
// q"uo\te, Environment="E1=x$y" stayed x$y, and "p%%q" became p%q.
func systemdQuote(s string, expandsDollar bool) string {
	r := []string{`\`, `\\`, `"`, `\"`, "%", "%%"}
	if expandsDollar {
		r = append(r, "$", "$$")
	}
	return `"` + strings.NewReplacer(r...).Replace(s) + `"`
}

// systemdPath escapes a path for WorkingDirectory= and append:, where
// specifiers are expanded but quotes are not parsed.
func systemdPath(p string) string { return strings.ReplaceAll(p, "%", "%%") }

// SystemdUserDir is where systemd reads user units: $XDG_CONFIG_HOME/systemd/user,
// or ~/.config/systemd/user when XDG_CONFIG_HOME is unset or not absolute.
func SystemdUserDir(userHome, xdgConfigHome string) string {
	if filepath.IsAbs(xdgConfigHome) {
		return filepath.Join(xdgConfigHome, "systemd", "user")
	}
	return filepath.Join(userHome, ".config", "systemd", "user")
}

// checkServicePaths refuses paths a unit or plist can't carry on one line.
func checkServicePaths(c Config) error {
	for _, p := range []string{c.Bin, c.Home, c.Path} {
		if strings.ContainsAny(p, "\n\r") {
			return fmt.Errorf("%q contains a line break, which the %s can't hold", p, c.Service().Noun())
		}
	}
	return nil
}

// systemdCheck is doctor's service row on Linux.
func systemdCheck(c Config) Check {
	path := c.Service().Path()
	body, err := os.ReadFile(path)
	if err != nil {
		return Check{"Service", false, "Not installed. Run swarm install."}
	}
	s := string(body)
	if !strings.Contains(s, `"daemon" "--home"`) || !strings.Contains(s, `"PATH=`) || !strings.Contains(s, "KillMode=process") {
		return Check{"Service", false, "The systemd unit is out of date. Run swarm install."}
	}
	return Check{"Service", true, path}
}
