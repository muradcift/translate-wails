# AGENTS.md — translate-wails (Wails v2 + Go, ana proje)

Electron sürümü (`../translate-app`) donduruldu; aktif geliştirme burada.

## Ürün (değişmez istekler)

- Klasik çevirmen düzeni: üstte dil dropdown'ları + swap, solda input, sağda output.
- Aşırı sade: dil + input + output. Geçmiş/TTS/ayar yok.
- Keyless Google endpoint, API anahtarı yok. Yazarken anında çeviri (300ms debounce).
- `Win+T` her yerden pencereyi açar + seçili metni doldurur. KAYIT YOLU ÇALIŞMAZ: kabuk Win+T'yi tutuyor (`RegisterHotKey(MOD_WIN,T)` → "Hot key is already registered", prob ile saha-doğrulandı; MOD_ALT serbest). Birincil tetik kbdhook.go'daki LL hook (tuşu yutar → `wmAppHotkey` → hotkey thread); RegisterHotKey sadece kombinasyonun boş olduğu makinelerde yedek. Yutulan kombinasyonlardan sonra Win-up maskelenir (Start menü açılmasın).
- `Win+,` uygulamaya odaklıyken ayar panelini aç/kapat (aynı LL hook; odak dışında tuş sisteme bırakılır, Peek bozulmaz). Panel içeriği SONRADAN belirlenecek — şimdilik boş iskelet.
- Varsayılan: kaynak Auto, hedef Türkçe. UI etiketleri İngilizce.

## Komutlar

- `wails dev` — geliştirme. `wails build` — `build/bin/translate-wails.exe` (~11MB).
- `go vet ./...` — hızlı kontrol. Test/lint/CI yok.
- DİKKAT: `go build ./...` köke `translate-wails.exe` yazarken `wails build` ön yüzü de derleyip `build/bin/`e koyar; kökteki exe bayat kalabilir — kullanıcı daima `build/bin/`dekini çalıştırsın.

## Mimari

