# coderbuzz/picoclaw: rencana fork

Fork `sipeed/picoclaw` untuk asisten pribadi pemilik (WhatsApp nomor pribadi sebagai perangkat tertaut + bot Telegram),
pengganti ZeroClaw di VM kecil (964 MB RAM). Repo ini PUBLIK: jangan pernah menulis nomor telepon, JID grup, token,
isi chat, atau nama orang di repo ini (kode, tes, PR, commit). Pakai contoh fiktif (`1203630000000000@g.us`, `628000000000`).

## Keputusan
- K1. `main` fork = upstream + patch kita. Sinkron upstream lewat merge `upstream/main` (bukan rebase).
- K2. Build: `make build GO_BUILD_TAGS=goolm,stdjson,whatsapp_native GOMODCACHE=$(go env GOMODCACHE) GOCACHE=$(go env GOCACHE)`
  di VM Tencent (Go 1.26), biner linux/amd64 disalin ke VM bot. Jangan build di VM bot.
- K3. Config WhatsApp native: `type: "whatsapp_native"`, field di bawah `settings` (lihat `docs/guides/chat-apps.md`).
- K4. Patch ditulis supaya bisa dikirim ke upstream: perilaku lama tetap default bila ada opsi baru, kecuali bug keamanan.
- K5. Bot di grup WA hanya merespons bila: di-mention (JID nomor ATAU LID akun bot), atau membalas (reply/quote) pesan bot,
  atau pesan diawali prefix `group_trigger.prefixes`. Pesan dari akun bot sendiri (`IsFromMe`) tidak pernah diproses.
- K6. Grup yang boleh dilayani dibatasi allowlist JID grup (setting baru). Allowlist kosong + `group_policy` default = perilaku lama.

## Langkah

| ID | Isi | Status |
|---|---|---|
| S1 | Bump whatsmeow 20261017 + mautrix v0.32.0 (fix 405), perbaiki contoh config docs | selesai (#1) |
| S2 | WA grup: gate mention/reply/prefix, skip IsFromMe, allowlist grup, allow_from normalisasi LID/nomor | selesai (#2) |
| S3 | Pesan duluan & cron ke grup memakai agen grup yang benar; riwayat agen yang di-route (context manager legacy) | selesai (#3) |
| S3b | Dedup balasan cron vs tool message ke grup (kunci sesi route) | selesai (#4) |
| S4 | WA media: gambar/stiker masuk -> vision, voice note -> transkripsi (`voice`), dokumen; kirim gambar/dokumen keluar | selesai (PR s4-wa-media) |
| S5 | (opsional, keputusan pemilik nanti) konteks pasif grup: simpan pesan grup non-trigger ke sesi tanpa memicu balasan | |
| S6 | Path baca/tulis ekstra per agen lewat frontmatter AGENT.md (`allow_read_paths`/`allow_write_paths`), ditambahkan ke global hanya untuk agen itu | PR s6-agent-paths |
| S7 | Isolasi bubblewrap per agen (`agents.list[].isolation`): exec agen terisolasi, tanpa instance root/HOME asli, jaringan mati default | PR s7-agent-isolation |

### S2 — kriteria selesai
1. `NewWhatsAppNativeChannel` memasang `channels.WithGroupTrigger(...)` dari config channel; `handleIncoming` memakai
   `ShouldRespondInGroup` (atau setara) untuk chat `@g.us`, dan mengisi `Mentioned` di konteks inbound.
2. Mention terdeteksi dari `ExtendedTextMessage.ContextInfo.MentionedJID` (dan ContextInfo pada pesan gambar/video/dokumen)
   dibandingkan dengan `client.Store.ID` (tanpa device, `ToNonAD()`) dan LID akun bot (`client.Store.LID`).
   Reply ke pesan bot: `ContextInfo.Participant` = JID/LID bot. Keduanya dihitung trigger.
3. `evt.Info.IsFromMe` -> abaikan (DM dan grup).
4. Setting baru di `WhatsAppSettings`: `group_policy` (`"open"` default = perilaku lama, `"allowlist"`, `"disabled"`) dan
   `group_allow_from` ([]string JID grup). Pesan dari grup yang ditolak dibuang tanpa log isi pesan.
5. `allow_from` cocok untuk JID dengan/atau tanpa suffix device (`628..:12@s.whatsapp.net` == `628..@s.whatsapp.net`) dan
   untuk LID (`...@lid`); bila whatsmeow punya pemetaan LID<->nomor (`Store.LIDs`), keduanya diterima.
6. Unit test untuk: deteksi mention (nomor, LID, tidak di-mention), reply-ke-bot, IsFromMe, ketiga group_policy, normalisasi allow_from.
   Logika murni dipisah ke fungsi tanpa client supaya bisa dites tanpa koneksi.
7. Docs `docs/guides/chat-apps.md` bagian WhatsApp diperbarui (setting baru + contoh).
8. Bukti di PR: output `go test -tags goolm,stdjson,whatsapp_native ./pkg/channels/... ./pkg/config/...`, `go vet` paket yang diubah, `make build` (tag di K2) hijau.

## Aturan eksekutor
- Satu langkah = satu cabang `s<N>-<topik>` dari `main` = satu PR ke `coderbuzz/picoclaw` (`gh pr create -R coderbuzz/picoclaw --base main`;
  tanpa `-R` gh mengarah ke upstream sipeed: JANGAN buka PR ke upstream).
- Uji secukupnya: tes paket yang diubah + `make build`. Tidak perlu tes seluruh repo kecuali menyentuh paket bersama (`pkg/channels/base.go`, `pkg/agent`, `pkg/config`): maka jalankan `go test ./pkg/...` sekali sebelum PR.
- Jangan merge, push ke `main`, force-push, atau menjalankan biner dengan akun WhatsApp/Telegram sungguhan. Uji koneksi langsung hanya oleh leader.
- Gaya: ikuti kode sekitarnya, komentar hanya untuk "kenapa", tanpa dependensi baru.
