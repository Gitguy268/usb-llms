// Media: chat attachments, voice notes, the speak button and the Create view.
// Shares app.js's globals ($, el, api, stream, state, show).

const b64 = (blob) =>
  new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result).replace(/^data:[^,]*,/, ""));
    r.onerror = () => reject(r.error);
    r.readAsDataURL(blob);
  });

/** 16-bit mono WAV, which the engine reads. */
function wavFrom(samples, rate) {
  const buf = new ArrayBuffer(44 + samples.length * 2);
  const v = new DataView(buf);
  const str = (o, t) => [...t].forEach((c, i) => v.setUint8(o + i, c.charCodeAt(0)));
  str(0, "RIFF"); v.setUint32(4, 36 + samples.length * 2, true); str(8, "WAVEfmt ");
  v.setUint32(16, 16, true); v.setUint16(20, 1, true); v.setUint16(22, 1, true);
  v.setUint32(24, rate, true); v.setUint32(28, rate * 2, true); v.setUint16(32, 2, true); v.setUint16(34, 16, true);
  str(36, "data"); v.setUint32(40, samples.length * 2, true);
  samples.forEach((x, i) => v.setInt16(44 + i * 2, Math.max(-1, Math.min(1, x)) * 0x7fff, true));
  return new Blob([buf], { type: "audio/wav" });
}

async function recordMic() {
  const media = await navigator.mediaDevices.getUserMedia({ audio: true });
  const rec = new MediaRecorder(media);
  const parts = [];
  rec.ondataavailable = (e) => parts.push(e.data);
  rec.start();
  return {
    stop: () => new Promise((resolve) => {
      rec.onstop = async () => {
        media.getTracks().forEach((t) => t.stop());
        const decoded = await new OfflineAudioContext(1, 1, 16000).decodeAudioData(await new Blob(parts).arrayBuffer());
        const off = new OfflineAudioContext(1, Math.ceil(decoded.duration * 16000), 16000);
        const src = off.createBufferSource();
        src.buffer = decoded;
        src.connect(off.destination);
        src.start();
        resolve(wavFrom((await off.startRendering()).getChannelData(0), 16000));
      };
      rec.stop();
    }),
  };
}

// ---- chat attachments -------------------------------------------------------

let pending = [];
function takeAttachments() {
  const out = pending;
  pending = [];
  renderChips();
  return out;
}

function renderChips() {
  $("chips").replaceChildren(...pending.map((a, i) =>
    el("span", { className: "chip" },
      a.mime.startsWith("image/") ? el("img", { src: `data:${a.mime};base64,${a.data}`, alt: "" }) : "🎤",
      a.name,
      el("button", { type: "button", textContent: "×", ariaLabel: `Remove ${a.name}`, onclick: () => { pending.splice(i, 1); renderChips(); } }))));
}

