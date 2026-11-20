# Running Swarm in isolation

Use this for any e2e, probe or experiment that runs the `swarm` binary with its own data folder, and for any Go test that could reach `swarm install`, `swarm uninstall`, `launchctl`, `systemctl` or `loginctl`.

## Why a scratch `SWARM_HOME` is not enough

`SWARM_HOME` (or `--home`) moves Swarm's data. It does not move the machine-wide things Swarm also owns:

- the launchd labels `dev.swarm.daemon` and `dev.swarm.updater` (`internal/install/launchd.go` `Label`, `internal/install/legacy.go` `UpdaterLabel`): they are fixed, so `launchctl bootout gui/<uid>/dev.swarm.daemon` from a scratch run stops the **live** daemon, whatever `HOME` is;
- on Linux, the systemd user unit `dev.swarm.daemon.service` (`internal/install/service.go` `Unit`): `systemctl --user disable --now dev.swarm.daemon.service` stops the live daemon the same way. A fake `HOME` moves the unit file only when `XDG_CONFIG_HOME` is unset or scratch too (the unit goes to `$XDG_CONFIG_HOME/systemd/user` when it is set), and it never moves the running user manager, which still has the live unit loaded;
- the agents' own configuration (`~/.claude`, `~/.codex`, `~/.cursor`, `~/.gemini`, `~/.grok`, muse) and `~/.local/bin/swarm`;
- `~/.swarm/research` (agents write research notes there regardless of `SWARM_HOME`).

BUG-100: an isolated e2e that ran `SWARM_HOME=<scratch> swarm install` would have booted the live daemon out.

## What the code does now

When the swarm home is not `~/.swarm`, `swarm install` and `swarm uninstall` set `install.Config.Isolated` and then:

- write no plist or unit and run no `launchctl`, `systemctl` or `loginctl` command (`install.Install`, `install.Uninstall`, `install.RemoveLegacyShared`);
- touch no agent configuration, plugin, skill link or `~/.local/bin/swarm` (`install.Agents`, `install.Uninstall`);
- say so and name the flag that opts in.

`--live` opts in (`swarm install --live`, `swarm uninstall --live`, `swarm migrate --live`): it acts on the live service and the real configuration even though the home is not `~/.swarm`. Only use it with a fake `HOME` **and** a `launchctl` stub (macOS) or `systemctl` and `loginctl` stubs (Linux) on `PATH`. The default home is unchanged.

## Safe setup for a real binary (e2e, probes)

```sh
S=$(mktemp -d)                       # keep it short: unix socket paths have a length limit
mkdir -p "$S/shim" "$S/home" "$S/fakehome"
cat >"$S/shim/launchctl" <<EOF2
#!/bin/sh
echo "launchctl \$*" >> $S/launchctl-shim.log
EOF2
chmod +x "$S/shim/launchctl"
env -i PATH="$S/shim:/usr/bin:/bin:/opt/homebrew/bin" HOME="$S/fakehome" USER=tester \
  SWARM_HOME="$S/home" SWARM_TMUX_SOCKET=swarm-e2e-mine swarm install
```

- On Linux, stub `systemctl` and `loginctl` the same way (same script, one file each) and put Homebrew on Linux's prefix in place of `/opt/homebrew/bin`; the shim log must stay absent just the same. `env -i` also clears `XDG_CONFIG_HOME`; if you don't use it, unset `XDG_CONFIG_HOME` or point it inside `$S`, or a `--live` run writes the unit into your real `~/.config/systemd/user`.
- `HOME` is a fake folder, `PATH` starts with the `launchctl` stub, and `SWARM_HOME` is scratch. Do not set `SWARM_USAGE=live`.
- After the run, `$S/launchctl-shim.log` must not exist (an isolated run never calls `launchctl`). If you pass `--live`, it lists exactly what would have hit the live job.
- Never use `--live` without the fake `HOME` and the stub.
- An agent started inside the run can still guess the real home from its work-dir path and write to `~/.swarm/research`; check the live folder afterwards and remove strays.
- `scripts/e2e.sh` follows this rule for the fake adapter: it never runs `swarm install`.
- An isolated `swarm install` writes no plist and starts no daemon, so here it only proves nothing live is touched. To run an isolated daemon, start it yourself under the same `env -i ...` prefix: `swarm daemon --home "$S/home" --port 17778` (any free port other than 7777), and stop it with Ctrl-C.
- `swarm migrate` (the one-time 1.x upgrade, macOS only: on Linux it prints that there is nothing to migrate and exits 0 without touching anything) always acts on the fixed launchd labels, so it refuses a non-default `--home` (exit 2, in every mode including `--dry-run`) unless you pass `--live`. Never run it for real with a scratch home; `TestMigrateRefusesANonDefaultHomeWithoutLive` (`cmd/swarm`) guards the refusal.

## Safe setup for Go tests

- Never call the real `install.Install`, `install.Uninstall`, `install.Agents` or `migrate` with `execx.Run` in a test. Inject `execx.Fake` (`internal/install/isolated_test.go`) and a fake `HOME` (`fakeHome` in `internal/install/codex_test.go`). In `cmd/swarm` tests, stub `installLaunchd`, `installAgents`, `installUninstall` and `migrateRun`.
- `execx.Run`, `execx.RunFor`, `execx.RunEnv`, `execx.Start`, `execx.StartEnv`, `execx.StartMerged` and `execx.StartDetached` return an error for `launchctl`, `systemctl` and `loginctl` when running under `go test` (`refuseUnderTest`, guarded by `TestRealRunnersRefuseLaunchctlUnderGoTest`). A test that hits it forgot to inject a fake; fix the test, do not weaken the guard. `cmd/swarm` tests that depend on the service manager pin it with `pinGOOS`.
- If a test run might have touched launchd, check at once: `launchctl print gui/$(id -u)/dev.swarm.daemon | grep -E 'state|pid'`. To restore it: `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.swarm.daemon.plist`.
- On Linux, check with `systemctl --user status dev.swarm.daemon.service`, and restore it with `systemctl --user enable --now dev.swarm.daemon.service`.

