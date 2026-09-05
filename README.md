# GoUltroid 🚀

Reimplementasi Telegram UserBot **Ultroid** dari Python/Telethon ke **Go** dengan arsitektur modern, type-safe, cepat, dan hemat sumber daya. Menggunakan library MTProto **[gotd/td](https://github.com/gotd/td)**.

---

## 🏛️ Arsitektur

```text
               Telegram MTProto
                      │
                      ▼
               gotd/td Client
                      │
                      ▼
          telegram.updates.Manager (gap & state sync)
                      │
                      ▼
             tg.UpdateDispatcher
                      │
                      ▼
         telegram.Dispatcher (bridge)
                      │
                      ▼
            core.Command Router (quoted args, aliases, case-insensitive)
                      │
                      ▼
        core.Middleware Chain (Recovery → Logging → Permission → Timeout)
                      │
                      ▼
             core.Context API (Reply, Edit, Delete, React, GetReply)
                      │
           ┌──────────┴──────────┐
           ▼                     ▼
      plugins/ping          plugins/help
```

---

## 🚀 Fitur Utama

- **Zero CGO & Cross-Platform**: Dapat dikompilasi ke single binary tanpa dependensi eksternal C.
- **Security & Multi-Tier Permissions**:
  - `Owner` — Hak akses penuh terhadap seluruh perintah userbot.
  - `Sudo` — Pengguna terpercaya yang diizinkan menjalankan perintah tertentu.
  - `Everyone` — Perintah publik.
- **Clean Context Abstraction**: Plugin hanya berinteraksi via `*core.Context`, tidak pernah langsung menyentuh raw MTProto client.
- **Robust Command Router**: Mendukung single/double quote argument (`.cmd "hello world" arg2`), multi-alias (`.ping`, `.p`, `.latency`), dan case-insensitive (`.PING` == `.ping`).
- **Resilient Update Pipeline**: Menggunakan `updates.Manager` dari `gotd/td` untuk menjamin sinkronisasi urutan `pts/qts/seq` dan pencegahan gap update.
- **Graceful Shutdown**: Menangkap `SIGINT` dan `SIGTERM` untuk menutup koneksi dan membersihkan resources plugin.

---

## ⚙️ Persyaratan

- Go 1.24+
- Akun Telegram (API ID dan API Hash dari [my.telegram.org](https://my.telegram.org))

---

## 📦 Instalasi & Penggunaan

### 1. Clone & Setup Konfigurasi

```bash
cp .env.example .env
```

Edit file `.env`:

```env
APP_ID=12345678
APP_HASH=your_telegram_app_hash
PHONE=+628123456789
SESSION_FILE=data/session.json
PREFIX=.
OWNER_ID=123456789
SUDO_USERS=
LOG_LEVEL=info
```

### 2. Jalankan

```bash
go run ./cmd/goultroid
```

Saat pertama kali dijalankan, masukkan kode verifikasi Telegram yang dikirimkan ke aplikasi Telegram Anda (serta password 2FA jika aktif). Session akan tersimpan di `data/session.json`.

---

## 🐳 Deployment via Docker

```bash
# Build image
docker build -t goultroid .

# Run container
docker run -it --rm \
  -v "$(pwd)/data:/app/data" \
  --env-file .env \
  goultroid
```

---

## 🧩 Built-in Commands

| Perintah | Alias | Kategori | Izin | Deskripsi |
|---|---|---|---|---|
| `.ping` | `.p`, `.latency` | Utility | Everyone | Mengukur respons dan latensi userbot |
| `.alive` | `.a` | Utility | Everyone | Menampilkan status aktif, uptime, versi Go, dan resource usage |
| `.help` | `.h`, `.commands` | Utility | Everyone | Menampilkan daftar perintah atau detail perintah |
| `.pin` | - | Admin | Sudo | Sematkan pesan reply (opsi `silent` untuk hening) |
| `.unpin` | - | Admin | Sudo | Lepas sematan pesan yang di-reply |
| `.forward` | `.fwd` | Utility | Sudo | Teruskan pesan reply ke chat saat ini atau target yang ditentukan |
| `.download` | `.dl` | Media | Sudo | Unduh media (foto/dokumen/video/audio) dari pesan yang di-reply ke server lokal |
| `.addsudo` | - | Admin | Owner | Tambahkan user ke daftar sudo secara dinamis |
| `.delsudo` | - | Admin | Owner | Hapus user dari daftar sudo |
| `.sudolist` | `.sudos` | Admin | Owner | Tampilkan daftar semua sudo users aktif |
| `.ban` | - | Admin | Sudo | Blokir pengguna dari grup (opsional sertakan alasan) |
| `.unban` | - | Admin | Sudo | Buka blokir pengguna di grup |
| `.kick` | - | Admin | Sudo | Keluarkan pengguna dari grup |
| `.mute` | - | Admin | Sudo | Bisukan pengguna (opsi durasi: `10m`, `2h`, `1d`) |
| `.unmute` | - | Admin | Sudo | Buka status bisu pengguna di grup |
| `.purge` | - | Admin | Sudo | Hapus pesan massal secara aman & scoped per forum topic |
| `.save` | - | Notes | Sudo | Simpan teks catatan di chat (mendukung pesan reply) |
| `.get` | - | Notes | Sudo | Ambil dan kirim isi catatan tersimpan |
| `.notes` | - | Notes | Sudo | Tampilkan daftar semua catatan di chat saat ini |
| `.clear` | - | Notes | Sudo | Hapus catatan yang tersimpan |
| `.afk` | - | AFK | Owner | Aktifkan mode AFK dengan alasan opsional (auto-reply & auto-unafk) |
| `.mediainfo` | `.media`, `.minfo` | Media | Everyone | Inspeksi metadata, resolusi, durasi, dan ukuran media |
| `.extractaudio` | `.extaudio` | Media | Sudo | Ekstrak track audio dari video/dokumen ke format MP3 |
| `.sticker` | `.stk` | Media | Sudo | Konversi foto/gambar yang di-reply menjadi Telegram sticker (512x512) |
| `.whois` | `.info`, `.userinfo` | Info | Everyone | Tampilkan informasi profil lengkap pengguna (ID, username, status, bio) |
| `.chatinfo` | `.groupinfo`, `.cinfo` | Info | Sudo | Tampilkan metadata lengkap grup/supergroup/channel saat ini |
| `.exec` | `.sh`, `.bash`, `.cmd` | System | Owner | Eksekusi shell command di host secara aman (timeout 60s & auto-upload) |
| `.restart` | - | System | Owner | Restart proses userbot secara anggun dan konfirmasi otomatis |
| `.filter` | - | Filters | Sudo | Simpan auto-reply berbasis kata kunci per chat (mendukung teks reply) |
| `.stop` | - | Filters | Sudo | Hapus filter kata kunci aktif di chat saat ini |
| `.filters` | - | Filters | Sudo | Tampilkan daftar semua filter kata kunci aktif di chat saat ini |
| `.roll` | `.dice` | Fun | Sudo | Lempar dadu atau angka acak (e.g. `.roll` atau `.roll 20`) |
| `.shrug` | - | Fun | Sudo | Kirim ekspresi shrug ¯\\_(ツ)_/¯ |
| `.tableflip` | - | Fun | Sudo | Kirim ekspresi tableflip (╯°□°)╯︵ ┻━┻ |
| `.unflip` | - | Fun | Sudo | Kirim ekspresi unflip ┬─┬ノ( º _ ºノ) |
| `.mock` | - | Fun | Sudo | Konversi teks menjadi SpongeBob mock case selang-seling huruf |

---

## 🧪 Testing

```bash
# Jalankan seluruh unit test dengan race detector
go test -v -race ./...

# Coverage report
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```
