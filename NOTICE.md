# NOTICE

USB LLMS is MIT licensed (see [LICENSE](LICENSE)). It builds on the work below. Licences were checked on 2026-11-01.

## Dependencies compiled into the binary

| Module | Licence | Used for |
|---|---|---|
| [charmbracelet/bubbletea, bubbles, lipgloss, colorprofile, x/ansi, x/cellbuf, x/term](https://github.com/charmbracelet) | MIT | the terminal interface |
| [klauspost/compress](https://github.com/klauspost/compress) | BSD-3-Clause and others (see its LICENSE) | unpacking `.tar.zst` engine archives |
| [skip2/go-qrcode](https://github.com/skip2/go-qrcode) | MIT | the QR code for `web --lan` |
| atotto/clipboard | BSD-3-Clause | terminal interface dependency |
| aymanbagabas/go-osc52 | MIT | terminal interface dependency |
| clipperhouse/displaywidth, stringish, uax29 | MIT | terminal interface dependency |
| lucasb-eyer/go-colorful | MIT | terminal interface dependency |
| mattn/go-isatty, go-runewidth | MIT | terminal interface dependency |
| muesli/ansi, cancelreader, termenv | MIT | terminal interface dependency |
| rivo/uniseg | MIT | terminal interface dependency |
| xo/terminfo | MIT | terminal interface dependency |
| golang.org/x/sys | BSD-3-Clause | system calls |

## Downloaded at run time (not part of this repository)

| Software | Licence | When |
|---|---|---|
| [Ollama](https://github.com/ollama/ollama) | MIT | `usb-llms setup`. This is the engine |
| [Open WebUI](https://github.com/open-webui/open-webui) | [Open WebUI License](https://github.com/open-webui/open-webui/blob/main/LICENSE), a custom licence with a branding clause | `usb-llms webui install`. Installed unmodified from PyPI with its branding intact. Never bundled or restyled |
| [uv](https://github.com/astral-sh/uv) | MIT or Apache-2.0 | `usb-llms webui install` |
| Python | PSF-2.0 | `usb-llms webui install` |
| [LvW-harness](https://github.com/Gitguy268/LvW-harness) | MIT | `usb-llms lvw install`, when you choose to add it |
| Models | each model's own licence, shown in the catalogue | when you add one |

Two catalogue models carry terms beyond open-source licences: Llama (Meta's community licence) and Gemma (Google's terms). NemoMix Unleashed states no licence on its model page. You're responsible for following each model's terms.

## Ideas, no code

USB LLMS was written from scratch. These projects supplied the ideas.

| Project | Licence | Idea used |
|---|---|---|
| [techjarves/USB-Uncensored-LLM](https://github.com/techjarves/USB-Uncensored-LLM) | **No licence (all rights reserved).** Nothing was copied: not its code, chat page, model list or prompts | one shared model folder used by every system, a phone-friendly local web chat |
| [techjarves/Portable-AI-USB](https://github.com/techjarves/Portable-AI-USB) (MIT, © 2026) | MIT | running from an exFAT stick, leaving no trace on the host, choosing models at setup |
| [odysseus](https://github.com/odysseus-dev/odysseus) | **AGPL-3.0** | the RAM fit warning, seen via LvW-harness. Ideas only |

The model spec syntax (`hf:`, `gh:`) follows [LvW-harness](https://github.com/Gitguy268/LvW-harness), by the same author, so one spec works in both tools.

## Trademarks

Ollama, Open WebUI, Hugging Face, GitHub, Llama, Gemma, Qwen, Mistral, Phi and other names are trademarks of their owners. USB LLMS isn't affiliated with or endorsed by any of them.
