#!/usr/bin/env python3
"""Drives the terminal interface through a pseudo-terminal: test/tui_pty.py /path/to/usb-llms"""
import os, pty, re, select, struct, fcntl, subprocess, termios, sys, time

ANSI = re.compile(r"\x1b(\[[0-9;?<>=]*[a-zA-Z~]|\][^\x07\x1b]*(\x07|\x1b\\)|[()][A-Z0-9]|[=>])")
pid, fd = pty.fork()
if pid == 0:
    os.environ["TERM"] = "xterm-256color"
    os.execv(sys.argv[1], [sys.argv[1]])
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))
seen = ""

def pump(seconds, until=None):
    global seen
    end = time.time() + seconds
    while time.time() < end:
        r, _, _ = select.select([fd], [], [], 0.2)
        if r:
            try:
                data = os.read(fd, 65536)
            except OSError:
                return False
            if not data:
                return False
            text = data.decode("utf-8", "replace")
            # Terminal capability questions need answers, or the UI waits for them.
            if "\x1b[6n" in text:
                os.write(fd, b"\x1b[1;1R")
            if "\x1b]11;?" in text:
                os.write(fd, b"\x1b]11;rgb:0000/0000/0000\x07")
            seen += ANSI.sub("", text)
        if until and re.search(until, seen):
            return True
    return not until

def expect(pattern, seconds=30):
    global seen
    if not pump(seconds, pattern):
        print(seen[-1500:])
        sys.exit(f"did not see {pattern!r}")
    print("saw", pattern)
    seen = ""

def key(s):
    os.write(fd, s.encode())
    time.sleep(0.3)

def go_to(n):
    for _ in range(10):
        key("\x1b[A")
    for _ in range(n):
        key("\x1b[B")
    key("\r")

expect(r"Run LvW-harness", 40)
go_to(1)
expect(r"d deletes", 15)
key("\x1b")
pump(1)
go_to(2)
expect(r"Llama 3\.2", 15)
key("\x1b")
pump(1)
go_to(0)
expect(r"smollm2", 15)
key("\x1b[B")
key("\r")
pump(2)
seen = ""
key("Say hi.")
key("\r")
pump(45)
reply = seen.replace("\n", " ").strip()
assert len(reply) > 30, f"no reply on screen: {reply!r}"
print("reply tail:", reply[-120:])
key("\x03")
pump(8)
drive = os.path.dirname(os.path.dirname(os.path.realpath(sys.argv[1])))
left = subprocess.run(["pgrep", "-f", os.path.join(drive, "engine")], capture_output=True, text=True).stdout.strip()
assert not left, f"the engine is still running after quit: {left}"
print("tui test passed")
