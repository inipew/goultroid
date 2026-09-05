Siap. Kita lanjutkan dari MVP tadi, tetapi kali ini saya sarankan kita **rapikan fondasinya dulu** supaya tidak terjebak desain yang harus dibongkar ketika plugin mulai banyak.

Target **v0.2**:

```text
Go UserBot
│
├── Telegram Client
│   ├── MTProto
│   ├── Authentication
│   └── Persistent Session
│
├── Update Dispatcher
│   └── New Message
│
├── Message Context
│   ├── Chat
│   ├── Sender
│   ├── Message
│   ├── Reply()
│   ├── Edit()
│   └── Delete()
│
├── Command Router
│   ├── Prefix
│   ├── Arguments
│   ├── Aliases
│   └── Permissions
│
├── Permission
│   ├── Owner
│   └── Sudo
│
└── Plugins
    ├── ping
    └── help
```

Saya juga akan mengubah sedikit struktur project agar lebih scalable.

---

# 1. Struktur baru

```text
goultroid/
│
├── cmd/
│   └── goultroid/
│       └── main.go
│
├── internal/
│   │
│   ├── app/
│   │   └── app.go
│   │
│   ├── config/
│   │   └── config.go
│   │
│   ├── core/
│   │   ├── context.go
│   │   ├── command.go
│   │   ├── router.go
│   │   └── permissions.go
│   │
│   ├── plugin/
│   │   ├── plugin.go
│   │   └── manager.go
│   │
│   └── telegram/
│       ├── client.go
│       ├── dispatcher.go
│       └── message.go
│
├── plugins/
│   ├── ping/
│   │   └── ping.go
│   │
│   └── help/
│       └── help.go
│
├── data/
│   └── .gitkeep
│
├── .env
├── .env.example
├── .gitignore
├── go.mod
└── README.md
```

Ada pemisahan penting:

```text
telegram/
    "Bagaimana bicara dengan Telegram?"

core/
    "Bagaimana aplikasi kita bekerja?"

plugin/
    "Bagaimana plugin didaftarkan?"

plugins/
    "Fitur userbot."
```

Jadi plugin tidak menjadi tergantung langsung pada seluruh implementasi Telegram.

---

# 2. Config layer

Daripada `os.Getenv()` tersebar di seluruh aplikasi, kita buat satu configuration object.

`internal/config/config.go`

```go
package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	AppID   int
	AppHash string

	Phone string

	SessionFile string
	Prefix      string

	OwnerID int64
}

func Load() (*Config, error) {
	appID, err := strconv.Atoi(os.Getenv("APP_ID"))
	if err != nil {
		return nil, fmt.Errorf("invalid APP_ID: %w", err)
	}

	appHash := os.Getenv("APP_HASH")
	if appHash == "" {
		return nil, fmt.Errorf("APP_HASH is required")
	}

	phone := os.Getenv("PHONE")
	if phone == "" {
		return nil, fmt.Errorf("PHONE is required")
	}

	sessionFile := os.Getenv("SESSION_FILE")
	if sessionFile == "" {
		sessionFile = "data/session.json"
	}

	prefix := os.Getenv("PREFIX")
	if prefix == "" {
		prefix = "."
	}

	var ownerID int64

	if value := os.Getenv("OWNER_ID"); value != "" {
		ownerID, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid OWNER_ID: %w", err)
		}
	}

	return &Config{
		AppID:        appID,
		AppHash:      appHash,
		Phone:        phone,
		SessionFile: sessionFile,
		Prefix:       prefix,
		OwnerID:      ownerID,
	}, nil
}
```

`.env.example`:

```env
APP_ID=12345678
APP_HASH=
PHONE=+628xxxxxxxxxx

SESSION_FILE=data/session.json

PREFIX=.

OWNER_ID=123456789
```

Nanti bisa kita tambahkan:

```env
LOG_LEVEL=info
DATABASE_URL=
REDIS_URL=

SUDO_USERS=
BOT_TOKEN=

DOWNLOAD_DIR=data/downloads
```

---

# 3. Command abstraction

Sekarang kita buat command sebagai object yang jelas.

`internal/core/command.go`

```go
package core

import "context"

type CommandHandler func(*Context) error

type Command struct {
	Name        string
	Aliases     []string
	Description string
	Usage       string

	OwnerOnly bool

	Handler CommandHandler
}
```

Contoh:

```text
.ping

.ping hello

.alive

.help ping
```

Semua akan masuk ke object `Command`.

---

# 4. Context

