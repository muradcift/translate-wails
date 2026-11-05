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
      paintOutput("Translation will appear here…", false, true);
    }
    schedule();
  });

  let timer = null;
  let seq = 0;
  let debounceMs = 300;
  const RETRY_DELAY = 1500;
  const RATE_LIMIT_BACKOFF = 1200;
  const BACKOFF_DECAY = 600;
  const SOFT_INTERVAL = 250;
  const CACHE_CAP = 40;
  let lastKey = "";
  let lastCallAt = 0;
  let minInterval = SOFT_INTERVAL; // adaptive throttle floor (ms); soft floor between network calls
  let backoffRemaining = 0; // successes needed to clear the backoff
  const translationCache = new Map(); // `${from}|${to}|${text}` -> {translatedText, detectedSource}
  let countRaf = 0;
  function schedule() {
    clearTimeout(timer);
    timer = setTimeout(() => doRun(), debounceMs);
  }

  function isRateLimit(err) {
    if (err && err.code === "RATE_LIMIT") return true;
    const msg = err instanceof Error ? err.message : String(err == null ? "" : err);
    return /Rate limited/i.test(msg);
  }

  function paintOutput(text, isError, isPlaceholder) {
    if (out.textContent !== text) out.textContent = text;
    const hasErr = out.classList.contains("error");
    if (isError !== hasErr) {
      if (isError) out.classList.add("error");
      else out.classList.remove("error");
    }
    const hasPh = out.classList.contains("placeholder");
    if (isPlaceholder !== hasPh) {
      if (isPlaceholder) out.classList.add("placeholder");
      else out.classList.remove("placeholder");
    }
  }

  function updateCount() {
    const compute = () => {
      const v = input.value;
      const len = v.length;
      let words = 0;
      let inWord = false;
      for (let i = 0; i < v.length; i++) {
        const c = v.charCodeAt(i);
        const ws = c === 32 || c === 9 || c === 10 || c === 13 || c === 12 || c === 11;
        if (ws) inWord = false;
        else if (!inWord) {
          inWord = true;
          words++;
        }
      }
      const next = `${words} Words • ${len} Characters`;
      if (count.textContent !== next) count.textContent = next;
    };
    if (typeof requestAnimationFrame !== "function") {
      compute();
      return;
    }
    if (countRaf) return;
    countRaf = requestAnimationFrame(() => {
      countRaf = 0;
      compute();
    });
  }

  async function doRun(retry = 0, seqId = null) {
    stopSpeaking();
    const text = input.value.trim();
    updateCount();
    if (!text) {
      paintOutput("Translation will appear here…", false, true);
      status.textContent = "";
      return;
    }
    const key = `${fromSel.value}|${toSel.value}|${text}`;
    if (retry === 0) {
      if (key === lastKey) return;
      // Client-side cache: instant paint, no network, no throttle wait.
      const cached = translationCache.get(key);
      if (cached) {
        if (seqId === null) seqId = ++seq;
        if (seqId !== seq) return;
        paintOutput(cached.translatedText, false, false);
        status.textContent = "";
        lastKey = key;
        if (cached.detectedSource && cached.detectedSource !== lastDetected) {
          lastDetected = cached.detectedSource;
          updateSpellcheck();
        }
        return;
      }
      if (seqId === null) seqId = ++seq;
      const wait = minInterval - (Date.now() - lastCallAt);
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
      paintOutput(r.translatedText, false, false);
      status.textContent = "";
      lastKey = key;
      if (r.detectedSource && r.detectedSource !== lastDetected) {
        lastDetected = r.detectedSource;
        updateSpellcheck();
      }
      // Store in cache (FIFO eviction on overflow).
      if (!translationCache.has(key)) {
        if (translationCache.size >= CACHE_CAP) {
          const oldest = translationCache.keys().next().value;
          translationCache.delete(oldest);
        }
      }
      translationCache.set(key, { translatedText: r.translatedText, detectedSource: r.detectedSource });
      // Decay adaptive backoff on success.
      if (backoffRemaining > 0) {
        backoffRemaining--;
        minInterval = backoffRemaining > 0 ? BACKOFF_DECAY : SOFT_INTERVAL;
      }
    } catch (err) {
      if (seqId !== seq) return;
      if (isRateLimit(err)) {
        minInterval = RATE_LIMIT_BACKOFF;
        // 429 sonrası 4 başarılı çağrı decay tabanında kalır, sonra 250 ms'ye döner.
        backoffRemaining = 4;
        // Backend zaten ortak cooldown ile retry yaptığı için ön yüz tek retry eder.
        if (retry < 1) {
          status.textContent = "Rate limited, retrying…";
          clearTimeout(timer);
          timer = setTimeout(() => {
            if (seqId === seq) doRun(retry + 1, seqId);
          }, RETRY_DELAY);
          return;
        }
      }
      const msg = err instanceof Error ? err.message : String(err);
      paintOutput(msg, true, false);
      status.textContent = "";
    }
  }

  input.addEventListener("input", () => {
    updateCount();
    schedule();
  });

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

  // ---- Text-to-speech: keyless Google TTS via the Go backend. The
  // WebView2 network layer could not reach translate_tts from inside the
  // page (fetch and <audio> both failed), so the MP3 is downloaded by the
  // same Go transport that does the translation (WinHTTP primary) and
  // arrives as base64; we play it from a blob. Local speechSynthesis
  // voices are the fallback for offline / throttled moments and for when
  // the source language is unknown. ----
  const speechAvailable = "speechSynthesis" in window;
  let speakingBtn = null;
  let currentAudio = null;
  let currentAudioUrl = null;
  let speechToken = 0; // bumped on stop: invalidates in-flight async chains
  let voices = [];
  const pendingTtsUrls = new Set(); // blob URLs not yet played/revoked

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

  function stopSpeaking() {
    speechToken++;
    // Revoke every blob URL still pending (prefetched / not yet played).
    for (const u of pendingTtsUrls) {
      try {
        URL.revokeObjectURL(u);
      } catch {}
    }
    pendingTtsUrls.clear();
    if (currentAudio) {
      const a = currentAudio;
      currentAudio = null;
      a.onended = null;
      a.onerror = null;
      a.pause();
      a.removeAttribute("src");
    }
    if (currentAudioUrl) {
      try {
        URL.revokeObjectURL(currentAudioUrl);
      } catch {}
      currentAudioUrl = null;
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

  // One chunk from the Go backend (WinHTTP transport, the same wire the
  // translation uses) as base64 MP3, played from a blob URL.
  function speakBridge(text, lang) {
    const app = appBinding();
    if (!app) return Promise.reject(new Error("no bridge (plain vite dev?)"));
    return app.SpeakTTS(text, lang);
  }

  function b64ToBlobUrl(b64) {
    const bin = atob(b64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    return URL.createObjectURL(new Blob([bytes], { type: "audio/mpeg" }));
  }

  async function ttsChunkUrl(text, lang) {
    const myToken = speechToken;
    const b64 = await speakBridge(text, lang);
    const url = b64ToBlobUrl(b64);
    if (myToken !== speechToken) {
      URL.revokeObjectURL(url);
      return url;
    }
    pendingTtsUrls.add(url);
    return url;
  }

  function playBlob(url) {
    return new Promise((resolve) => {
      const a = new Audio(url);
      currentAudio = a;
      currentAudioUrl = url;
      pendingTtsUrls.delete(url);
      const done = () => {
        if (currentAudio === a) {
          currentAudio = null;
          URL.revokeObjectURL(url);
          if (currentAudioUrl === url) currentAudioUrl = null;
        }
        resolve();
      };
      a.onended = done;
      a.onerror = done;
      a.play().catch(done);
    });
  }

  async function speakChunks(texts, lang) {
    const token = speechToken;
    let next = ttsChunkUrl(texts[0], lang);
    for (let i = 0; i < texts.length; i++) {
      let blobUrl = null;
      try {
        blobUrl = await next;
      } catch (e) {
        console.warn("TTS bridge failed:", e);
      }
      if (token !== speechToken) {
        if (blobUrl) {
          pendingTtsUrls.delete(blobUrl);
          try {
            URL.revokeObjectURL(blobUrl);
          } catch {}
        }
        return;
      }
      if (!blobUrl) {
        ttsUnavailableNotice();
        speakLocal(texts.slice(i).join(" "), lang);
        return;
      }
      // Prefetch the next chunk while this one plays.
      const prefetch = i + 1 < texts.length ? ttsChunkUrl(texts[i + 1], lang) : null;
      next = prefetch;
      await playBlob(blobUrl);
      if (token !== speechToken) {
        try {
          URL.revokeObjectURL(blobUrl);
        } catch {}
        if (prefetch) {
          prefetch
            .then((u) => {
              if (token !== speechToken && u) {
                pendingTtsUrls.delete(u);
                try {
                  URL.revokeObjectURL(u);
                } catch {}
              }
            })
            .catch(() => {});
        }
        return;
      }
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
