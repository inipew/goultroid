FROM alpine:3.21

# Install seluruh dependencies runtime untuk Alpine:
# - ca-certificates: koneksi TLS/HTTPS Telegram API & layanan eksternal
# - tzdata: zona waktu
# - ffmpeg (menyediakan ffmpeg & ffprobe): pemrosesan audio/video/stiker
# - tesseract-ocr (+ data ind): OCR extraction
# - git: fitur pembaruan sistem (.update / gitupdate)
# - curl & python3: runtime untuk yt-dlp
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    ffmpeg \
    tesseract-ocr \
    tesseract-ocr-data-ind \
    git \
    curl \
    python3 \
    && curl -fsSL https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp \
    && chmod a+rx /usr/local/bin/yt-dlp

WORKDIR /app

# Salin binary goultroid statis dan konfigurasi .env (jika ada)
COPY goultroid ./goultroid
COPY .env* ./

# Pastikan permission executable dan direktori data aman
RUN chmod +x /app/goultroid && \
    mkdir -p /app/data && \
    chmod 700 /app/data

# Mount point untuk data persisten (session, database goultroid.db, cache, storage)
VOLUME ["/app/data"]

ENTRYPOINT ["./goultroid"]
CMD ["run"]
