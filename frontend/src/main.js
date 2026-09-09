import { LANGUAGES } from "./languages.js";
import { EventsOn, WindowHide } from "../wailsjs/runtime/runtime.js";

// Wails injects `window.runtime` and `window.go` itself in the production
// build; under plain `vite dev` they don't exist, so every bridge call below
// is guarded and becomes a harmless no-op there.
function hasRuntime() {
  return typeof window !== "undefined" && !!window.runtime;
}

function appBinding() {
  return window.go && window.go.main && window.go.main.App;
}

function hideWindow() {
  if (hasRuntime()) WindowHide();
}

function onBridgeEvent(name, cb) {
  if (hasRuntime()) EventsOn(name, cb);
}

function translateBridge(text, source, target) {
  return appBinding().TranslateText(text, source, target);
}

function copyBridge(text) {
  return appBinding().CopyText(text);
}

(() => {
  const input = document.getElementById("in");
  const out = document.getElementById("out");
  const count = document.getElementById("count");
  const status = document.getElementById("status");

  // Raycast tarzı: pencere düğmesi yok, Esc gizler, hotkey geri getirir.
  // Esc sırası: ayar paneli → dil menüsü → pencereyi gizle.
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
      if (closeSettings()) return;
      if (closeOpenDropdown()) return;
      stopSpeaking();
      hideWindow();
    }
  });

  // Özel dil menüleri (native select popup'u stillenemediği için).
  let openDD = null;
  function closeOpenDropdown() {
    if (!openDD) return false;
    openDD.list.hidden = true;
    openDD = null;
    return true;
  }
  document.addEventListener("click", () => closeOpenDropdown());

  // Settings pane: Win+, toggles it while the window is focused (backend
  // hook emits "open-settings"); Esc / ✕ / backdrop click closes it.
  const settings = document.getElementById("settings");
  function closeSettings() {
    if (settings.hidden) return false;
    settings.hidden = true;
    return true;
  }
  document.getElementById("settingsClose").addEventListener("click", closeSettings);
  settings.addEventListener("click", (e) => {
    if (e.target === settings) closeSettings();
  });
  onBridgeEvent("open-settings", () => {
    settings.hidden = !settings.hidden;
  });

  function buildDropdown(btnId, listId, labelId, items, initial) {
    const btn = document.getElementById(btnId);
    const list = document.getElementById(listId);
    const label = document.getElementById(labelId);
    const dd = {
      list,
      value: initial,
      set(code) {
        dd.value = code;
        const it = items.find((l) => l.code === code) || items[0];
        label.textContent = it.name;
        for (const el of list.querySelectorAll(".dd-item")) {
          el.classList.toggle("selected", el.dataset.code === code);
        }
      },
    };
    const searchWrap = document.createElement("div");
    searchWrap.className = "dd-searchwrap";
    const search = document.createElement("input");
    search.className = "dd-search";
    search.placeholder = "Search...";
    search.autocomplete = "off";
    search.spellcheck = false;
    search.addEventListener("click", (e) => e.stopPropagation());
    search.addEventListener("keydown", (e) => e.stopPropagation());
    searchWrap.appendChild(search);
    list.appendChild(searchWrap);
    const itemsBox = document.createElement("div");
    itemsBox.className = "dd-items";
    list.appendChild(itemsBox);
    const itemEls = [];
    for (const lang of items) {
      const el = document.createElement("div");
      el.className = "dd-item";
      el.textContent = lang.name;
      el.dataset.code = lang.code;
      el.addEventListener("click", (e) => {
        e.stopPropagation();
        dd.set(lang.code);
        closeOpenDropdown();
        updateSpellcheck();
        schedule();
      });
      itemsBox.appendChild(el);
      itemEls.push(el);
    }
    search.addEventListener("input", () => {
      const q = search.value.trim().toLowerCase();
      for (const el of itemEls) {
        el.style.display = !q || el.textContent.toLowerCase().includes(q) ? "" : "none";
      }
    });
    btn.addEventListener("click", (e) => {
      e.stopPropagation();
      const willOpen = list.hidden;
      closeOpenDropdown();
      if (willOpen) {
        search.value = "";
        for (const el of itemEls) el.style.display = "";
        list.hidden = false;
        openDD = dd;
        setTimeout(() => search.focus(), 0);
      }
    });
    dd.set(initial);
    return dd;
  }

  // Yazım denetimi seçili dile uyar: açık kaynak + çeviri sonucu
  // belli değilse kapalı (cihaz sözlüğü İngilizceyi çizerdi).
  let lastDetected = null;
  // Chromium sözlük kodları (sözlüğü olmayan dile null → denetim kapalı).
  function dictLang(code) {
    if (code === "en") return "en-US";
    if (code === "pt") return "pt-BR";
    if (code === "ar" || code === "ja" || code === "zh-CN" || code === "az") return null;
    return code;
  }
  function updateSpellcheck() {
    const raw = fromSel.value === "auto" ? lastDetected : fromSel.value;
    const src = raw ? dictLang(raw) : null;
    if (src) {
      input.spellcheck = true;
      input.lang = src;
    } else {
      input.spellcheck = false;
      input.removeAttribute("lang");
    }
  }

  const fromSel = buildDropdown("fromBtn", "fromList", "fromLabel", LANGUAGES, "auto");
  const toSel = buildDropdown(
    "toBtn",
    "toList",
    "toLabel",
    LANGUAGES.filter((l) => l.code !== "auto"),
    "tr",
  );

  // Swap: diller değişir, output metni input'a taşınır ve yeni çiftle
  // çevrilir — eski input metni düşer. Kaynak Detect language ise algılanan
  // dil hedefe gider (hiç algılanmadıysa swap anlamsız, dokunma).
  document.getElementById("swap").addEventListener("click", () => {
    const from = fromSel.value === "auto" ? lastDetected : fromSel.value;
    if (!from) return;
    const to = toSel.value;
    fromSel.set(to);
    toSel.set(from);
    updateSpellcheck();
    if (out.textContent && !out.classList.contains("placeholder") && !out.classList.contains("error")) {
      input.value = out.textContent;
      out.textContent = "Translation will appear here…";
      out.classList.add("placeholder");
      out.classList.remove("error");
    }
    schedule();
  });

  let timer = null;
  let seq = 0;
  let debounceMs = 300;
  const MIN_INTERVAL = 800;
  const RETRY_DELAY = 1500;
  let lastKey = "";
  let lastCallAt = 0;
  function schedule() {
    clearTimeout(timer);
    timer = setTimeout(() => doRun(), debounceMs);
  }

  function isRateLimit(err) {
    if (err && err.code === "RATE_LIMIT") return true;
    const msg = err instanceof Error ? err.message : String(err == null ? "" : err);
    return /Rate limited/i.test(msg);
  }

  function updateCount() {
    const len = input.value.length;
    const words = input.value.trim() ? input.value.trim().split(/\s+/).length : 0;
    count.textContent = `${words} Words • ${len} Characters`;
  }

  async function doRun(retry = 0, seqId = null) {
    stopSpeaking();
    const text = input.value.trim();
    updateCount();
    if (!text) {
      out.textContent = "Translation will appear here…";
      out.classList.remove("error");
      out.classList.add("placeholder");
      status.textContent = "";
      return;
    }
    const key = `${fromSel.value}|${toSel.value}|${text}`;
    if (retry === 0) {
      if (key === lastKey) return;
      if (seqId === null) seqId = ++seq;
      const wait = MIN_INTERVAL - (Date.now() - lastCallAt);
      if (wait > 0) {
        clearTimeout(timer);
        timer = setTimeout(() => {
          if (seqId === seq) doRun(0, seqId);
        }, wait);
        return;
      }
    } else if (seqId !== seq) {
      return;
    }
    lastCallAt = Date.now();
    status.textContent = retry > 0 ? "Rate limited, retrying…" : "Translating…";
    try {
      const r = await translateBridge(text, fromSel.value, toSel.value);
      if (seqId !== seq) return;
      out.textContent = r.translatedText;
      out.classList.remove("error");
      out.classList.remove("placeholder");
      status.textContent = "";
      lastKey = key;
      if (r.detectedSource && r.detectedSource !== lastDetected) {
        lastDetected = r.detectedSource;
        updateSpellcheck();
      }
    } catch (err) {
      if (seqId !== seq) return;
      if (isRateLimit(err) && retry < 2) {
        status.textContent = "Rate limited, retrying…";
        clearTimeout(timer);
        timer = setTimeout(() => {
          if (seqId === seq) doRun(retry + 1, seqId);
        }, RETRY_DELAY);
        return;
      }
      out.textContent = err instanceof Error ? err.message : String(err);
      out.classList.add("error");
      out.classList.remove("placeholder");
      status.textContent = "";
    }
  }

  input.addEventListener("input", schedule);

  document.getElementById("copybtn").addEventListener("click", () => {
    const t = out.textContent;
    if (t && !out.classList.contains("placeholder")) {
      Promise.resolve(copyBridge(t)).catch(() => {});
    }
  });

  // ---- Settings (persisted in localStorage) ----
  const THEMES = [
    { id: "default", name: "Default", swatch: ["#1b1b22", "#5aa2ff", "#f2f2f5"] },
    { id: "tokyo-night", name: "Tokyo Night", swatch: ["#1a1b26", "#7aa2f7", "#c0caf5"] },
    { id: "catppuccin", name: "Catppuccin", swatch: ["#1e1e2e", "#b4befe", "#cdd6f4"] },
    { id: "dracula", name: "Dracula", swatch: ["#282a36", "#bd93f9", "#f8f8f2"] },
    { id: "nord", name: "Nord", swatch: ["#2e3440", "#88c0d0", "#e5e9f0"] },
    { id: "gruvbox", name: "Gruvbox", swatch: ["#282828", "#83a598", "#ebdbb2"] },
    { id: "one-dark", name: "One Dark", swatch: ["#282c34", "#61afef", "#d7dae0"] },
  ];
  const DEBOUNCE_OPTIONS = [150, 300, 500, 800];

  function loadSettings() {
    try {
      const raw = JSON.parse(localStorage.getItem("settings") || "{}");
      return {
        debounce: DEBOUNCE_OPTIONS.includes(raw.debounce) ? raw.debounce : 300,
        theme: THEMES.some((t) => t.id === raw.theme) ? raw.theme : "default",
      };
    } catch {
      return { debounce: 300, theme: "default" };
    }
  }
  const appSettings = loadSettings();
  debounceMs = appSettings.debounce;
  function saveSettings() {
    try {
      localStorage.setItem("settings", JSON.stringify(appSettings));
    } catch {}
  }
  function applyTheme(id) {
    document.documentElement.dataset.theme = id;
  }

  const segButtons = [];
  const themeButtons = [];
  function syncSettingsUI() {
    for (const { b, ms } of segButtons) {
      b.classList.toggle("selected", appSettings.debounce === ms);
    }
    for (const { b, id } of themeButtons) {
      b.classList.toggle("selected", appSettings.theme === id);
    }
  }
  function buildSettingsUI() {
    const seg = document.getElementById("debounceSeg");
    for (const ms of DEBOUNCE_OPTIONS) {
      const b = document.createElement("button");
      b.className = "seg-btn";
      b.textContent = `${ms} ms`;
      b.addEventListener("click", () => {
        appSettings.debounce = ms;
        debounceMs = ms;
        saveSettings();
        syncSettingsUI();
      });
      seg.appendChild(b);
      segButtons.push({ b, ms });
    }
    const grid = document.getElementById("themeGrid");
    for (const t of THEMES) {
      const b = document.createElement("button");
      b.className = "theme-opt";
      b.innerHTML =
        `<span class="swatch">` +
        t.swatch.map((c) => `<i style="background:${c}"></i>`).join("") +
        `</span><span>${t.name}</span>`;
      b.addEventListener("click", () => {
        appSettings.theme = t.id;
        applyTheme(t.id);
        saveSettings();
        syncSettingsUI();
      });
      grid.appendChild(b);
      themeButtons.push({ b, id: t.id });
    }
    syncSettingsUI();
  }

  updateCount();
  updateSpellcheck();
  applyTheme(appSettings.theme);
  buildSettingsUI();

  // ---- Text-to-speech: keyless Google TTS endpoint, same family as the
  // translation one. The <audio> element fetches it with the browser's own
  // identity (real Chrome UA + TLS), so it reads as a genuine browser
  // request and the Go-side 429 fingerprint problem does not apply.
  // Local speechSynthesis voices are the fallback for offline / throttled
  // moments and for when the source language is unknown. ----
  const speechAvailable = "speechSynthesis" in window;
  let speakingBtn = null;
  let currentAudio = null;
  let speechToken = 0; // bumped on stop: invalidates in-flight async chains
  let voices = [];

  function ttsLang(code) {
    if (!code) return null;
    if (code === "en") return "en-US";
    if (code === "pt") return "pt-BR";
    if (code.includes("-")) return code; // zh-CN, zh-TW, pt-BR …
    return `${code}-${code.toUpperCase()}`; // tr -> tr-TR, de -> de-DE …
  }

  function refreshVoices() {
    voices = window.speechSynthesis.getVoices() || [];
  }
  if (speechAvailable) {
    refreshVoices();
    window.speechSynthesis.onvoiceschanged = refreshVoices;
  }

  function pickVoice(lang) {
    if (!voices.length) refreshVoices();
    const l = lang.toLowerCase();
    const base = l.split("-")[0];
    return (
      voices.find((v) => v.lang.replace("_", "-").toLowerCase() === l) ||
      voices.find((v) => v.lang.replace("_", "-").toLowerCase().startsWith(base)) ||
      null
    );
  }

  function setSpeakingUI(btn) {
    speakingBtn = btn;
    for (const id of ["speakIn", "speakOut"]) {
      document.getElementById(id).classList.toggle("speaking", document.getElementById(id) === btn);
    }
  }

  function ttsChunk(text) {
    // Google TTS serves ~200 chars per request; cut at word boundaries.
    const chunks = [];
    let rest = text.trim();
    while (rest.length > 190) {
      let cut = rest.lastIndexOf(" ", 190);
      if (cut < 80) cut = 190;
      chunks.push(rest.slice(0, cut));
      rest = rest.slice(cut).trimStart();
    }
    if (rest) chunks.push(rest);
    return chunks;
  }

  function ttsUrl(text, lang) {
    return (
      "https://translate.googleapis.com/translate_tts?ie=UTF-8&client=gtx&tl=" +
      encodeURIComponent(lang) +
      "&q=" +
      encodeURIComponent(text)
    );
  }

  function stopSpeaking() {
    speechToken++;
    if (currentAudio) {
      const a = currentAudio;
      currentAudio = null;
      a.onended = null;
      a.onerror = null;
      a.pause();
      a.removeAttribute("src");
    }
    if (speechAvailable) window.speechSynthesis.cancel();
    setSpeakingUI(null);
  }

  function speakLocal(text, langCode) {
    if (!speechAvailable) {
      setSpeakingUI(null);
      return;
    }
    const u = new SpeechSynthesisUtterance(text);
    const lang = ttsLang(langCode);
    if (lang) {
      u.lang = lang;
      const v = pickVoice(lang);
      if (v) u.voice = v;
    }
    u.onend = () => setSpeakingUI(null);
    u.onerror = () => setSpeakingUI(null);
    window.speechSynthesis.speak(u);
  }

  function ttsUnavailableNotice() {
    status.textContent = "Google TTS unavailable — using local voice";
    setTimeout(() => {
      if (status.textContent.startsWith("Google TTS")) status.textContent = "";
    }, 4000);
  }

  // Fetch one chunk with the page's fetch(): we get the HTTP status, so a
  // failure is diagnosable, and playback runs from a blob (no mid-play
  // network). Returns null when the Google path is unavailable.
  async function fetchChunkBlob(url) {
    try {
      const r = await fetch(url);
      if (!r.ok) {
        console.warn("TTS HTTP", r.status);
        return null;
      }
      const b = await r.blob();
      if (!b.type.startsWith("audio") || b.size < 512) {
        console.warn("TTS unexpected payload:", b.type, b.size);
        return null;
      }
      return URL.createObjectURL(b);
    } catch (e) {
      console.warn("TTS fetch blocked (CORS/network):", e);
      return null;
    }
  }

  function playBlob(url) {
    return new Promise((resolve) => {
      const a = new Audio(url);
      currentAudio = a;
      const done = () => {
        if (currentAudio === a) {
          currentAudio = null;
          URL.revokeObjectURL(url);
        }
        resolve();
      };
      a.onended = done;
      a.onerror = done;
      a.play().catch(done);
    });
  }

  // No-CORS escape hatch: plain audio-element playback of the endpoint URL.
  // If even this errors, the Google path is genuinely unavailable.
  function playElementChain(texts, lang, token) {
    if (token !== speechToken) return;
    if (!texts.length) {
      setSpeakingUI(null);
      return;
    }
    const a = new Audio(ttsUrl(texts[0], lang));
    currentAudio = a;
    a.onended = () => {
      if (currentAudio !== a || token !== speechToken) return;
      currentAudio = null;
      playElementChain(texts.slice(1), lang, token);
    };
    const fail = () => {
      if (currentAudio !== a || token !== speechToken) return;
      currentAudio = null;
      ttsUnavailableNotice();
      speakLocal(texts.join(" "), lang);
    };
    a.onerror = fail;
    a.play().catch(fail);
  }

  async function speakChunks(texts, lang) {
    const token = speechToken;
    let nextBlob = fetchChunkBlob(ttsUrl(texts[0], lang));
    for (let i = 0; i < texts.length; i++) {
      const blobUrl = await nextBlob;
      if (token !== speechToken) return;
      if (!blobUrl) {
        playElementChain(texts.slice(i), lang, token);
        return;
      }
      nextBlob = i + 1 < texts.length ? fetchChunkBlob(ttsUrl(texts[i + 1], lang)) : null;
      await playBlob(blobUrl);
      if (token !== speechToken) return;
    }
    setSpeakingUI(null);
  }

  function speak(text, langCode, btn) {
    if (!text) return;
    if (speakingBtn === btn) {
      stopSpeaking();
      return;
    }
    stopSpeaking();
    setSpeakingUI(btn);
    if (!langCode) {
      // Source is Detect language and nothing has been detected yet:
      // read with the local default voice.
      speakLocal(text, null);
      return;
    }
    speakChunks(ttsChunk(text), langCode);
  }

  for (const id of ["speakIn", "speakOut"]) document.getElementById(id).hidden = false;
  document.getElementById("speakIn").addEventListener("click", () => {
    const lang = fromSel.value === "auto" ? lastDetected : fromSel.value;
    speak(input.value.trim(), lang, document.getElementById("speakIn"));
  });
  document.getElementById("speakOut").addEventListener("click", () => {
    const t = out.textContent;
    if (!t || out.classList.contains("placeholder") || out.classList.contains("error")) return;
    speak(t, toSel.value, document.getElementById("speakOut"));
  });

  // Hotkey: input'u boşaltmıyoruz — eski metin ekranda kalır (flicker ve
  // stash-restore gecikmesi yok), prefill seçimle gelirse üzerine yazar,
  // boş gelirse hiçbir şey yapmaz. Yeni metin akarken konuşma kesilir.
  // Pencere Esc ile gizliyken gelen geç prefill input'u sessizce doldurur;
  // sonraki Win+T'de hazır olur.
  onBridgeEvent("hotkey-pressed", () => {
    stopSpeaking();
    status.textContent = "Reading selection…";
    input.focus();
  });
  onBridgeEvent("prefill", (t) => {
    const sel = (t || "").trim();
    if (sel && sel !== input.value) {
      input.value = sel;
      schedule();
    }
    status.textContent = "";
    updateCount();
    input.focus();
  });
})();