Ini salah satu komponen paling penting dari framework.

`internal/core/context.go`

```go
package core

import (
	"context"

	"github.com/gotd/td/tg"
)

type Context struct {
	Context context.Context

	API *tg.Client

	Message *tg.Message

	ChatID int64
	UserID int64

	Command string
	Args    []string
	RawArgs string
}

func (c *Context) Text() string {
	if c.Message == nil {
		return ""
	}

	return c.Message.Message
}
```

Nanti kita tambahkan:

```go
func (c *Context) Reply(text string) error
func (c *Context) Edit(text string) error
func (c *Context) Delete() error
func (c *Context) React(...) error
func (c *Context) Download(...) error
```

Jadi plugin nantinya bisa terlihat seperti:

```go
func ping(ctx *core.Context) error {
	return ctx.Reply("🏓 Pong!")
}
```

Ini jauh lebih bersih daripada:

```go
tg.MessagesSendMessage(...)
```

di setiap plugin.

---

# 5. Permission system

Sekarang kita buat owner.

`internal/core/permissions.go`

```go
package core

type Permissions struct {
	OwnerID int64
}

func (p *Permissions) IsOwner(userID int64) bool {
	return p.OwnerID != 0 && userID == p.OwnerID
}
```

Kemudian Context:

```go
type Context struct {
	Context context.Context

	API     *tg.Client
	Message *tg.Message

	ChatID int64
	UserID int64

	Command string
	Args    []string
	RawArgs string

	Permissions *Permissions
}
```

Plugin:

```go
Command{
	Name:      "restart",
	OwnerOnly: true,
	Handler:   restart,
}
```

Router yang memutuskan apakah user boleh menjalankan command.

**Bukan plugin-nya.**

Ini penting untuk keamanan.

---

# 6. Command Router

`internal/core/router.go`

```go
package core

import (
	"fmt"
	"strings"
)

type Router struct {
	Prefix string

	Commands map[string]Command
}

func NewRouter(prefix string) *Router {
	return &Router{
		Prefix:   prefix,
		Commands: make(map[string]Command),
	}
}

func (r *Router) Register(cmd Command) error {
	name := strings.ToLower(cmd.Name)

	if _, exists := r.Commands[name]; exists {
		return fmt.Errorf(
			"command already registered: %s",
			name,
		)
	}

	r.Commands[name] = cmd

	for _, alias := range cmd.Aliases {
		alias = strings.ToLower(alias)

		if _, exists := r.Commands[alias]; exists {
			return fmt.Errorf(
				"command alias already registered: %s",
				alias,
			)
		}

		r.Commands[alias] = cmd
	}

	return nil
}

func (r *Router) Parse(
	text string,
) (string, []string, string, bool) {

	if !strings.HasPrefix(text, r.Prefix) {
		return "", nil, "", false
	}

	text = strings.TrimSpace(
		strings.TrimPrefix(text, r.Prefix),
	)

	parts := strings.Fields(text)

	if len(parts) == 0 {
		return "", nil, "", false
	}

	command := strings.ToLower(parts[0])

	args := parts[1:]

	rawArgs := ""

	if len(args) > 0 {
		rawArgs = strings.Join(args, " ")
	}

	return command, args, rawArgs, true
}
```

Sekarang:

```text
.ping
```

menjadi:

```go
command = "ping"
args    = []
rawArgs = ""
```

Sedangkan:

```text
.ping hello world
```

menjadi:

```go
command = "ping"
args    = ["hello", "world"]
rawArgs = "hello world"
```

---

# 7. Plugin interface

`internal/plugin/plugin.go`

```go
package plugin

import "github.com/yourname/goultroid/internal/core"

type Plugin interface {
	Name() string

	Commands() []core.Command

	Init() error
}
```

Manager:

```go
package plugin

import (
	"fmt"

	"github.com/yourname/goultroid/internal/core"
)

type Manager struct {
	plugins []Plugin
	router  *core.Router
}

func NewManager(router *core.Router) *Manager {
	return &Manager{
		router: router,
	}
}

func (m *Manager) Register(p Plugin) error {
	if err := p.Init(); err != nil {
		return fmt.Errorf(
			"initialize plugin %s: %w",
			p.Name(),
			err,
		)
	}

	for _, cmd := range p.Commands() {
		if err := m.router.Register(cmd); err != nil {
			return err
		}
	}

	m.plugins = append(m.plugins, p)

	return nil
}

func (m *Manager) Plugins() []Plugin {
	return m.plugins
}
```

