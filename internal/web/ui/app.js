const $ = (id) => document.getElementById(id);
const el = (tag, props = {}, ...kids) => {
  const n = Object.assign(document.createElement(tag), props);
  n.append(...kids);
  return n;
};
const gb = (n) => (n / 1e9 >= 10 ? (n / 1e9).toFixed(0) : (n / 1e9).toFixed(1)) + " GB";

const state = { chatId: "", status: null, sending: null, installed: [] };

async function api(path, opts) {
  const res = await fetch("api/" + path, opts);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `Request failed (${res.status})`);
  return data;
}

/** POST and call onLine for each JSON line of the streamed answer. */
async function stream(path, body, onLine, signal) {
  const res = await fetch("api/" + path, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(body), signal });
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error(data.error || `Request failed (${res.status})`);
  }
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let nl;
    while ((nl = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (line) onLine(JSON.parse(line));
    }
  }
}

/** Paragraphs and fenced code. Built from text nodes, so a reply can't inject markup. */
function renderText(node, text) {
  node.replaceChildren();
  text.split(/```/).forEach((part, i) => {
    if (i % 2) {
      node.append(el("pre", {}, el("code", { textContent: part.replace(/^[^\n]*\n/, "") })));
    } else if (part.trim()) {
      for (const para of part.trim().split(/\n{2,}/)) {
        const p = el("p");
        para.split(/`([^`\n]+)`/).forEach((t, j) => p.append(j % 2 ? el("code", { textContent: t }) : t));
        node.append(p);
      }
    }
  });
}

function addMessage(role, text) {
  const m = el("div", { className: "msg " + role });
  renderText(m, text);
  $("messages").append(m);
  $("messages").scrollTop = $("messages").scrollHeight;
  return m;
}

function showEmpty() {
  const box = el("div", { className: "empty" });
  if (!state.status?.engineInstalled) {
    box.append(el("h2", { textContent: "This computer needs the engine" }), el("p", { textContent: "Run usb-llms setup while online. It adds the engine for this system to the drive." }));
  } else if (!state.installed.length) {
    box.append(el("h2", { textContent: "No models yet" }), el("p", { textContent: "Add a model to start chatting. It stays on this drive." }), el("button", { className: "primary", textContent: "Choose a model", onclick: () => show("models") }));
  } else {
    box.append(el("h2", { textContent: "Ask anything" }), el("p", { textContent: "Replies come from the model running on this computer." }));
  }
  $("messages").replaceChildren(box);
}

function show(view) {
  $("chat-view").hidden = view !== "chat";
  $("models-view").hidden = view !== "models";
  $("rail").classList.remove("open");
  if (view === "models") renderModels();
  else $("input").focus();
}

async function refreshStatus() {
  const s = (state.status = await api("status"));
  $("mode").setAttribute("aria-checked", String(s.online));
  $("mode-text").textContent = s.online ? "Online" : "Offline";
  $("engine-text").textContent = s.engineInstalled ? `Engine ${s.engineVersion} on ${s.platform}` : `No engine for ${s.platform}`;
  renderBar();
}

function renderBar() {
  const s = state.status;
  if (!s) return;
  const used = state.installed.reduce((n, m) => n + m.size, 0);
  const total = used + s.freeBytes || 1;
  $("bar").replaceChildren(...state.installed.map((m) => el("i", { title: `${m.name}, ${gb(m.size)}`, style: `width:${(m.size / total) * 100}%` })));
  $("drive-text").textContent = `${gb(used)} in models, ${gb(s.freeBytes)} free`;
}

async function refreshModels() {
  try {
    const { models, remote } = await api("models");
    state.installed = models;
    const keep = $("model").value;
    const opts = [...models.map((m) => m.name), ...remote];
    $("model").replaceChildren(...opts.map((name) => el("option", { value: name, textContent: name })));
    if (opts.includes(keep)) $("model").value = keep;
    else if (!keep) {
      try { const saved = localStorage.getItem("model"); if (opts.includes(saved)) $("model").value = saved; } catch {}
    }
    $("send").disabled = !opts.length;
  } catch {
    state.installed = [];
    $("send").disabled = true;
  }
  renderBar();
}

async function refreshChats() {
  const list = await api("chats");
  if (!list.length) return $("chats").replaceChildren(el("p", { textContent: "Saved chats appear here." }));
  $("chats").replaceChildren(
    ...list.map((c) => {
      const row = el("div", { className: "chat-row" + (c.id === state.chatId ? " on" : "") });
      row.append(
        el("button", { className: "open", textContent: c.title || "Untitled", title: c.title, onclick: () => openChat(c.id) }),
        el("button", {
          className: "del", textContent: "×", ariaLabel: `Delete ${c.title}`,
          onclick: async () => {
            await api(`chats/${c.id}`, { method: "DELETE" });
            if (c.id === state.chatId) newChat();
            refreshChats();
          },
        }),
      );
      return row;
    }),
  );
}

async function openChat(id) {
  const c = await api(`chats/${id}`);
  state.chatId = id;
  $("messages").replaceChildren();
  c.messages.forEach((m) => addMessage(m.role === "user" ? "user" : "assistant", m.content));
  if ([...$("model").options].some((o) => o.value === c.model)) $("model").value = c.model;
  show("chat");
  refreshChats();
}

