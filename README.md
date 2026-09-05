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

---

## 🧪 Testing

```bash
# Jalankan seluruh unit test dengan race detector
go test -v -race ./...

# Coverage report
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```
