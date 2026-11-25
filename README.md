<div align="center">

# USB LLMS

Run AI models from a USB stick on macOS, Windows and Linux, with or without the internet.

[Start](#start) · [Interfaces](#interfaces) · [Models](#models) · [Media](#media) · [Offline and online](#offline-and-online) · [LvW-harness](#lvw-harness) · [Limits](#limits)

![Licence: MIT](https://img.shields.io/badge/licence-MIT-blue) ![Go 1.24+](https://img.shields.io/badge/go-1.24%2B-00add8) ![Engine: Ollama 0.40.13](https://img.shields.io/badge/engine-Ollama%200.40.13-555) ![macOS, Linux, Windows](https://img.shields.io/badge/runs%20on-macOS%20%7C%20Linux%20%7C%20Windows-555)

<img src="docs/images/demo.webp" width="820" alt="Asking a model a question in the USB LLMS web interface, then opening the models page">

</div>

USB LLMS is one small program and a folder. Put the folder on a stick and the same models and chats work on every computer you plug it into. It can also make pictures, short videos, speech, music and 3D models from the stick, and the chat takes pictures and voice notes. Nothing gets installed on the computer, and if it already runs Ollama, that keeps running undisturbed.

I wrote it from scratch to replace two deprecated projects, [USB-Uncensored-LLM](https://github.com/techjarves/USB-Uncensored-LLM) and [Portable-AI-USB](https://github.com/techjarves/Portable-AI-USB). See [NOTICE](NOTICE.md).

Project page: [gitguy268.github.io/usb-llms](https://gitguy268.github.io/usb-llms/), also on [usb-llms.vercel.app](https://usb-llms.vercel.app).

## Start

1. Format the stick as exFAT so all three systems can use it. Plan for at least 16 GB.
2. Copy the release folder onto it.
3. While online, open the launcher for your system and choose **Set up this computer**:

| System | Open this |
|---|---|
| macOS | double-click `start.command` |
| Windows | double-click `start.bat` |
| Linux | `sh start.sh` |

Each kind of computer needs its engine once. The models are shared between all of them.

From a terminal it's:

```sh
usb-llms setup --model llama-3.2-3b     # engine for this computer, plus a first model
usb-llms run hf.co/bartowski/Llama-3.2-3B-Instruct-GGUF:Q4_K_M
```

To get the program with Homebrew instead:

```sh
brew trust gitguy268/tap
brew install gitguy268/tap/usb-llms
```

Or build it from source with [Go](https://go.dev/dl/) 1.24 or newer, and set it up in a test folder with a tiny model:

```sh
git clone https://github.com/Gitguy268/usb-llms.git
cd usb-llms
go build -o usb-llms .
./usb-llms --home ./drive setup --lean --model smollm2-135m
```

<details>
<summary>What goes where on the drive</summary>

```
USB LLMS/
  start.command  start.sh  start.bat     pick the right program for this computer
  bin/                                   usb-llms for each system
  engine/<system>/                       Ollama, added per system on demand
  models/                                every chat and audio model, shared by all systems
  media/                                 image and video weights, shared by all systems
  data/                                  settings, chats, logs and engine state
  data/media/                            what you made, and files attached in chat
  data/sidecar/                          the 3D and music engines: shared weights, a Python env per system
```

Stop the engine before you eject the stick. The terminal interface, `web`, `app`, `webui` and `lvw` stop an engine they started when you quit them. After `usb-llms run`, `pull`, `start` and the like, run `usb-llms stop`.

The program finds the folder from its own location, so the drive letter or mount point doesn't matter. `--home DIR` points it at another folder.

</details>

<details>
<summary>How much room the engine takes</summary>

`setup` picks lean unless it finds an NVIDIA or AMD GPU. Lean leaves out those GPU libraries and still uses Metal, Vulkan and the CPU. `--lean` or `--full` forces a choice.

| System | Lean | Full |
|---|---|---|
| macOS (on exFAT) | about 580 MB | the same |
| Linux arm64 | about 65 MB | about 2.3 GB |

Linux and macOS engines are unpacked as they download, so the archive never lands on the drive. Windows needs room for its 1.6 GB zip on the drive during setup.

</details>

## Interfaces

| Interface | Command | What you get |
|---|---|---|
| Terminal | `usb-llms` | menus for chat, models, mode and everything below |
| Web | `usb-llms web` | chat with pictures and voice notes, saved chats, a Create page and a model manager in your browser. Works with no internet |
| App window | `usb-llms app` | the same page in its own window, like Ollama's desktop app |
| Ollama commands | `usb-llms run`, `list`, `ps`, `show`, `rm`, `cp`, `create` | the real Ollama commands, run against this drive |
| Ollama API | `usb-llms serve` | Ollama's API on `127.0.0.1:11435`, for any tool that speaks it |
| Open WebUI | `usb-llms webui install`, then `webui start` | the full [Open WebUI](https://github.com/open-webui/open-webui), unmodified, kept on the drive |

<table>
<tr>
<td><img src="docs/images/chat.webp" alt="Chat in the web interface: each message under the name of who wrote it, and the offline switch with its status light at the top right"></td>
<td><img src="docs/images/models.webp" alt="Models page with installed models and suggested models"></td>
<td width="170"><img src="docs/images/phone.webp" alt="The web interface on a phone-sized screen"></td>
</tr>
</table>

```
 U S B  L L M S   ● offline   darwin-arm64
▮▮▯▯▯▯▯▯▯▯▯▯▯▯▯▯▯▯▯▯▯▯▯▯  2.1 GB in models, 24.0 GB free

> Chat
  Models on this drive
  Add a model
  Create a picture, video, speech, music or 3D model
  Switch to online
  Open the web interface
  Open the app window
  Open Open WebUI
  Run LvW-harness
  Quit
```

The app window needs Chrome, Edge, Brave or Chromium on the computer and falls back to a normal browser tab. Its temporary browser profile is deleted when the window closes.

To reach the chat from your phone, run `usb-llms web --lan`. It prints a link and a QR code with a one-time token. The connection isn't encrypted, so only use it on a network you trust.

## Models

```sh
usb-llms catalog                                        # suggested models
usb-llms pull llama-3.2-3b                              # by catalogue id
usb-llms pull hf:bartowski/Phi-3.5-mini-instruct-GGUF:Q4_K_M
usb-llms pull gh:modelmaker/aimodel1                    # GitHub release, or Git LFS files in the repo
usb-llms pull gh:modelmaker/aimodel1@v2:Q4_K_M --name mine
usb-llms pull https://example.com/model.gguf
usb-llms pull ./model.gguf
usb-llms pull llama3.2                                  # the Ollama library
usb-llms rm mine
```

The `hf:` and `gh:` forms are the same as in [LvW-harness](https://github.com/Gitguy268/LvW-harness), and full `huggingface.co` and `github.com` links work too. Only GGUF models run. Set `HF_TOKEN` or `GITHUB_TOKEN` for gated or private models and to get around GitHub's rate limit.

<details>
<summary>The catalogue</summary>

| Id | Size | RAM | Label | Licence |
|---|---|---|---|---|
| `smollm2-135m` | 0.11 GB | 1 GB | tiny | Apache-2.0 |
| `llama-3.2-3b` (default) | 2.02 GB | 4 GB | standard | Llama 3.2 |
| `phi-3.5-mini` | 2.39 GB | 6 GB | standard | MIT |
| `mistral-7b` | 4.37 GB | 8 GB | standard | Apache-2.0 |
| `qwen2.5-7b` | 4.68 GB | 8 GB | standard | Apache-2.0 |
| `gemma2-2b-abliterated` | 1.71 GB | 4 GB | uncensored | Gemma |
| `dolphin-8b` | 4.92 GB | 8 GB | uncensored | Llama 3.1 |
| `qwen3.5-9b-uncensored` | 5.63 GB | 12 GB | uncensored | Apache-2.0 |
| `nemomix-12b` | 7.48 GB | 16 GB | uncensored | not stated |

Models labelled uncensored have had their safety refusals removed and can produce harmful or false text. USB LLMS adds no system prompt to any model, and each model's own licence applies.

</details>

## Media

Each kind of media has a small default model that lives on the drive next to the chat models. Set a kind up once while online, and after that it works offline on every computer of the same type.

```sh
usb-llms media install speech          # LFM3 Audio (1.7 GB) in the drive's engine
usb-llms make speech "Dinner is ready." --voice female-american-english
usb-llms transcribe meeting.wav
usb-llms media install image           # stable-diffusion.cpp plus Z-Image-Turbo (6 GB)
usb-llms make image "a lighthouse on a rocky coast, watercolor"
usb-llms make 3d "a small toy robot"   # draws it first, then builds a GLB mesh
usb-llms media ls
```

| Kind | Engine on the drive | Default model | Licence |
|---|---|---|---|
| Image (text to image, picture editing) | [stable-diffusion.cpp](https://github.com/leejet/stable-diffusion.cpp) `sd-server`, added per system | Z-Image-Turbo Q3_K (6 GB). SD 1.5 (1.6 GB) is a smaller option | Apache-2.0 |
| Video (text to video, picture to video) | the same `sd-server` | Wan 2.1 T2V 1.3B (6 GB) | Apache-2.0 |
| Speech and transcription | the drive's Ollama | `lfm3-audio` (1.7 GB, 18 languages). `phonon` is a tiny English voice for Apple silicon | LFM Open 1.0 |
| Pictures and voice notes in chat | the drive's Ollama | any model that sees or hears, such as `lfm3-omni` | the model's own |
| 3D (picture to 3D, text to 3D) | [TripoSR](https://github.com/VAST-AI-Research/TripoSR) in a Python env made with uv | TripoSR (1.7 GB) | MIT |
| Music and sound | [ACE-Step 1.5](https://github.com/ace-step/ACE-Step-1.5) in a Python env made with uv | ACE-Step 1.5 2B turbo (about 9 GB) | MIT |

You can attach, paste, drop or record pictures and voice notes in the chat. A model that hears gets the audio itself; any other model gets a transcript. Attached files are kept in `data/media/uploads`, and the saved chat only points to them. Replies have a 🔊 button that reads them aloud.

The web interface's Create page lets you pick a kind, write a prompt or add a file, and watch the steps and a live preview. Everything you make stays in a gallery, and 3D models open in a viewer you can turn. The terminal menu has a Create entry that asks what to make, step by step. Results go to `data/media/<date>/`, each with a `.json` next to it holding the prompt, model, seed and settings.

In online mode, a remote can fill in for kinds the drive can't make: `usb-llms remote add openai https://api.openai.com/v1 OPENAI_API_KEY`, then `usb-llms media api openai image=gpt-image-1 speech=tts-1 voice=alloy transcribe=whisper-1`. Offline mode never uses it.

<details>
<summary>Where each kind runs</summary>

| | macOS arm64 | macOS x64 | Linux x64 | Linux arm64 | Windows x64 | Windows arm64 |
|---|---|---|---|---|---|---|
| Image and video | yes | yes | yes (CPU, or `USB_LLMS_SD_VARIANT=vulkan`) | remote only | yes (CPU, or `vulkan` / `cuda12`) | remote only |
| Speech, transcription, voice notes | yes | yes | yes | yes | yes | yes |
| 3D and music | yes (Apple GPU) | no (no PyTorch builds) | yes | yes | yes | no |

Engines start when they're needed: `sd-server` on port 11438, 3D on 11439, music on 11440. `usb-llms media stop` stops them, and the terminal, web and app interfaces stop the chat engine as before. Image and video weights are shared by every system, but the 3D and music engines need one install per system (a few GB each, because of PyTorch).
</details>

## Offline and online

```sh
usb-llms mode offline    # the default once a model is on the drive
usb-llms mode online
```

Offline blocks every outside connection. USB LLMS refuses its own requests, Ollama's cloud features are off, and the engine, Open WebUI and LvW-harness get a dead proxy address.

Online allows model and engine downloads, and remote OpenAI-compatible services:

```sh
export OPENROUTER_API_KEY=...
usb-llms remote add openrouter https://openrouter.ai/api/v1 OPENROUTER_API_KEY
```

Only the variable's name is saved, so a lost stick can't leak your key, and a key is never sent over plain `http` to another machine.

The engine's home folder and temp files stay on the drive, so it leaves no files on the computer.

## LvW-harness

[LvW-harness](https://github.com/Gitguy268/LvW-harness) is an agent that can edit files and run commands. USB LLMS can carry it and point it at the drive's models.

```sh
usb-llms lvw install /path/to/lvw       # copies the program onto the drive (about 78 MB)
usb-llms lvw run "Summarize this folder"
```

Its settings stay in `data/lvw` on the drive, the engine address updates itself if the port changes, and offline mode applies to it too. Models without tool support, such as SmolLM2, can chat but can't use its tools. Tiny models call tools badly, so for agent work use a tool-capable model of 3B or larger.

## Limits

The drive isn't encrypted; see [SECURITY](SECURITY.md). Offline mode isn't a firewall either. It stops programs that respect proxy settings, which covers everything USB LLMS starts, so a model can't reach the network, but a tool you run through LvW-harness could ignore those settings.

Some smaller gaps:

- LvW-harness's browser tools use the host's temp folder, because browsers can't keep a profile on exFAT.
- Split GGUF files from GitHub or direct links aren't supported. Pick a single-file version.
- GitLab specs work in LvW-harness but not here. Use the file's direct link.
- Transcription reads WAV files; other formats need ffmpeg on the computer.
- 3D works on one object on a plain background. Landscapes and busy scenes give an error.
- The programs aren't signed, so macOS or Windows may warn the first time. `start.command` clears the macOS download flag for this folder.

Media is slow without a GPU. A 512×512 SD 1.5 picture takes about 4 minutes on a Mac's CPU, and that's where SD 1.5 runs on a Mac, because the pinned stable-diffusion.cpp build renders SD 1.x models blank on Apple GPUs. Z-Image-Turbo, the default, works on Apple GPUs: about 3 minutes for a first 512×512 picture (tested through LvW-harness, which uses the same engine build).

What I've tested for version 0.1.0:

| Tested | How |
|---|---|
| macOS on Apple silicon | a simulated exFAT disk image: engine setup, chat on the GPU |
| Downloads | live pulls from Hugging Face, the Ollama library and GitHub repo files. GitHub release assets only against a mock |
| Offline mode | replies with outside network blocked by the OS, refused pulls, and no files left in the host's home or temp folders |
| Web, app window and terminal | a browser test, a headless window test and a pseudo-terminal test, each with a real model reply |
| Open WebUI | a full install on exFAT (3.5 GB), a clean first start with outside network blocked, its page in a browser, and a chat through it |
| LvW-harness | run from the drive with a real reply. A tool call went through, but the 0.5B test model sent empty arguments, so real tool work with a small local model is unproven |
| Media | on macOS (Apple silicon) in an APFS folder, not the exFAT image: speech, transcription, a voice note in chat, SD 1.5 images and TripoSR picture to 3D. The remote fallback has unit tests against a fake server |

| Not tested | Note |
|---|---|
| Windows | builds and has a CI job, but has never been run. Expect rough edges in `start.bat` and in stopping the engine |
| Linux | builds and has a CI job, and its lean filter was checked against the real archive listing, but it hasn't been run |
| A real USB stick | only an exFAT disk image. Real sticks are slower, and first starts can take minutes |
| Intel Macs, NVIDIA and AMD GPUs, Linux x64 | I don't have that hardware |
| `web --lan` from a second device | the token rules and QR code were only tested on one machine |
| Wan video, ACE-Step music, the remote fallback with a real key | video has a unit test of its request; music has none |

## Build

```sh
go test ./...
sh scripts/build.sh                # every system, into bin/
sh scripts/build.sh darwin-arm64   # just one
```

[CONTRIBUTING](CONTRIBUTING.md) has the test scripts, [SECURITY](SECURITY.md) says what is and isn't protected, and [NOTICE](NOTICE.md) has the credits.

## Licence

MIT, © Lucas Van De Weerd. Ollama, Open WebUI and the models keep their own licences and are downloaded separately. USB LLMS isn't affiliated with any of the projects it uses.