- `main.go` — pencere (760x500, frameless, `StartHidden`, `SingleInstanceLock` + dolu `UniqueId`, koyu zemin).
- `app.go` — bindingler: `TranslateText(text, source, target)`, `CopyText(t)`. Startup: hotkey döngüsü + klavye hook'u + DWM yuvarlak köşe (DWMWA_WINDOW_CORNER_PREFERENCE=33, DWMWCP_ROUND=2; Win10'da sessizce etkisiz).
- `translate.go` — çeviri motoru (1200-char chunk, parça arası 250ms, 429 yönetimi).
- `winhttp.go` — BİRİNCİL transport WinHTTP/SChannel (Go `net/http` TLS parmak izi bu ağda 429 yiyor; `net/http` sadece yedek).
- `hotkey.go` — `Win+T`: kendi `GetMessage` thread'i (`LockOSThread`, COM init YOK), sıra: göster (`SW_SHOWNA`, odak çalmadan) → `hotkey-pressed` → seçimi goroutine'de yakala (1.2s deadline; gecikme sonucu ATILIR, sadece bu thread `prefill` yayar) → kullanıcı bu sırada Esc ile gizlediyse (`IsWindowVisible`) pencereyi CANLANDIRMA → `SetForegroundWindow` (hotkey thread'i foreground hakkına sahiptir) → `prefill`. `EnumWindows` callback'i package-level (NewCallback ~2000 limiti).
- `kbdhook.go` — LL klavye hook'u, İKİ iş: (1) Win+T tetiği (kabuğa kaptırmadan yut, `wmAppHotkey` ile hotkey thread'ine posta, tekrar filtresi `winTArmed`), (2) Win+, ayar kısayolu (yalnız pencere foreground'dayken). Yutma sonrası menü maskesi: atanmamış tuş (VK 0xE8) enjekte edilir VE ilk Win-up maskelenir — Start kararı hook zincirinin ALTINDA verildiği için sadece yutmak yetmez.
- `foregroundTranslatorWindow` (hotkey.go) ön-plan muafiyetleri ŞART: hook tetiği tuşu kuyruğa girmeden yuttuğu için thread "son giriş olayını almış" sayılmaz → `SetForegroundWindow` başka uygulama ön plandayken sessizce başarısız olur (pencere ARKADA kalır). Sırayla: AttachThreadInput(fg thread) → başarısızsa sentetik Alt vuruşu + tekrar dene.
- `selection.go` — pano tabanlı seçim yakalama (max 5000 rune): pano snapshot (metin + sequence number) → `SendInput` Ctrl+C (basılı Alt/Shift/Win önce synthetically bırakılır) → sequence number poll (≤400ms, kör 250ms sleep yok) → oku → eski metni restore et. UIA YOK (bkz. Gotchas). PowerShell YOK.
- `clipboard.go`, `win32.go` — ham Win32 (`x/sys` only, cgo/gcc YOK). `GetClipboardSequenceNumber` kopyalama başarı testi için.
- `frontend/` — Electron'dan birebir port (düz JS + Vite build). Bridge: `window.go.main.App.*`, `window.runtime.EventsOn/WindowHide`. Hotkey akışı: `hotkey-pressed` input'u TEMİZLEMEZ (eski metin ekranda kalır — flicker/stash gecikmesi yok), `prefill` seçimle gelirse üzerine yazar, boşsa dokunmaz. Swap: diller değişir + output metni input'a taşınıp yeni çiftle çevrilir (eski input düşer); kaynak Detect language ise algılanan dil hedefe gider (algılanan yoksa swap no-op).

## Gotchas

- Yeni bağımlılıkta cgo/gcc gerektirmeyen seç (MSVC toolchain'i yok say).
- **UIA'yı hotkey akışına geri getirme.** Gerçek UIA çağrıları (GetFocusedElement/TextPattern) zamanaşımısızdır: meşgul bir sağlayıcı hotkey thread'ini kilitler, çapraz-apartment servis kendi ana STA'sına (WebView2 host) taşınır → ana thread kilitlenir → "yanıt vermiyor" → WebView2 browser process çıkışı → Wails `os.Exit(-1)`. Sahada görüldü. UIA gerçekten gerekirse: kendi thread'i + katı deadline + geç gelen sonucu at (prefill'i sadece hotkey thread yazar) — ve UIA vtable slotları: `GetFocusedElement=7`, `TextPattern::GetSelection=7`, `TextRange::GetText=12` (yanlış slot = çöp-arity çağrı; bir slot'u yanlış çağırmak bellek bozar).
- `sendCtrlC` basılı modifier'ları (Win/Alt/Shift) önce bırakmak ZORUNDA: `WM_HOTKEY` Win+T key-down anında gelir, kullanıcının Win'i hâlâ basılıdır; bırakılmazsa uygulama Ctrl+Win+C alır, kopyalama olmaz.
- LL keyboard hook (kbdhook.go) KESİNLİKLE kendi pump eden thread'inde durmalı: hotkey thread'i handleHotkey sırasında 1.2s'ye kadar pump etmez, LL callback'ler gecikirse Windows hook'u sessizce kaldırır. Hook callback'i hızlı olmalı (INJECTED=0x10 olayları atla; emit kuyruk+PostMessage'dır, bloklamaz).
- **MOD_WIN kombinasyonlarını RegisterHotKey'le alma düşünülmese bile kabuk tutuyor**: Win+T kanıtlı. Win'e bağlı kısayollar LL hook yoluyla alınır; hook yutmadan önce hiçbir hotkey mekanizması tuşu görmez (LL hook zinciri en dışta).
- Pencereyi CAPTURE'dan ÖNCE odaklarsan Ctrl+C kendi penceremize düşer — sıra kutsal: yakala (önceki uygulama foreground'dayken) → göster/odakla → prefill.
- `SingleInstanceLock.UniqueId` boş bırakılmaz: boş değer makine-genel `wails-app-` ad alanını herhangi bir Wails uygulamasıyla paylaştırır.
- `go build` repo git değilse `-buildvcs=false` ister (sandbox quirk).
