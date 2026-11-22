# headset-button

Hands-free dictation from the button on a **Corsair VOID WIRELESS v2** headset, on
Omarchy (Hyprland) with Superwhisper as the dictation backend.

| Press  | Does                                   |
|--------|----------------------------------------|
| Single | Play/pause (passed through unchanged)  |
| Double | `omarchy-dictation toggle`: start or stop dictation |
| Triple | Enter, to submit what was dictated    |

Volume and any other keys from the headset pass through unchanged.

## How it works

The headset does its own multi-press detection and sends one key per gesture on
the receiver's "Consumer Control" input device:

| Gesture | Key sent              | Code |
|---------|-----------------------|------|
| Single  | `KEY_PLAYPAUSE`       | 164  |
| Double  | `KEY_NEXTSONG`        | 163  |
| Triple  | `KEY_PREVIOUSSONG`    | 165  |

Its timing is fixed in the headset, so double and triple presses need to be
quick. A long press sends nothing to the laptop (the headset beeps and handles
it itself).

`headset-button` finds the device by name, grabs it (`EVIOCGRAB`) so nothing
else sees the raw keys, and re-sends them through a uinput virtual device named
`headset-button (VOID v2)`, swapping next track for the dictation toggle and
previous track for Enter. If the receiver is unplugged it waits and grabs it
again when it comes back.

Go standard library only: no third-party modules.

## Requirements

- [mise](https://mise.jdx.dev): pins Go 1.27.3 (`mise.toml`) and runs the tasks.
  `activate_aggressive` is set so mise's Go wins over a system `/usr/bin/go`.
- The user is in the `input` group, for reading `/dev/input/event*` and writing
  `/dev/uinput` (on Omarchy `/dev/uinput` is `root:input`, mode 660). Check with
  `id -nG` and `ls -l /dev/uinput`.
- `omarchy-dictation` (Omarchy) with a dictation backend set up.

The receiver shows up as USB `1b1c:2a08`, "Corsair CORSAIR VOID WIRELESS v2
Gaming Receiver". Another model needs `deviceName` in `main.go` changed, and its
key codes checked (set `HEADSET_DEBUG=1` to log every key it sends).

## Install

```bash
mise trust && mise install   # first time: trust the project, install Go
mise run install             # build, install, enable and (re)start the service
```

Tasks (shell scripts in `.mise/tasks/`):

| Task        | Does |
|-------------|------|
| `build`     | `go vet`, then build `bin/headset-button` |
| `install`   | build, copy the binary to `~/.local/bin` and the unit to `~/.config/systemd/user`, enable and restart |
| `logs`      | follow the service log |
| `uninstall` | stop and disable the service, remove the binary and unit |

The service is a systemd **user** service started with the desktop session
(`graphical-session.target`), restarted 3 s after a crash.

## Use and troubleshoot

```bash
systemctl --user status headset-button       # running?
journalctl --user -u headset-button -f       # log; each double/triple press is logged
systemctl --user restart headset-button      # after rebuilding
systemctl --user disable --now headset-button  # turn it off
```

To log every key the headset sends:

```bash
systemctl --user edit headset-button         # add: [Service] Environment=HEADSET_DEBUG=1
systemctl --user restart headset-button
```

The log should show `grabbed /dev/input/eventN` after start. If not, the device
name didn't match or the user lacks the `input` group.

## Files

- `main.go`: the service.
- `headset-button.service`: the systemd user unit.
- `mise.toml`, `.mise/tasks/`: toolchain and tasks.
- `LICENSE.md`: MIT licence.

## License

MIT, see [LICENSE.md](LICENSE.md). Text from SPDX (`spdx/license-list-data`, `text/MIT.txt`).