Sekarang hubungan:

```text
Plugin
   │
   ▼
Plugin Manager
   │
   ▼
Command Router
```

---

# 8. Ping plugin

`plugins/ping/ping.go`

```go
package ping

import (
	"fmt"
	"time"

	"github.com/yourname/goultroid/internal/core"
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

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "ping",
			Aliases:     []string{"p"},
			Description: "Check userbot latency",
			Usage:       ".ping",
			Handler:     p.handlePing,
		},
	}
}

func (p *Plugin) handlePing(ctx *core.Context) error {
	start := time.Now()

	if err := ctx.Reply("🏓 Pong!"); err != nil {
		return err
	}

	latency := time.Since(start)

	return ctx.Edit(
		fmt.Sprintf(
			"🏓 Pong!\nLatency: %d ms",
			latency.Milliseconds(),
		),
	)
}
```

Ini memberikan behavior yang lebih menarik:

```text
User:
.ping

Userbot:
🏓 Pong!

          ↓

🏓 Pong!
Latency: 42 ms
```

Tapi kita perlu mengimplementasikan `Reply()` dan `Edit()` dengan benar.

---

# 9. Message abstraction

Saya akan membuat:

`internal/telegram/message.go`

```go
package telegram

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/telegram/message"
)

type MessageService struct {
	API *tg.Client
}

func NewMessageService(api *tg.Client) *MessageService {
	return &MessageService{
		API: api,
	}
}

func (s *MessageService) Reply(
	ctx context.Context,
	msg *tg.Message,
	text string,
) (*tg.Message, error) {

	peer := msg.GetPeerID()

	if peer == nil {
		return nil, fmt.Errorf("message has no peer")
	}

	sender := message.NewSender(s.API)

	return sender.
		Peer(peer).
		Text(ctx, text)
}
```

Konsepnya:

```text
core.Context
     │
     ▼
MessageService
     │
     ▼
gotd
     │
     ▼
Telegram
```

Dengan begini `core.Context` tidak perlu mengetahui detail MTProto.

---

# 10. Update Dispatcher

Untuk layer Telegram, kita manfaatkan update handling dari gotd.

Konsepnya:

```go
type Dispatcher struct {
	Router *core.Router
}
```

Kemudian:

```text
Telegram Update
       │
       ▼
UpdateNewMessage
       │
       ▼
*core.Context
       │
       ▼
Router.Parse()
       │
       ▼
Router.Commands[]
       │
       ▼
Permission Check
       │
       ▼
Handler()
```

Dan bukan plugin yang menerima `tg.UpdateNewMessage` langsung.

Itu akan menjadi salah satu prinsip utama framework kita:

> **Telegram-specific code hanya boleh berada di layer `internal/telegram`.**

---

# 11. Help plugin

Setelah `.ping`, plugin pertama yang sangat berguna adalah `.help`.

`plugins/help/help.go`

```go
package help

import (
	"fmt"
	"strings"

	"github.com/yourname/goultroid/internal/core"
	"github.com/yourname/goultroid/internal/plugin"
)

type Plugin struct {
	Manager *plugin.Manager
}

func New(manager *plugin.Manager) *Plugin {
	return &Plugin{
		Manager: manager,
	}
}

func (p *Plugin) Name() string {
	return "help"
}

func (p *Plugin) Init() error {
	return nil
}

func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "help",
			Aliases:     []string{"h"},
			Description: "Show available commands",
			Usage:       ".help [command]",
			Handler:     p.handleHelp,
		},
	}
}

func (p *Plugin) handleHelp(ctx *core.Context) error {
	var builder strings.Builder

	builder.WriteString("📚 Available commands\n\n")

	for _, plugin := range p.Manager.Plugins() {
		builder.WriteString(
			fmt.Sprintf("• %s\n", plugin.Name()),
		)
	}

	return ctx.Reply(builder.String())
}
```

Nanti output:

```text
📚 Available commands

• ping
• help
```

Kemudian kita bisa upgrade menjadi:

```text
📚 GoUltroid Help

[Admin]
.ban
.unban
.mute

[Utility]
.ping
.alive
.help

[Media]
.sticker
.download
```

Jadi Command nantinya punya:

```go
Category string
```

---

# 12. Dependency antar-plugin

Ada hal menarik di sini.

`help` membutuhkan:

```go
Manager
```

Sedangkan `ping` tidak.

Kita bisa membuat:

```text
Plugin Manager
      │
      ├── ping
      ├── help
      ├── admin
      ├── media
      └── sticker
```