## Restarting the daemon after `make install`

The user restarts the live daemon with one command: `make restart-daemon` (it runs `make install-daemon`, then `~/.swarm/bin/swarm restart`). `swarm restart` is `bootout`, a wait until `launchctl print gui/$(id -u)/dev.swarm.daemon` fails, then `bootstrap`, retried on exit 5 for up to 30 s (`launchdService.Start`, `internal/install/service.go`). On Linux it is `systemctl --user restart`. It leaves the agents' configuration alone, unlike `swarm install`. An agent never runs it, nor `kickstart`, `bootout`, `bootstrap` or `kill` on the live job: the daemon hosts every running agent, and the restart drops the board for some seconds.

Two failures used to make the restart unreliable (BUG-165, BUG-188; evidence: the crash reports `~/Library/Logs/DiagnosticReports/swarm-*.ips`):

- **`launchctl kickstart -k` after replacing the binary.** launchd respawns `~/.swarm/bin/swarm` under the job's existing launch constraint (a managed LWCR from Background Task Management). Each build has a new cdhash and the daemon is self-signed with no Team ID, so the stale constraint rejects the new binary and the kernel kills it: `EXC_CRASH`, `SIGKILL (Code Signature Invalid)`, `Launch Constraint Violation`. launchd then shows `spawn failed`, `last exit reason = OS_REASON_CODESIGNING` and `needs LWCR update | managed LWCR`. KeepAlive respawns it every 10 s, so it often recovers after 10-30 s, and sometimes does not. A job registered fresh by `bootstrap` carries no stale constraint. This is inferred from crash reports and launchd's properties; it was not reproduced on a throwaway job.
- **`bootout` followed straight away by `bootstrap`.** `bootout` returns once the daemon has been signalled (0.03 s); the job stays loaded until the daemon has drained, which takes seconds. A `bootstrap` in that window fails with `Bootstrap failed: 5: Input/output error`. This was reproduced: success came 3.2 s after `bootout` on a job that needs 3 s to exit.

Never: `launchctl kickstart -k gui/$(id -u)/dev.swarm.daemon` after replacing the binary, and back-to-back `bootout; bootstrap`. Use `make restart-daemon`. Do not write the old `kickstart -k` line in new plans, specs or briefs (the ones in `docs/plans` and `docs/specs` are history).

Detect it: `launchctl print gui/$(id -u)/dev.swarm.daemon | grep -E 'state =|last exit|properties|pid'`. `spawn failed` plus `OS_REASON_CODESIGNING` plus `needs LWCR update` is the first failure; look for `"indicator":"Launch Constraint Violation"` in `~/Library/Logs/DiagnosticReports/swarm-*.ips`. To recover, run `swarm restart` (or `make restart-daemon`), not another `kickstart`.

Traps:

- `codesign --verify --strict ~/.swarm/bin/swarm` passes and the binary runs by hand, so it looks like a signing bug. It is not: the binary violates the job's launch constraint, not its own signature.
- It can recover by itself in 10-30 s through the throttled respawn, so a short outage does not prove the restart worked. Check `state = running` and a new `pid`.
- Exit 5 from `bootstrap` means "still unloading", not a bad plist. A bad plist gives exit 122 or similar, and `Start` does not retry it.
- Tests for this path script the runner (`scripted` in `internal/install/service_start_test.go`) and replace `launchdSleep`, so they never wait or touch launchd. `execx.Fake` returns one answer per command, so it cannot express "print succeeds twice, then fails"; use `scripted`. Any `launchctl print` error counts as "unloaded", so a `Fake` without a `print` answer reads as already gone.
- Signing the daemon with a Developer ID would let the constraint survive rebuilds. That is a separate decision.

## Guards

| Rule | Test |
| --- | --- |
| A non-default home never reaches launchd | `TestIsolatedInstallNeverTouchesLaunchd`, `TestIsolatedUninstallNeverTouchesLaunchd`, `TestIsolatedAgentsNeverBootsOutTheUpdater` (`internal/install`) |
| A non-default home never reaches systemd | `TestIsolatedSystemdInstallAndUninstallNeverTouchSystemd` (`internal/install`) |
| A non-default home never touches agent configs | `TestIsolatedInstallAndUninstallLeaveUserAgentConfigsAlone` |
| `swarm migrate` refuses a non-default home without `--live` | `TestMigrateRefusesANonDefaultHomeWithoutLive` (`cmd/swarm`) |
| The default home is unchanged | `TestNonIsolatedInstallStillTouchesLaunchd`, `TestInstallAndUninstallIsolateANonDefaultHome` (`cmd/swarm`) |
| Restart waits for the unload, retries bootstrap on exit 5 and never kickstarts | `TestStart*`, `TestRestart*` (`internal/install`), `TestRestartCommand*` (`cmd/swarm`) |
| Tests cannot run the real `launchctl`, `systemctl` or `loginctl` | `TestRealRunnersRefuseLaunchctlUnderGoTest` (`internal/execx`) |
