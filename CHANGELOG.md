# Changelog

## 0.1.0 (unreleased)

First version. Replaces USB-Uncensored-LLM and Portable-AI-USB with one tool.

- Portable engine setup for macOS, Linux and Windows, with a lean option that leaves out GPU libraries.
- Models from Hugging Face, GitHub, direct links, local files and the Ollama library.
- Terminal interface, web interface, app window, Ollama commands and API, and optional Open WebUI.
- Offline and online modes, and remote OpenAI-compatible services in online mode.
- Runs LvW-harness against the drive.
- Media: `usb-llms media`, `make` and `transcribe` make images and video (stable-diffusion.cpp), speech and transcripts (the drive's Ollama), 3D models from a picture or a prompt (TripoSR) and music (ACE-Step 1.5), all kept on the drive. An OpenAI-compatible remote can stand in for kinds the drive can't make, in online mode only.
- The web chat takes pictures and voice notes and can read replies aloud; a new Create page has live previews, a gallery and a 3D viewer. The terminal menu has a Create entry.
