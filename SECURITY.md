# Security

## Reporting a vulnerability

Please don't open a public issue. Use GitHub's [private vulnerability reporting](https://github.com/Gitguy268/usb-llms/security/advisories/new) instead. You'll get an answer within a week.

## What USB LLMS protects

- The engine and the web interface listen on `127.0.0.1` only. The web interface rejects requests whose `Host` or `Origin` isn't its own, so a website can't drive it.
- `web --lan` listens on the network and requires a random token, which is printed once and never saved. That traffic isn't encrypted, so use it only on a network you trust.
- Offline mode refuses outside connections in USB LLMS's own HTTP client, and gives the engine, Open WebUI and LvW-harness a dead proxy address, so their outbound requests fail. It's a software setting and not a firewall: a program that ignores proxy settings isn't stopped by it.
- API keys are read from environment variables and never written to the drive. A key is never sent over plain `http` to another machine.
- Engine archives are checked against the release's SHA-256 list before they are used. Archive entries and links that point outside the engine folder are refused.

## What it doesn't protect

- **The drive isn't encrypted.** Anyone holding it can read your saved chats and models. Use BitLocker, FileVault or LUKS on the drive if that matters.
- **Models are data from strangers.** Check where a model comes from before you add it. Models labelled `uncensored` have had their safety refusals removed and can produce harmful or false text.
- **Running a program from a shared stick is a risk in both directions.** A computer you don't trust can change the programs on the drive.
