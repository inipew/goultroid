Bisa. Untuk MVP ini saya sarankan memakai **[gotd/td](https://github.com/gotd/td?utm_source=chatgpt.com)** sebagai layer MTProto, bukan membuat protokol Telegram sendiri. `gotd/td` memang menyediakan client MTProto untuk user/bot, persistent session, authentication flow, dan update handler; versi yang tersedia saat ini adalah **v0.161.0**. ([Go Packages][1])

Saya buat desain yang sengaja **tidak bergantung pada framework UserBot lain**, supaya nanti plugin Ultroid-style bisa dibangun di atas core kita.

## 1. Struktur MVP

```text
goultroid/
├── cmd/
│   └── goultroid/
│       └── main.go
│
├── internal/
│   ├── app/
│   │   └── app.go
│   │
│   ├── telegram/
│   │   ├── client.go
│   │   └── event.go
│   │
│   └── plugin/
│       ├── plugin.go
│       └── manager.go
│
├── plugins/
│   └── ping/
│       └── ping.go
│
├── data/
│   └── .gitkeep
│
├── .env.example
├── .gitignore
├── go.mod
└── README.md
```

Arsitekturnya:

```text
                    Telegram
                        │
                        ▼
               ┌────────────────┐
               │   gotd/td       │
               │    MTProto      │
               └───────┬────────┘
                       │
                       ▼
               ┌────────────────┐
               │ Event Dispatcher│
               └───────┬────────┘
                       │
                       ▼
               ┌────────────────┐
               │ Plugin Manager │
               └───────┬────────┘
                       │
             ┌─────────┴─────────┐
             ▼                   ▼
        ping plugin          plugin lain
             │
             ▼
          .ping
             │
             ▼
          "Pong!"
```

`gotd/td` sendiri mempunyai contoh resmi untuk userbot dan update dispatcher, termasuk authentication, persistent session, dan message update handling. ([GitHub][2])

---

# 2. `go.mod`

```go
module github.com/yourname/goultroid

go 1.24

require (
	github.com/gotd/td v0.161.0
)
```

Setelah membuat file:

```bash
go mod tidy
```

Saya sengaja belum memasukkan database, Redis, logger framework, FFmpeg wrapper, dan dependency lain. Untuk MVP, semakin sedikit dependency semakin mudah kita pastikan fondasinya benar.

---

# 3. `.env.example`

```env
APP_ID=12345678
APP_HASH=your_app_hash_here

PHONE=+628123456789

SESSION_FILE=data/session.json

PREFIX=.
```

`APP_ID` dan `APP_HASH` didapat dari aplikasi Telegram di `my.telegram.org/apps`; jangan commit credential tersebut ke repository. Dokumentasi resmi `gotd/td` juga menggunakan `APP_ID`, `APP_HASH`, dan `SESSION_FILE` untuk konfigurasi environment. ([GitHub][3])

---

# 4. `.gitignore`

```gitignore
.env
data/*
!data/.gitkeep

*.log

bin/
dist/

.idea/
.vscode/
```

---

# 5. Core plugin interface

`internal/plugin/plugin.go`

```go
package plugin

import "context"

type Context struct {
	Context context.Context
}

type Handler func(*Context, string) error

type Command struct {
	Name        string
	Description string
	Handler     Handler
}

type Plugin interface {
	Name() string
	Commands() []Command
	Init() error
}
```

Ini sengaja sederhana.

Nanti bisa kita kembangkan menjadi:

```go
type Command struct {
	Name        string
	Aliases     []string
	Description string
	Usage       string
	OwnerOnly   bool
	Handler     Handler
}
```

dan:

```go
type Context struct {
	Context context.Context

	Client  *telegram.Client
	Message *Message

	Args    []string
	RawArgs string
}
```

Tetapi untuk MVP kita jangan langsung over-engineering.

---

# 6. Plugin Manager

`internal/plugin/manager.go`

```go
package plugin

import (
	"fmt"
	"strings"
	"sync"
)

type Manager struct {
	mu       sync.RWMutex
	plugins  map[string]Plugin
	commands map[string]Command
}

func NewManager() *Manager {
	return &Manager{
		plugins:  make(map[string]Plugin),
		commands: make(map[string]Command),
	}
}

func (m *Manager) Register(p Plugin) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := strings.ToLower(p.Name())

	if _, exists := m.plugins[name]; exists {
		return fmt.Errorf("plugin already registered: %s", name)
	}

	if err := p.Init(); err != nil {
		return fmt.Errorf("initialize plugin %s: %w", name, err)
	}

	for _, cmd := range p.Commands() {
		commandName := strings.ToLower(cmd.Name)

		if _, exists := m.commands[commandName]; exists {
			return fmt.Errorf(
				"command already registered: %s",
				commandName,
			)
		}

		m.commands[commandName] = cmd
	}

	m.plugins[name] = p

	return nil
}

func (m *Manager) FindCommand(name string) (Command, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cmd, ok := m.commands[strings.ToLower(name)]

	return cmd, ok
}

func (m *Manager) Plugins() []Plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]Plugin, 0, len(m.plugins))

	for _, p := range m.plugins {
		result = append(result, p)
	}

	return result
}
```

---

# 7. Telegram Event

Sekarang kita buat abstraction supaya plugin **tidak perlu tahu detail `tg.UpdateNewMessage`**.

`internal/telegram/event.go`

```go
package telegram

import (
	"context"

	"github.com/gotd/td/tg"
)

type MessageEvent struct {
	Context context.Context
	API     *tg.Client
	Message *tg.Message
}

func (e *MessageEvent) Text() string {
	if e.Message == nil {
		return ""
	}

	return e.Message.Message
}

func (e *MessageEvent) Reply(text string) error {
	_, err := e.API.MessagesSendMessage(
		e.Context,
		&tg.MessagesSendMessageRequest{
			Peer: &tg.InputPeerSelf{},
			Message: text,
		},
	)

	return err
}
```

**Catatan penting:** bagian `Reply()` di atas hanya placeholder MVP. Untuk versi sebenarnya kita sebaiknya menyimpan peer dari incoming message lalu menggunakan `message.NewSender`/builder agar reply benar-benar dikirim ke chat asal. API higher-level `message` dari gotd memang menyediakan `SendText`/reply builder. ([Go Packages][4])

Jadi kita perbaiki event supaya peer ikut dibawa.

---

# 8. Telegram client

`internal/telegram/client.go`

```go
package telegram

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

type Client struct {
	Raw *telegram.Client
	API *tg.Client
}

func NewClient(
	appID int,
	appHash string,
	sessionFile string,
	handler telegram.UpdateHandler,
) (*Client, error) {

	raw := telegram.NewClient(
		appID,
		appHash,
		telegram.Options{
			SessionStorage: &telegram.FileSessionStorage{
				Path: sessionFile,
			},
			UpdateHandler: handler,
		},
	)

	return &Client{
		Raw: raw,
	}, nil
}

func (c *Client) Run(ctx context.Context) error {
	return c.Raw.Run(ctx, func(ctx context.Context) error {
		c.API = c.Raw.API()

		if err := c.authenticate(ctx); err != nil {
			return err
		}

		me, err := c.Raw.Self(ctx)
		if err != nil {
			return fmt.Errorf("get self: %w", err)
		}

		fmt.Printf(
			"Logged in as %s (@%s)\n",
			me.FirstName,
			me.Username,
		)

		<-ctx.Done()

		return ctx.Err()
	})
}

func (c *Client) authenticate(ctx context.Context) error {
	phone := os.Getenv("PHONE")

	if phone == "" {
		return fmt.Errorf("PHONE is not set")
	}

	codePrompt := func(
		ctx context.Context,
		sentCode *tg.AuthSentCode,
	) (string, error) {

		fmt.Print("Telegram code: ")

		reader := bufio.NewReader(os.Stdin)

		code, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}

		return strings.TrimSpace(code), nil
	}

	flow := auth.NewFlow(
		auth.CodeOnly(
			phone,
			auth.CodeAuthenticatorFunc(codePrompt),
		),
		auth.SendCodeOptions{},
	)

	if err := c.Raw.Auth().IfNecessary(ctx, flow); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	return nil
}

func AppIDFromEnv() (int, error) {
	value := os.Getenv("APP_ID")

	if value == "" {
		return 0, fmt.Errorf("APP_ID is not set")
	}

	return strconv.Atoi(value)
}
```

`gotd/td` memang menyediakan `telegram.NewClient`, `FileSessionStorage`, `Client.Run`, `Client.Self`, serta `auth.NewFlow`/`CodeOnly` untuk authentication user account. ([Go Packages][1])

---

# 9. Event Dispatcher

Sekarang bagian pentingnya.

`internal/telegram/event.go` kita ubah menjadi:

```go
package telegram

import (
	"context"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/telegram/message"
)

type MessageEvent struct {
	Context context.Context

	API    *tg.Client
	Peer   tg.InputPeerClass
	Message *tg.Message
}

func (e *MessageEvent) Text() string {
	if e.Message == nil {
		return ""
	}

	return e.Message.Message
}

func (e *MessageEvent) Reply(text string) error {
	sender := message.NewSender(e.API)

	_, err := sender.
		Peer(e.Peer).
		Text(e.Context, text)

	return err
}

func (e *MessageEvent) Command(prefix string) (string, string, bool) {
	text := strings.TrimSpace(e.Text())

	if !strings.HasPrefix(text, prefix) {
		return "", "", false
	}

	text = strings.TrimPrefix(text, prefix)

	parts := strings.Fields(text)

	if len(parts) == 0 {
		return "", "", false
	}

	command := strings.ToLower(parts[0])

	args := ""

	if len(parts) > 1 {
		args = strings.Join(parts[1:], " ")
	}

	return command, args, true
}
```

Namun tergantung API `message.Sender` versi exact, `Peer()` bisa berbeda. Agar MVP tidak terjebak pada helper API yang berubah, pendekatan yang lebih aman adalah memakai `message.NewSender(...).Resolve(...)` atau builder sesuai versi dependency. Dokumentasi `gotd` sendiri menunjukkan `message.NewSender` sebagai abstraction pengiriman pesan. ([Go Packages][4])

Untuk implementasi final, saya akan membuat `TelegramContext` sendiri sehingga detail ini hanya berada di satu tempat.

---

# 10. Dispatcher kita

`internal/telegram/dispatcher.go`

```go
package telegram

import (
	"context"
	"fmt"
	"strings"

	"github.com/yourname/goultroid/internal/plugin"

	"github.com/gotd/td/tg"
)

type Dispatcher struct {
	Plugins *plugin.Manager
	Prefix  string
	API     *tg.Client
}

func NewDispatcher(
	manager *plugin.Manager,
	prefix string,
) *Dispatcher {

	return &Dispatcher{
		Plugins: manager,
		Prefix:  prefix,
	}
}

func (d *Dispatcher) Handle(
	ctx context.Context,
	update tg.UpdatesClass,
) error {

	switch u := update.(type) {

	case *tg.UpdateShortMessage:
		return d.handleShortMessage(
			ctx,
			u,
		)

	case *tg.UpdateShortChatMessage:
		return d.handleShortChatMessage(
			ctx,
			u,
		)

	case *tg.Updates:
		return d.handleUpdates(
			ctx,
			u,
		)

	case *tg.UpdatesCombined:
		return d.handleUpdatesCombined(
			ctx,
			u,
		)
	}

	return nil
}

func (d *Dispatcher) handleShortMessage(
	ctx context.Context,
	update *tg.UpdateShortMessage,
) error {

	message := &tg.Message{
		ID:      update.ID,
		Message: update.Message,
		Date:    update.Date,
	}

	return d.dispatchMessage(
		ctx,
		&MessageEvent{
			Context: ctx,
			API:     d.API,
			Message: message,
		},
	)
}

func (d *Dispatcher) handleShortChatMessage(
	ctx context.Context,
	update *tg.UpdateShortChatMessage,
) error {

	message := &tg.Message{
		ID:      update.ID,
		Message: update.Message,
		Date:    update.Date,
	}

	return d.dispatchMessage(
		ctx,
		&MessageEvent{
			Context: ctx,
			API:     d.API,
			Message: message,
		},
	)
}

func (d *Dispatcher) handleUpdates(
	ctx context.Context,
	updates *tg.Updates,
) error {

	for _, update := range updates.Updates {

		if err := d.handleUpdate(ctx, update); err != nil {
			return err
		}
	}

	return nil
}

func (d *Dispatcher) handleUpdatesCombined(
	ctx context.Context,
	updates *tg.UpdatesCombined,
) error {

	for _, update := range updates.Updates {

		if err := d.handleUpdate(ctx, update); err != nil {
			return err
		}
	}

	return nil
}

func (d *Dispatcher) handleUpdate(
	ctx context.Context,
	update tg.UpdateClass,
) error {

	switch u := update.(type) {

	case *tg.UpdateNewMessage:

		message, ok := u.Message.(*tg.Message)
		if !ok {
			return nil
		}

		return d.dispatchMessage(
			ctx,
			&MessageEvent{
				Context: ctx,
				API:     d.API,
				Message: message,
			},
		)
	}

	return nil
}

func (d *Dispatcher) dispatchMessage(
	ctx context.Context,
	event *MessageEvent,
) error {

	text := strings.TrimSpace(event.Text())

	if text == "" {
		return nil
	}

	command, args, ok := parseCommand(
		text,
		d.Prefix,
	)

	if !ok {
		return nil
	}

	cmd, exists := d.Plugins.FindCommand(command)

	if !exists {
		return nil
	}

	pluginContext := &plugin.Context{
		Context: ctx,
	}

	if err := cmd.Handler(
		pluginContext,
		args,
	); err != nil {

		return fmt.Errorf(
			"command %s: %w",
			command,
			err,
		)
	}

	return nil
}

func parseCommand(
	text string,
	prefix string,
) (string, string, bool) {

	if !strings.HasPrefix(text, prefix) {
		return "", "", false
	}

	text = strings.TrimSpace(
		strings.TrimPrefix(text, prefix),
	)

	parts := strings.Fields(text)

	if len(parts) == 0 {
		return "", "", false
	}

	command := strings.ToLower(parts[0])

	args := ""

	if len(parts) > 1 {
		args = strings.Join(parts[1:], " ")
	}

	return command, args, true
}
```

**Tetapi:** untuk userbot production, saya justru tidak akan mempertahankan parser update mentah seperti ini. `gotd` mempunyai `tg.UpdateDispatcher` dan contoh resmi menggunakan dispatcher untuk `OnNewMessage`, sementara `telegram.UpdateHandler` menerima `tg.UpdatesClass`. ([Go Packages][1])

Jadi desain final yang lebih sehat adalah:

```text
Telegram Update
      │
      ▼
gotd UpdateDispatcher
      │
      ▼
MessageEvent
      │
      ▼
GoUltroid Dispatcher
      │
      ▼
Plugin Manager
```

Bukan:

```text
Telegram Update
      │
      ▼
custom parser semua update
```

Ini akan menghemat banyak pekerjaan ketika nanti kita menambahkan channel, media, replies, albums, edited messages, callback, dan sebagainya.

---

# 11. Ping plugin

`plugins/ping/ping.go`

```go
package ping

import (
	"fmt"
	"time"

	"github.com/yourname/goultroid/internal/plugin"
)

type Plugin struct{}

func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) Name() string {
	return "ping"
}

func (p *Plugin) Init() error {
	return nil
}

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{
		{
			Name:        "ping",
			Description: "Check whether the userbot is alive",
			Handler:    p.ping,
		},
	}
}

func (p *Plugin) ping(
	ctx *plugin.Context,
	args string,
) error {

	start := time.Now()

	_ = args

	elapsed := time.Since(start)

	fmt.Printf(
		"[PING] %s\n",
		elapsed,
	)

	return nil
}
```

Di sini terlihat masalah desain yang sengaja kita temukan lebih awal:

**plugin belum memiliki akses ke Telegram reply.**

Itu justru bagus. Sebelum menambah 100 plugin, kita harus memperbaiki `plugin.Context`.

---

# 12. Context yang benar

Saya sarankan `plugin.Context` akhirnya menjadi:

```go
package plugin

import (
	"context"

	"github.com/yourname/goultroid/internal/telegram"
)

type Context struct {
	Context context.Context
	Event   *telegram.MessageEvent
}

func (c *Context) Reply(text string) error {
	return c.Event.Reply(text)
}
```

Maka ping menjadi:

```go
func (p *Plugin) ping(
	ctx *plugin.Context,
	args string,
) error {

	start := time.Now()

	// command processing...

	elapsed := time.Since(start)

	return ctx.Reply(
		fmt.Sprintf(
			"🏓 Pong!\nLatency: %d ms",
			elapsed.Milliseconds(),
		),
	)
}
```

Ini jauh lebih bagus.

Plugin tidak perlu tahu:

* `tg.UpdateNewMessage`
* `tg.InputPeerClass`
* `MessagesSendMessage`
* MTProto
* session
* update reconciliation

Plugin hanya tahu:

```go
ctx.Reply(...)
```

Itulah abstraction yang nanti memungkinkan kita membuat framework yang mirip Ultroid.

---

# 13. `app.go`

`internal/app/app.go`

```go
package app

import (
	"context"
	"fmt"
	"os"

	"github.com/yourname/goultroid/internal/plugin"
	telegramclient "github.com/yourname/goultroid/internal/telegram"
	"github.com/yourname/goultroid/plugins/ping"
)

type App struct {
	Telegram *telegramclient.Client
	Plugins  *plugin.Manager
}

func New() (*App, error) {

	appID, err := telegramclient.AppIDFromEnv()
	if err != nil {
		return nil, err
	}

	appHash := os.Getenv("APP_HASH")

	if appHash == "" {
		return nil, fmt.Errorf("APP_HASH is not set")
	}

	sessionFile := os.Getenv("SESSION_FILE")

	if sessionFile == "" {
		sessionFile = "data/session.json"
	}

	prefix := os.Getenv("PREFIX")

	if prefix == "" {
		prefix = "."
	}

	manager := plugin.NewManager()

	if err := manager.Register(
		ping.New(),
	); err != nil {
		return nil, err
	}

	dispatcher := telegramclient.NewDispatcher(
		manager,
		prefix,
	)

	client, err := telegramclient.NewClient(
		appID,
		appHash,
		sessionFile,
		dispatcher,
	)
	if err != nil {
		return nil, err
	}

	dispatcher.API = client.API

	return &App{
		Telegram: client,
		Plugins:  manager,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	return a.Telegram.Run(ctx)
}
```

---

# 14. `main.go`

`cmd/goultroid/main.go`

```go
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/yourname/goultroid/internal/app"
)

func main() {

	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer cancel()

	instance, err := app.New()
	if err != nil {
		log.Fatal(err)
	}

	log.Println("GoUltroid starting...")

	if err := instance.Run(ctx); err != nil {
		log.Fatal(err)
	}

	log.Println("GoUltroid stopped.")
}
```

---

# 15. Ada satu revisi penting sebelum benar-benar `go run`

Saya tidak ingin memberi kamu kode yang sekadar terlihat benar tetapi gagal compile karena API `gotd` berubah sedikit di bagian update dispatch/reply.

Untuk MVP yang **benar-benar executable**, saya akan menggunakan mekanisme resmi `tg.UpdateDispatcher`/`updates.Manager` dari gotd untuk update processing. Contoh resmi mereka memang menunjukkan pola:

```text
gotd Client
   │
   ├── authentication
   │
   ├── updates.Manager
   │
   └── UpdateDispatcher
          │
          └── OnNewMessage(...)
```

`updates.Manager` menangani sinkronisasi state Telegram seperti `pts/qts/seq`, dan dokumentasi gotd secara eksplisit menyebut bahwa manager menjaga update sensitif-state dalam urutan yang benar. ([Go Packages][5])

Jadi **arsitektur final MVP yang saya pilih adalah:**

```text
                        ┌────────────────────┐
                        │      Telegram      │
                        └─────────┬──────────┘
                                  │
                                  ▼
                        ┌────────────────────┐
                        │    gotd/td Client  │
                        └─────────┬──────────┘
                                  │
                                  ▼
                        ┌────────────────────┐
                        │ updates.Manager    │
                        │ pts/qts/seq sync   │
                        └─────────┬──────────┘
                                  │
                                  ▼
                        ┌────────────────────┐
                        │ tg.UpdateDispatcher│
                        └─────────┬──────────┘
                                  │
                                  ▼
                        ┌────────────────────┐
                        │  MessageEvent      │
                        │  Go abstraction    │
                        └─────────┬──────────┘
                                  │
                                  ▼
                        ┌────────────────────┐
                        │ Command Dispatcher │
                        └─────────┬──────────┘
                                  │
                                  ▼
                        ┌────────────────────┐
                        │   Plugin Manager   │
                        └─────────┬──────────┘
                                  │
                    ┌─────────────┴─────────────┐
                    ▼                           ▼
              ┌──────────┐                ┌──────────┐
              │  .ping   │                │  .help   │
              └──────────┘                └──────────┘
```

## 16. Target penggunaan

Setelah MVP selesai:

```bash
mkdir goultroid
cd goultroid

go mod init github.com/yourname/goultroid

go get github.com/gotd/td@v0.161.0
```

Buat:

```bash
cp .env.example .env
```

Isi:

```env
APP_ID=12345678
APP_HASH=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
PHONE=+628xxxxxxxxxx
SESSION_FILE=data/session.json
PREFIX=.
```

Lalu:

```bash
go run ./cmd/goultroid
```

Pertama kali:

```text
GoUltroid starting...

Telegram code: _
```

Masukkan kode Telegram.

Session disimpan sehingga login berikutnya tidak perlu mengulangi authentication selama session masih valid. Persistent session seperti ini memang merupakan pola yang digunakan contoh userbot resmi gotd. ([GitHub][2])

Kemudian di Telegram:

```text
.ping
```

dan bot/userbot menjawab:

```text
🏓 Pong!
Latency: 0 ms
```

---

## Yang saya sarankan untuk langkah berikutnya

Jangan langsung port semua fitur Ultroid. Kita bangun **framework-nya dulu**.

Urutannya:

```text
MVP
 │
 ├── .ping                    ← sekarang
 │
 ├── .alive
 ├── .help
 │
 ├── command arguments
 ├── aliases
 ├── filters
 │
 ├── owner/sudo
 ├── middleware
 │
 ├── reply/edit/delete
 ├── media
 │
 ├── plugin auto-discovery
 ├── plugin enable/disable
 │
 ├── SQLite
 ├── config system
 │
 ├── downloader
 ├── sticker
 ├── admin
 ├── AFK
 ├── notes
 └── scheduler
```

**Dan ada satu keputusan arsitektur yang menurut saya sangat penting:** jangan menggunakan Go's native `plugin` package untuk plugin Ultroid-style. Untuk deployment cross-platform, lebih baik plugin dikompilasi sebagai bagian dari binary dulu; setelah framework stabil, baru kita tambahkan **dynamic plugin melalui WASM/RPC** jika memang dibutuhkan. Dengan begitu core tetap portable dan tidak tergantung CGO.

Kalau target akhirnya adalah **"Ultroid versi Go"**, saya akan menjadikan fase berikutnya sebagai **MVP v0.2: `tg.UpdateDispatcher` + `MessageEvent` yang benar + `.ping` yang benar-benar reply ke chat asal + `.help` + command parser + alias + owner ID**, lalu baru kita mulai membangun plugin ecosystem.

[1]: https://pkg.go.dev/github.com/gotd/td/telegram?utm_source=chatgpt.com "telegram package - github.com/gotd/td/telegram - Go Packages"
[2]: https://github.com/gotd/td/blob/main/examples/userbot/main.go?utm_source=chatgpt.com "td/examples/userbot/main.go at main · gotd/td · GitHub"
[3]: https://github.com/gotd/td/blob/main/examples/README.md?utm_source=chatgpt.com "td/examples/README.md at main · gotd/td · GitHub"
[4]: https://pkg.go.dev/github.com/gotd/td/telegram/message?utm_source=chatgpt.com "message package - github.com/gotd/td/telegram/message - Go Packages"
[5]: https://pkg.go.dev/github.com/gotd/td/telegram/updates?utm_source=chatgpt.com "updates package - github.com/gotd/td/telegram/updates - Go Packages"