Dan plugin bisa mendapatkan service melalui `PluginContext`.

Contohnya kelak:

```go
type Services struct {
	Telegram  *TelegramService
	Database  *Database
	Downloads *DownloadService
	Config    *config.Config
}
```

Kemudian:

```go
type PluginContext struct {
	Services *Services
}
```

Ini jauh lebih scalable dibanding setiap plugin membuat client/service sendiri.

---

# 13. Arsitektur akhirnya

Saya akan mengarahkan project ini ke struktur seperti:

```text
                     ┌──────────────┐
                     │   Telegram   │
                     └──────┬───────┘
                            │
                         MTProto
                            │
                     ┌──────▼───────┐
                     │ gotd/td      │
                     └──────┬───────┘
                            │
                     ┌──────▼───────┐
                     │ Update Layer │
                     └──────┬───────┘
                            │
                     ┌──────▼───────┐
                     │ MessageEvent │
                     └──────┬───────┘
                            │
                     ┌──────▼───────┐
                     │ Command      │
                     │ Router       │
                     └──────┬───────┘
                            │
                 ┌──────────▼──────────┐
                 │ Permission/Middleware│
                 └──────────┬──────────┘
                            │
                     ┌──────▼───────┐
                     │ Plugin       │
                     │ Manager      │
                     └──────┬───────┘
                            │
          ┌─────────────────┼─────────────────┐
          ▼                 ▼                 ▼
       ┌──────┐          ┌──────┐          ┌──────┐
       │ ping │          │ help │          │ admin│
       └──────┘          └──────┘          └──────┘
```

## 14. Yang saya ubah dari desain awal

Ada beberapa keputusan yang menurut saya **wajib dilakukan sekarang**, sebelum project membesar:

| Komponen   | MVP awal           | v0.2               |
| ---------- | ------------------ | ------------------ |
| Telegram   | langsung ke plugin | abstraction        |
| Event      | raw update         | MessageEvent       |
| Command    | sederhana          | Router             |
| Alias      | belum              | ada                |
| Args       | sederhana          | `Args` + `RawArgs` |
| Permission | belum              | owner              |
| Plugin     | interface          | manager            |
| Config     | `os.Getenv`        | Config object      |
| Reply      | raw API            | Context            |
| Help       | belum              | plugin             |
| Database   | belum              | sengaja ditunda    |

Dengan ini kita sudah memiliki **framework**, bukan sekadar script Telegram.

---

## 15. Roadmap setelah v0.2

Setelah `.ping` dan `.help` stabil, saya tidak akan langsung membuat downloader atau sticker. Urutannya lebih baik:

### v0.3 — Middleware

```text
Message
   ↓
Prefix
   ↓
Command
   ↓
Middleware
   ├── IsOwner
   ├── IsSudo
   ├── Cooldown
   ├── Blacklist
   └── Logging
   ↓
Handler
```

### v0.4 — Message API

```go
ctx.Reply()
ctx.Edit()
ctx.Delete()
ctx.React()
ctx.Pin()
ctx.Forward()
ctx.GetReply()
ctx.GetMedia()
```

### v0.5 — Database

SQLite dulu:

```text
data/
└── goultroid.db
```

Schema awal:

```text
settings
sudo_users
disabled_plugins
notes
filters
afk
```

### v0.6 — Plugin discovery

Daripada:

```go
manager.Register(ping.New())
manager.Register(help.New())
manager.Register(admin.New())
```

kita bisa punya:

```text
plugins/
├── ping/
├── help/
├── admin/
└── media/
```

dan loader otomatis.

### v0.7 — Ultroid-style features

Baru masuk:

* AFK
* filters
* notes
* blacklist
* admin tools
* sticker
* downloader
* media tools
* scheduler
* inline buttons
* callback queries
* album/media handling

### v1.0

```text
GoUltroid
├── Multi-account
├── SQLite/PostgreSQL
├── Redis optional
├── Plugin ecosystem
├── Docker
├── Web dashboard
├── Hot reload
└── API
```

**Langkah teknis berikutnya yang paling tepat adalah membangun `v0.2` sampai benar-benar compile/run:** `gotd/td` `UpdateDispatcher` → `MessageEvent` → `core.Router` → owner middleware → `.ping` reply/edit → `.help`. Setelah itu baru kita punya fondasi yang layak untuk mulai mem-port **behavior** fitur-fitur Ultroid satu per satu, tanpa menyalin implementasi Python-nya.