function newChat() {
  state.chatId = "";
  showEmpty();
  show("chat");
  refreshChats();
}

async function send() {
  const content = $("input").value.trim();
  if (!content || !$("model").value) return;
  if ($("messages").querySelector(".empty")) $("messages").replaceChildren();
  $("input").value = "";
  sizeInput();
  addMessage("user", content);
  const reply = addMessage("assistant", "");
  const ctl = (state.sending = new AbortController());
  $("send").textContent = "Stop";
  let text = "";
  try {
    await stream("chat", { id: state.chatId, model: $("model").value, content }, (ev) => {
      if (ev.id) { state.chatId = ev.id; refreshChats(); }
      if (ev.delta) {
        text += ev.delta;
        renderText(reply, text);
        $("messages").scrollTop = $("messages").scrollHeight;
      }
      if (ev.error) throw new Error(ev.error);
    }, ctl.signal);
  } catch (err) {
    if (err.name !== "AbortError") addMessage("error", err.message);
  }
  state.sending = null;
  $("send").textContent = "Send";
  refreshChats();
}

async function renderModels() {
  await refreshModels();
  const ram = state.status?.ramBytes || 0;
  $("installed").replaceChildren(
    ...(state.installed.length
      ? state.installed.map((m) =>
          el("tr", {},
            el("td", { textContent: m.name }),
            el("td", { className: "num", textContent: gb(m.size) }),
            el("td", { className: "act" }, el("button", {
              className: "plain", textContent: "Delete",
              onclick: async () => {
                try { await api(`models?name=${encodeURIComponent(m.name)}`, { method: "DELETE" }); } catch (err) { setProgress(err.message, true); }
                renderModels();
              },
            })),
          ))
      : [el("tr", {}, el("td", { textContent: "Nothing yet. Add a model below." }))]),
  );
  const catalog = await api("catalog");
  $("catalog").replaceChildren(
    ...catalog.map((c) => {
      const heavy = ram && c.ramGB * 1e9 > ram;
      const name = el("td", {}, c.name, el("span", { className: "tag " + c.label, textContent: c.label }), el("small", { textContent: `${c.note} Licence: ${c.licence}.` }));
      if (heavy) name.append(el("small", { className: "warn", textContent: `Needs about ${c.ramGB} GB of memory; this computer has ${gb(ram)}.` }));
      return el("tr", {},
        name,
        el("td", { className: "num", textContent: `${c.sizeGB} GB` }),
        el("td", { className: "act" }, el("button", { className: "plain", textContent: "Add", disabled: !state.status?.online, title: state.status?.online ? "" : "Switch to Online to download", onclick: () => pull(c.id) })),
      );
    }),
  );
}

function setProgress(text, isError, done, total) {
  $("progress").hidden = false;
  $("progress").classList.toggle("error", !!isError);
  $("progress-text").textContent = text;
  if (total) { $("progress-bar").max = total; $("progress-bar").value = done || 0; }
  else $("progress-bar").removeAttribute("value");
}

async function pull(spec) {
  setProgress("Starting");
  try {
    await stream("pull", { spec }, (ev) => {
      if (ev.error) throw new Error(ev.error);
      const pct = ev.total ? `${gb(ev.done || 0)} of ${gb(ev.total)}` : "";
      setProgress(ev.status === "ready" ? `Added ${ev.model}` : `${ev.status} ${pct}`.trim(), false, ev.done, ev.total);
      if (ev.status === "ready") $("progress-bar").style.display = "none";
    });
    $("spec").value = "";
    await refreshStatus();
    await renderModels();
    if (state.chatId === "" && !$("chat-view").hidden) showEmpty();
  } catch (err) {
    setProgress(err.message, true);
  }
}

function sizeInput() {
  $("input").style.height = "auto";
  $("input").style.height = $("input").scrollHeight + 2 + "px";
}

$("composer").addEventListener("submit", (e) => {
  e.preventDefault();
  if (state.sending) state.sending.abort();
  else send();
});
$("input").addEventListener("input", sizeInput);
$("input").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); $("composer").requestSubmit(); }
});
$("new-chat").onclick = newChat;
$("show-models").onclick = () => show("models");
$("menu").onclick = () => $("rail").classList.toggle("open");
$("model").onchange = () => { try { localStorage.setItem("model", $("model").value); } catch {} };
$("add").addEventListener("submit", (e) => {
  e.preventDefault();
  $("progress-bar").style.display = "";
  if ($("spec").value.trim()) pull($("spec").value.trim());
});
$("mode").onclick = async () => {
  const online = $("mode").getAttribute("aria-checked") !== "true";
  $("mode").disabled = true;
  try {
    await api("mode", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ online }) });
    await refreshStatus();
    if (!$("models-view").hidden) await renderModels();
    else await refreshModels();
  } catch (err) {
    addMessage("error", err.message);
  }
  $("mode").disabled = false;
};

(async () => {
  await refreshStatus();
  await refreshModels();
  await refreshChats();
  showEmpty();
  $("input").focus();
})();