async function addFiles(list) {
  for (const f of list) {
    if (!/^(image|audio)\//.test(f.type)) { addMessage("error", `${f.name}: only pictures and audio can be attached.`); continue; }
    if (f.size > 20e6) { addMessage("error", `${f.name} is over 20 MB.`); continue; }
    pending.push({ name: f.name, mime: f.type, data: await b64(f) });
  }
  renderChips();
}

function attachedEl(files) {
  return el("div", { className: "attached" }, ...files.map((f) =>
    f.mime.startsWith("audio/") ? el("audio", { src: f.url, controls: true }) : el("img", { src: f.url, alt: "attached picture" })));
}

function speakButton(getText) {
  const b = el("button", { type: "button", className: "speak", textContent: "🔊", title: "Read this aloud", ariaLabel: "Read this aloud" });
  b.onclick = async () => {
    const text = (getText() || "").slice(0, 4000);
    if (!text) return;
    b.disabled = true;
    try {
      let out;
      await stream("media/make", { kind: "speech", prompt: text }, (ev) => {
        if (ev.error) throw new Error(ev.error);
        if (ev.done) out = ev.output;
      });
      new Audio(mediaURL(out.rel)).play();
    } catch (err) {
      addMessage("error", err.message);
    }
    b.disabled = false;
  };
  return b;
}

$("attach").onclick = () => $("attach-file").click();
$("attach-file").onchange = async () => { await addFiles($("attach-file").files); $("attach-file").value = ""; };
let recording = null;
$("record").onclick = async () => {
  try {
    if (!recording) {
      recording = await recordMic();
      $("record").classList.add("recording");
      $("record").textContent = "■";
    } else {
      const wav = await recording.stop();
      recording = null;
      $("record").classList.remove("recording");
      $("record").textContent = "🎤";
      pending.push({ name: "voice note", mime: "audio/wav", data: await b64(wav) });
      renderChips();
    }
  } catch (err) {
    recording = null;
    $("record").textContent = "🎤";
    addMessage("error", err.message);
  }
};
$("input").addEventListener("paste", (e) => {
  const files = [...(e.clipboardData?.files || [])];
  if (files.length) { e.preventDefault(); addFiles(files); }
});
$("messages").addEventListener("dragover", (e) => e.preventDefault());
$("messages").addEventListener("drop", (e) => { e.preventDefault(); addFiles([...(e.dataTransfer?.files || [])]); });

// ---- results ---------------------------------------------------------------------

const mediaURL = (rel) => "media/" + rel.split("/").map(encodeURIComponent).join("/");
let viewer;
const loadViewer = () => (viewer ??= new Promise((resolve, reject) => {
  const s = el("script", { src: "vendor/model-viewer.min.js" });
  s.onload = resolve;
  s.onerror = () => reject(new Error("Couldn't load the 3D viewer."));
  document.head.append(s);
}));

function mediaEl(o) {
  const src = mediaURL(o.rel);
  let node;
  if (o.mime.startsWith("image/")) node = el("img", { src, alt: o.meta?.prompt || o.kind, loading: "lazy" });
  else if (o.mime.startsWith("audio/")) node = el("audio", { src, controls: true, preload: "metadata" });
  else if (o.mime.startsWith("video/")) node = el("video", { src, controls: true, loop: true, muted: true, playsInline: true });
  else if (o.mime === "model/gltf-binary") {
    node = document.createElement("model-viewer");
    for (const [k, v] of Object.entries({ src, "camera-controls": "", "auto-rotate": "", "shadow-intensity": "1", alt: o.meta?.prompt || "3D model" })) node.setAttribute(k, v);
    loadViewer().catch((err) => addMessage("error", err.message));
  } else node = el("blockquote", { textContent: o.text ?? o.meta?.text ?? "" });
  return el("figure", { className: "media" }, node, el("figcaption", {}, el("a", { href: src, download: o.rel.split("/").pop(), textContent: "Download" })));
}

// ---- Create view ----------------------------------------------------------------

const KINDS = [
  { id: "image", label: "Image", hint: "Describe the picture: subject, style, light, framing.", input: "image/*", inputLabel: "Picture to edit (optional)" },
  { id: "video", label: "Video", hint: "Describe the motion. A few seconds; slow without a GPU.", input: "image/*", inputLabel: "Picture to animate (optional)" },
  { id: "speech", label: "Speech", hint: "Text to read aloud." },
  { id: "transcribe", label: "Transcribe", hint: "Speech to text from a recording or an audio file.", input: "audio/*", inputLabel: "Audio file", noPrompt: true },
  { id: "music", label: "Music", hint: "Genre, instruments, mood, tempo. Add lyrics for a song." },
  { id: "3d", label: "3D", hint: "One object to turn into a 3D model, or upload a picture of it.", input: "image/*", inputLabel: "Picture of the object (optional)" },
];
const OPTS = {
  image: [["width", "Width", "auto"], ["height", "Height", "auto"], ["steps", "Steps", "auto"], ["seed", "Seed", "random"]],
  video: [["frames", "Frames", "17"], ["seed", "Seed", "random"]],
  speech: [["voice", "Voice", "default", "text"]],
  transcribe: [["language", "Language", "auto", "text"], ["translate", "Translate to English", "", "checkbox"]],
  music: [["seconds", "Seconds", "30"], ["lyrics", "Lyrics (empty = instrumental)", "[verse] …", "textarea"]],
  "3d": [["seed", "Seed", "random"]],
};
const create = { kind: KINDS[0], file: null, data: null, rec: null };

function renderKind() {
  const k = create.kind;
  $("kinds").replaceChildren(...KINDS.map((x) => el("button", { type: "button", role: "tab", className: x === k ? "on" : "", ariaSelected: String(x === k), textContent: x.label, onclick: () => { create.kind = x; renderKind(); } })));
  $("m-prompt").hidden = !!k.noPrompt;
  $("m-prompt").placeholder = k.hint;
  $("m-file-row").hidden = !k.input;
  $("m-file-label").textContent = k.inputLabel || "";
  $("m-file").accept = k.input || "";
  $("m-file").value = "";
  $("m-record").hidden = k.id !== "transcribe";
  create.file = null;
  const fits = (create.data?.catalog || []).filter((e) => e.kinds.includes(k.id));
  $("m-model").replaceChildren(el("option", { value: "", textContent: "best available" }), ...fits.map((e) => el("option", { value: e.id, textContent: e.name + (e.ready ? "" : " (not on the drive)") })), el("option", { value: "api", textContent: "remote API" }));
  $("m-opts").replaceChildren(...(OPTS[k.id] || []).map(([name, label, ph, type = "number"]) => {
    const input = type === "textarea" ? el("textarea", { name, rows: 3, placeholder: ph }) : el("input", { name, type, placeholder: ph });
    return el("label", { className: type === "checkbox" ? "opt check" : type === "textarea" ? "opt wide" : "opt" }, el("span", { textContent: label }), input);
  }));
}

$("m-file").onchange = async () => {
  const f = $("m-file").files[0];
  create.file = f ? { mime: f.type, data: await b64(f) } : null;
};
$("m-record").onclick = async () => {
  try {
    if (!create.rec) { create.rec = await recordMic(); $("m-record").textContent = "■ Stop"; }
    else {
      create.file = { mime: "audio/wav", data: await b64(await create.rec.stop()) };
      create.rec = null;
      $("m-record").textContent = "🎤 Record";
      showJob({ status: "Recording ready. Press Make." });
    }
  } catch (err) { create.rec = null; $("m-record").textContent = "🎤 Record"; showJob({ error: err.message }); }
};

function showJob(j) {
  const box = $("m-job");
  box.hidden = false;
  box.replaceChildren(
    j.error ? el("p", { className: "warn", textContent: j.error }) : el("p", { textContent: j.status + (j.total ? ` · step ${j.step} of ${j.total}` : "") }),
    j.total ? Object.assign(el("progress"), { max: j.total, value: j.step || 0 }) : "",
    j.preview ? el("img", { className: "step", src: `data:image/png;base64,${j.preview}`, alt: "preview" }) : "",
    j.output ? mediaEl(j.output) : "",
  );
}

$("m-make").onclick = async () => {
  const k = create.kind;
  const body = { kind: k.id, prompt: $("m-prompt").value.trim(), model: $("m-model").value };
  for (const input of $("m-opts").querySelectorAll("[name]")) {
    if (input.type === "checkbox") body[input.name] = input.checked;
    else if (input.value !== "") body[input.name] = input.type === "number" ? Number(input.value) : input.value;
  }
  if (create.file) body[create.file.mime.startsWith("audio/") ? "audio" : "image"] = create.file.data;
  if (k.noPrompt ? !create.file : !body.prompt && !(k.id === "3d" && create.file)) return showJob({ error: k.noPrompt ? "Add or record audio first." : "Write a prompt first." });
  $("m-make").disabled = true;
  showJob({ status: "Starting…" });
  try {
    await stream("media/make", body, (ev) => {
      if (ev.error) throw new Error(ev.error);
      showJob(ev.done ? { status: "Done", output: ev.output } : ev);
    });
    await refreshCreate();
  } catch (err) {
    showJob({ error: err.message });
  }
  $("m-make").disabled = false;
};

async function pullMedia(id) {
  showJob({ status: "Starting the download…" });
  try {
    await stream("media/pull", { id }, (ev) => {
      if (ev.error) throw new Error(ev.error);
      showJob({ status: ev.total ? `${ev.status} ${gb(ev.done || 0)} of ${gb(ev.total)}` : ev.status });
    });
    showJob({ status: `${id} is ready.` });
  } catch (err) {
    showJob({ error: err.message });
  }
  await refreshCreate();
}

async function refreshCreate() {
  create.data = await api("media");
  const d = create.data;
  $("gallery").replaceChildren(...(d.outputs.length ? d.outputs.map((o) =>
    el("div", { className: "item" }, mediaEl(o), el("div", { className: "row" },
      el("small", { className: "grow", textContent: `${o.kind} · ${(o.meta?.prompt || o.text || (o.kind === "3d" ? "from a picture" : "")).slice(0, 70)}` }),
      el("button", { className: "plain", textContent: "Delete", onclick: async () => { await api(`media/out?rel=${encodeURIComponent(o.rel)}`, { method: "DELETE" }); refreshCreate(); } }))))
    : [el("p", { className: "hint", textContent: "Nothing made yet." })]));
  $("m-catalog").replaceChildren(...d.catalog.map((e) => el("tr", {},
    el("td", {}, e.name, el("small", { textContent: `${e.kinds.join(", ")} · ${e.note || ""} Licence: ${e.licence}.` })),
    el("td", { className: "num", textContent: `${e.sizeGB} GB` }),
    el("td", { className: "act" }, e.ready
      ? el("button", { className: "plain", textContent: "Remove", onclick: async () => { await api(`media/models?id=${encodeURIComponent(e.id)}`, { method: "DELETE" }).catch((err) => showJob({ error: err.message })); refreshCreate(); } })
      : el("button", { className: "plain", textContent: "Get", disabled: !d.online, title: d.online ? "" : "Switch to Online to download", onclick: () => pullMedia(e.id) })))));
  const keep = $("m-model").value;
  renderKind();
  $("m-model").value = keep;
}

async function renderCreate() {
  try {
    await refreshCreate();
  } catch (err) {
    showJob({ error: err.message });
  }
}

$("show-create").onclick = () => show("create");
