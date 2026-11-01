# Contributing

Bug reports and pull requests are welcome. For a large change, open an issue first so we can agree on the approach.

## Setup

You need Go 1.24 or newer.

```sh
go test ./...
go build -o /tmp/usb-llms .
/tmp/usb-llms --home /tmp/usb-llms-drive setup --lean --model smollm2-135m
/tmp/usb-llms --home /tmp/usb-llms-drive
```

## Test scripts

These need a real engine and model in the drive folder.

| Script | What it covers | Run it with |
|---|---|---|
| `test/usb-image.sh` | everything on a simulated exFAT drive, macOS only | `sh test/usb-image.sh` |
| `test/e2e.mjs` | the web interface in a browser | see the comment at the top of the file |
| `test/tui_pty.py` | the terminal interface | `python3 test/tui_pty.py /path/to/usb-llms` |

## Rules

- Every outside connection goes through `internal/netguard`. A new code path that dials out any other way is a bug, because it would break offline mode.
- Nothing may be written outside the drive, apart from the app window's temporary browser profile.
- Don't copy code from projects without a licence, or from copyleft projects. See `NOTICE.md`.
- Keep dependencies few, and add each new one to `NOTICE.md`.
