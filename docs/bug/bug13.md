Saya cek ulang **latest `main` GoUltroid**, dengan fokus bukan lagi pada fitur, tetapi pada **arsitektur, boundary antar-package, ownership state, dependency flow, duplication, coupling, lifecycle, dan kualitas penulisan kode**. Struktur sekarang sudah jauh lebih matang—sudah ada `settings`, `callback`, `ui`, `scheduler`, `services`, `plugin`, dll.—tetapi justru mulai terlihat masalah baru: **abstraksi sudah banyak, namun belum cukup terintegrasi**. Akibatnya sebagian abstraksi menjadi wrapper/helper yang berdiri sendiri dan business logic masih mengulang pola yang sama.

Secara keseluruhan saya nilai:

| Area                           |   Kondisi |
| ------------------------------ | --------: |
| Package separation             |   🟢 8/10 |
| Domain/service separation      | 🟢 7.5/10 |
| Dependency direction           | 🟡 6.5/10 |
| UI abstraction                 |   🟡 6/10 |
| Callback architecture          | 🟡 6.5/10 |
| Settings architecture          |   🟡 6/10 |
| State management               | 🟡 5.5/10 |
| Application composition        |   🔴 5/10 |
| Duplication                    |   🟡 6/10 |
| Testability                    | 🟡 6.5/10 |
| Lifecycle robustness           |   🟡 6/10 |
| Maintainability jangka panjang |   🟡 6/10 |

Masalah utamanya bukan "kode jelek". Justru **kode sudah cukup bagus secara lokal**, tetapi **belum cukup konsisten secara sistemik**.

---

# 1. Masalah terbesar: `internal/app/app.go` sudah menjadi God Object / Composition God File

`internal/app/app.go` sekarang sekitar 11 KB dan melakukan terlalu banyak hal:

* logger
* DB
* permissions
* router
* dispatcher
* Telegram client
* metrics
* localization
* callback store/router
* inline engine
* moderation
* scheduler
* storage
* downloader
* media
* PMPermit
* broadcast
* userlog
* addons
* assistant
* plugin registration
* lifecycle

Hal tersebut terlihat jelas dari constructor `New()` yang menginisialisasi hampir seluruh sistem sekaligus.

Ini adalah **red flag arsitektur utama**.

## Problem

Sekarang:

```text
App.New()
 ├── Database
 ├── Permissions
 ├── Router
 ├── Dispatcher
 ├── Telegram
 ├── Metrics
 ├── Callback
 ├── Inline
 ├── Moderation
 ├── Scheduler
 ├── Storage
 ├── Downloader
 ├── Media
 ├── PMPermit
 ├── Broadcast
 ├── UserLog
 ├── Addons
 ├── Assistant
 └── 20+ Plugins
```

Akibatnya setiap penambahan fitur baru harus menyentuh `app.go`.

Lama-lama:

```text
app.go
10000 lines
```

bukan mustahil.

## Solusi

Pisahkan menjadi **composition modules**:

```text
internal/app/
├── app.go
├── bootstrap.go
├── dependencies.go
├── telegram.go
├── services.go
├── plugins.go
├── lifecycle.go
└── shutdown.go
```

Contoh:

```go
type Dependencies struct {
    DB          *database.DB
    EventBus    *core.EventBus
    Permissions *core.Permissions
    Settings    *settings.Service
    Dispatcher  *telegram.Dispatcher
    Callbacks   *callback.Router
    Inline      *inline.Engine
}
```

Kemudian:

```go
func buildCore(...) (*Dependencies, error)
func buildTelegram(...) error
func buildServices(...) error
func buildPlugins(...) error
func wireEvents(...) error
```

Lebih bagus lagi:

```text
App
 │
 ├── Core
 │   ├── DB
 │   ├── EventBus
 │   ├── Permissions
 │   └── Config
 │
 ├── TelegramRuntime
 │   ├── Client
 │   ├── Dispatcher
 │   ├── Callback
 │   └── Inline
 │
 ├── DomainServices
 │   ├── Settings
 │   ├── Moderation
 │   ├── Scheduler
 │   └── Media
 │
 └── PluginRuntime
     ├── PluginManager
     └── Plugins
```

---

# 2. `SetX()` terlalu banyak pada Dispatcher

`Dispatcher` sekarang mempunyai banyak mutable dependency:

```go
SetExecutor()
SetRootContext()
SetService()
SetResolver()
SetSelfID()
SetEventBus()
SetLocalizer()
SetCallbackRouter()
SetInlineEngine()
```

dan masing-masing diikuti getter/private getter.

Ini biasanya tanda:

> object dibuat terlalu cepat sebelum semua dependency tersedia.

Akibatnya object bisa berada dalam kondisi:

```text
Dispatcher {
    service = nil
    resolver = nil
    eventBus = nil
    callbackRouter = nil
}
```

lalu sebagian API harus defensive terhadap `nil`.

## Lebih baik

Dependency wajib masuk constructor:

```go
type DispatcherDeps struct {
    Router    *core.Router
    Permissions *core.Permissions
    Service   core.TelegramServicer
    Resolver  core.PeerResolver
    EventBus  *core.EventBus
    Localizer core.Localizer
    Callback  *callback.Router
    Inline    *inline.Engine
}
```

```go
func NewDispatcher(deps DispatcherDeps) (*Dispatcher, error)
```

Setters hanya untuk dependency yang memang **runtime mutable**, bukan wiring.

### Prinsip

```text
Required dependency
        ↓
constructor

Runtime state
        ↓
methods

Optional capability
        ↓
explicit interface / nil capability
```

Bukan:

```text
constructor
   ↓
SetA
SetB
SetC
SetD
SetE
SetF
```

---

# 3. Callback Router terlalu gemuk dan terlalu banyak duplication

Ini salah satu bagian yang paling perlu dirapikan.

`callback.Router.Dispatch()` sekarang menangani:

* parsing
* noop
* rate limit
* state lookup
* expiry
* consumed
* user authorization
* namespace validation
* chat validation
* message validation
* single-use
* handler lookup
* callback context
* auto-answer
* timeout
* panic recovery
* metrics
* fallback response

Semua dalam satu method.

Secara fungsi memang robust, tetapi secara struktur **terlalu banyak responsibility**.

Lebih parah lagi, pola berikut diulang berkali-kali:

```go
if r.metrics != nil {
    ...
}

if svc != nil {
    _ = svc.AnswerCallbackQuery(...)
}

return ...
```

Ada banyak variasi:

```text
expired
consumed
unauthorized
namespace mismatch
chat mismatch
message mismatch
rate limited
handler missing
handler failed
```

## Buat abstraction:

```go
type CallbackFailure struct {
    Code    FailureCode
    Message string
    Alert   bool
    Err     error
}
```

Kemudian:

```go
func (r *Router) reject(
    ctx context.Context,
    evt *core.CallbackQueryEvent,
    failure CallbackFailure,
) error
```

Sehingga:

```go
return r.reject(ctx, evt, CallbackFailure{
    Code:    FailureUnauthorized,
    Message: "⚠️ You are not authorized.",
    Alert:   true,
    Err:     ErrUnauthorized,
})
```

Ini akan menghilangkan banyak repetitive code.

---

# 4. Callback pipeline seharusnya menjadi middleware chain

Daripada satu `Dispatch()` besar:

```text
Dispatch
 ├── parse
 ├── rate limit
 ├── state
 ├── authorization
 ├── scope
 ├── consume
 ├── lookup
 ├── timeout
 ├── recover
 └── response
```

buat:

```text
Callback Pipeline
        │
        ▼
Decode
        │
        ▼
RateLimit
        │
        ▼
ResolveState
        │
        ▼
Authorize
        │
        ▼
ValidateScope
        │
        ▼
Consume
        │
        ▼
ResolveHandler
        │
        ▼
Execute
        │
        ▼
Recover
        │
        ▼
Respond
```

Secara konsep:

```go
type CallbackMiddleware func(
    *CallbackContext,
    CallbackHandler,
) error
```

Lalu:

```go
router.Use(
    callback.Recover(),
    callback.Timeout(...),
    callback.RateLimit(...),
    callback.State(),
    callback.Authorization(),
)
```

Ini akan sangat mengurangi coupling.

---

# 5. `StateStore` mencampur terlalu banyak konsep

`StateStore` sekarang menangani:

* storage
* TTL
* expiration
* authorization scope
* single-use
* consumed state
* memory limit
* eviction
* background pruning
* lifecycle

Itu terlalu banyak.

Saya akan pisahkan:

```text
interaction/
├── state/
│   ├── store.go
│   ├── memory.go
│   └── ttl.go
│
├── authorization/
│   └── scope.go
│
└── lifecycle/
    └── cleanup.go
```

Karena:

```text
State storage
≠
State authorization
≠
State lifecycle
```

---

# 6. Ada lifecycle issue nyata: `context.Background()`

Di `app.go` ada:

```go
callbackStore.Start(context.Background())
```

dan:

```go
inlineEngine.Cache().Start(context.Background())
```

Ini tidak ideal.

`App.Run(ctx)` sebenarnya mempunyai lifecycle context yang benar, tetapi beberapa subsystem dimulai menggunakan `context.Background()`.

Memang kemudian `Stop()` dipanggil melalui `defer`, tetapi secara arsitektur:

```text
App lifecycle
      │
      ├── callback lifecycle
      ├── inline cache lifecycle
      └── scheduler lifecycle
```

seharusnya semuanya mengikuti **satu root context**.

Target:

```go
func (a *App) Run(ctx context.Context) error {
    ...
    callbackStore.Start(ctx)
    inlineCache.Start(ctx)
    dispatcher.Start(ctx)
    scheduler.Start(ctx)
}
```

Lebih ideal lagi:

```text
App.Start(ctx)
      │
      ├── Runtime.Start(ctx)
      ├── Telegram.Start(ctx)
      ├── Scheduler.Start(ctx)
      └── Interaction.Start(ctx)
```

---

# 7. `StateStore.Stop()` masih terlalu rumit untuk sesuatu yang seharusnya sederhana

Ada kombinasi:

```go
stopCh
stopOnce
mutex
context
reset sync.Once
```

Ini adalah indikasi lifecycle state machine yang terlalu manual.

Lebih bersih:

```go
type StateStore struct {
    ...
    cancel context.CancelFunc
}
```

Start:

```go
ctx, cancel := context.WithCancel(ctx)
s.cancel = cancel
go s.run(ctx)
```

Stop:

```go
func (s *StateStore) Stop() {
    if s.cancel != nil {
        s.cancel()
    }
}
```

Kalau perlu idempotency, gunakan `sync.Once` untuk shutdown, bukan reset/reuse lifecycle object.

---

# 8. Settings architecture sekarang sudah bagus secara konsep, tetapi implementasinya terlalu string-oriented

`settings.Service` melakukan:

```go
Resolve(...)
ResolveBool(...)
ResolveInt(...)
ResolveDuration(...)
ResolveString(...)
```

Ini menimbulkan masalah:

```text
string
 ↓
parse bool
 ↓
parse int
 ↓
parse duration
```

padahal `SettingDefinition` sudah mengetahui type.

Artinya logic type parsing sekarang tersebar:

```text
Definition.Validate()
Definition.Canonicalize()
Service.ResolveBool()
Service.ResolveInt()
Service.ResolveDuration()
Plugin Settings parsing
```

Ini duplicate responsibility.

---

# 9. Jadikan `SettingValue` typed abstraction

Daripada:

```go
string
```

gunakan:

```go
type Value struct {
    Raw string
    Type SettingType
}
```

atau lebih baik API typed:

```go
type Value struct {
    Bool     *bool
    Int      *int64
    String   *string
    Duration *time.Duration
    Enum     *string
}
```

Tetapi saya lebih suka:

```go
type SettingValue struct {
    Definition SettingDefinition
    Raw        string
}
```

dan:

```go
func (v SettingValue) Bool() (bool, error)
func (v SettingValue) Int() (int64, error)
func (v SettingValue) Duration() (time.Duration, error)
func (v SettingValue) String() string
```

Dengan demikian parser hanya ada **satu tempat**.

---

# 10. `SettingDefinition` seharusnya menjadi source of truth penuh

Saat ini definition:

```go
Type
DefaultValue
AllowedValues
MinVal
MaxVal
Title
Description
Category
Validator
```

Sudah bagus, tetapi masih kurang untuk architecture yang sedang dibangun.

Tambahkan:

```go
type SettingDefinition struct {
    Namespace string
    Key       string

    Type      SettingType
    Default   Value

    Scope     ScopePolicy

    Title       string
    Description string
    Category    string

    Validation Validator

    UI          UIHint

    Mutability  Mutability
    Sensitive   bool
    RestartRequired bool

    Order int
}
```

`UIHint` misalnya:

```go
type UIHint struct {
    Widget       WidgetType
    Step         int64
    Presets      []string
    Confirm      bool
    Searchable   bool
}
```

Dengan begitu plugin settings tidak perlu hardcode:

```go
switch def.Type {
case TypeBool:
...
case TypeInt:
...
case TypeDuration:
...
}
```

UI engine bisa otomatis mengetahui cara merender.

---

# 11. Settings plugin sekarang mengulang business logic UI terlalu banyak

Ini salah satu contoh paling jelas.

Di `plugins/settings/settings.go`, ada pola:

```go
Resolve()
SplitN()
Set()
renderScreen()
Edit()
```

yang berulang untuk:

* toggle
* step
* duration
* select

Misalnya:

```go
case "step":
   ...
   p.service.Set(...)
   ...
   renderScreen(...)
```

lalu:

```go
case "dur":
   ...
   p.service.Set(...)
   ...
   renderScreen(...)
```

lalu:

```go
case "select":
   ...
   p.service.Set(...)
   ...
   renderScreen(...)
```

Ini harus disatukan.

Target:

```go
func (p *Plugin) applySettingAction(
    ctx context.Context,
    state SettingInteractionState,
    value string,
) error
```

Kemudian:

```go
case "toggle":
    return p.applySettingAction(ctx, state, toggleValue(...))

case "step":
    return p.applySettingAction(ctx, state, targetValue)

case "duration":
    return p.applySettingAction(ctx, state, targetValue)

case "select":
    return p.applySettingAction(ctx, state, targetValue)
```

Lalu satu tempat:

```go
func (p *Plugin) applySettingAction(...) error {
    if err := p.service.Set(...); err != nil {
        return err
    }

    return p.refresh(...)
}
```

Ini akan mengurangi duplication cukup signifikan.

---

# 12. `Selected string` adalah desain yang harus diubah

Ini menurut saya **salah satu masalah arsitektur terbesar di settings UI**.

Sekarang:

```go
Selected string
```

berisi:

```text
namespace:key
```

kemudian untuk integer:

```text
namespace:key:42
```

duration:

```text
namespace:key:5m
```

enum:

```text
namespace:key:value
```

Kemudian di callback diparse menggunakan:

```go
strings.Split(...)
strings.SplitN(...)
```

Ini sangat fragile.

Misalnya value:

```text
foo:bar
```

langsung bermasalah.

## Jangan encode domain state ke string.

Buat:

```go
type SettingTarget struct {
    Namespace string `json:"ns"`
    Key       string `json:"key"`
}

type SettingAction struct {
    Target SettingTarget `json:"target"`
    Value  string        `json:"value,omitempty"`
}
```

Lebih bagus lagi:

```go
type InteractionState struct {
    Scope     settings.Scope
    ScopeID   int64
    Category  string
    Page      int
    Setting   *SettingTarget
    Input     any
}
```

StateStore sudah bisa menyimpan arbitrary `any`, jadi tidak ada alasan menggunakan colon-delimited string.

---

# 13. Ada potensi bug scope pada settings UI

Ini perlu diperhatikan.

Di dashboard:

```go
state := MenuState{
    Scope:   settings.ScopeGlobal,
    ScopeID: 0,
}
```

kemudian scope diganti:

```go
nextScope = settings.ScopeChat
```

tetapi `ScopeID` tidak otomatis diisi chat ID pada state tersebut.

Akibatnya bisa menjadi:

```text
Scope = chat
ScopeID = 0
```

Padahal chat scope membutuhkan:

```text
ScopeID = current ChatID
```

Ini contoh bagus kenapa **state UI jangan dibiarkan menjadi sekumpulan field yang harus selalu sinkron secara manual**.

Buat:

```go
type ScopeRef struct {
    Type settings.SettingScope
    ID   int64
}
```

dan constructor:

```go
func GlobalScope() ScopeRef
func UserScope(id int64) ScopeRef
func ChatScope(id int64) ScopeRef
```

Kemudian validasi:

```go
func (s ScopeRef) Validate() error
```

---

# 14. UI package mulai mengalami "helper explosion"

Sekarang sudah ada:

```text
actions.go
alerts.go
button.go
card.go
confirmation.go
format.go
menu.go
navigator.go
progress.go
screen.go
toast.go
wizard.go
```

Ini belum tentu buruk, tetapi ada tanda bahwa abstraction dibuat berdasarkan **fitur UI**, bukan berdasarkan **responsibility**.

Misalnya:

```text
Button
Screen
Card
Menu
Wizard
Navigator
Actions
Toast
Alerts
Confirmation
```

bisa saling overlap.

Saya akan ubah menjadi:

```text
internal/ui/
├── component/
│   ├── button.go
│   ├── screen.go
│   ├── card.go
│   └── pagination.go
│
├── interaction/
│   ├── action.go
│   ├── confirmation.go
│   ├── form.go
│   ├── selector.go
│   └── wizard.go
│
├── navigation/
│   └── navigator.go
│
├── feedback/
│   ├── toast.go
│   ├── alert.go
│   ├── error.go
│   └── progress.go
│
└── render/
    └── telegram.go
```

Bukan wajib persis seperti ini, tapi prinsipnya:

> **component ≠ interaction ≠ state ≠ rendering ≠ feedback**

---

# 15. `Wizard` saat ini sebenarnya belum Wizard Engine

`internal/ui/wizard.go` sekarang hanya:

```go
WizardStep
Wizard
RenderStep()
```

Ia belum memiliki:

* current state
* transition
* validation
* input
* persistence
* cancellation
* expiration
* ownership
* submit
* rollback
* resume

Jadi nama `Wizard` agak misleading.

Lebih tepat:

```text
WizardRenderer
```

Sedangkan actual:

```text
InteractionWizard
```

harus berada di service layer:

```text
internal/services/interaction/wizard.go
```

Ini penting supaya UI package tidak menjadi tempat business state machine.

---

# 16. `Navigator` juga sebaiknya bukan bagian dari rendering UI

`Navigator` sekarang adalah stateful navigation model yang berada di `internal/ui`.

Menurut saya lebih tepat:

```text
internal/services/interaction/navigation
```

karena Navigator adalah **state/session concern**, bukan Telegram rendering.

UI hanya:

```go
ui.BackButton(...)
ui.HomeButton(...)
```

Sedangkan:

```go
navigator.Push(...)
navigator.Pop(...)
navigator.Replace(...)
```

berada di interaction service.

---

# 17. `Screen` terlalu tahu Telegram

`Screen.Render()`:

```go
markupClass := Markup{Rows: s.Rows}.ToTelegramMarkup()

if inlineMarkup, ok := markupClass.(*tg.ReplyInlineMarkup); ok {
    return text, inlineMarkup
}
```

Ini menyebabkan UI abstraction tidak sepenuhnya abstraction.

Lebih bersih:

```go
type RenderedScreen struct {
    Text   string
    Markup Markup
}
```

Lalu Telegram adapter:

```text
ui.Screen
   ↓
ui.RenderedScreen
   ↓
telegram.MarkupRenderer
   ↓
tg.ReplyInlineMarkup
```

Dengan begitu UI package tidak harus tahu `tg.*`.

---

# 18. `button.go` juga melakukan terlalu banyak

`button.go` sekarang:

* model button
* constructors
* pagination
* confirmation
* close
* back
* help
* standard action
* Telegram conversion

Ini terlalu banyak responsibility.

Pisahkan:

```text
button.go
pagination.go
navigation.go
actions.go
telegram_renderer.go
```

Terutama:

```go
ToTelegramMarkup()
```

harus berada di adapter/render package.

---

# 19. Jangan membuat abstraction yang hanya membungkus abstraction lain

Contoh:

```go
NewPaginationRow()
NewPaginationMarkup()
```

```go
NewConfirmCancelRow()
NewConfirmCancelMarkup()
```

```go
NewCloseRow()
NewCloseMarkup()
```

Ini memang convenience API, tetapi terlalu banyak wrapper kecil.

Kalau pattern ini terus berlanjut:

```text
XRow
XMarkup
XButton
XScreen
XCard
```

API akan menjadi gemuk.

Lebih baik:

```go
ui.Pagination(...)
ui.Confirm(...)
ui.Close(...)
```

menghasilkan component yang bisa ditempel ke Screen.

---

# 20. `noop` jangan menjadi magic string

Sekarang:

```go
[]byte("noop")
```

dan:

```go
if string(evt.Data) == "noop"
```

serta:

```go
action == "noop"
```

Ini harus dihilangkan.

Gunakan:

```go
const ActionNoop Action = ...
```

atau bahkan jangan generate callback sama sekali untuk button indikator halaman.

---

# 21. Callback data harus typed, bukan string protocol yang tersebar

Saat ini:

```go
EncodeCallbackData("settings", "toggle", oid)
```

Ini sudah lebih baik daripada raw string, tetapi masih protocol-level.

Target:

```go
type CallbackAction struct {
    Namespace string
    Action    string
    StateID   string
}
```

dan helper:

```go
callback.NewAction(
    callback.NamespaceSettings,
    settings.ActionToggle,
    stateID,
)
```

Dengan constants:

```go
const (
    ActionNavigate Action = "navigate"
    ActionToggle   Action = "toggle"
    ActionStep     Action = "step"
    ActionReset    Action = "reset"
)
```

Ini menghilangkan typo:

```text
"toggle"
"toggel"
"step"
"steps"
"nav"
"navigate"
```

---

# 22. Plugin settings jangan menjadi special implementation

Sekarang settings plugin sendiri mengetahui:

```text
bool → BuildStateToggle
int → BuildStepper
duration → BuildDurationPicker
enum → BuildSelector
```

Padahal Registry sudah mengetahui type.

Target architecture:

```text
SettingDefinition
       │
       ▼
SettingWidgetFactory
       │
       ├── BoolWidget
       ├── IntWidget
       ├── DurationWidget
       ├── EnumWidget
       └── StringWidget
```

Jadi plugin settings hanya:

```go
screen := settingsUI.Render(
    settings.View{
        Scope: ...
    },
)
```

Tidak perlu switch type lagi.

---

# 23. Buat `SettingsViewModel`

Saat ini `plugins/settings` mencampur:

```text
DB access
settings resolution
inheritance detection
UI state
Telegram callback
rendering
```

Terlalu banyak.

Buat:

```go
type SettingViewModel struct {
    Definition SettingDefinition
    Value      string
    Source     ValueSource
    Overridden bool
}
```

Service:

```go
func (s *Service) BuildView(
    ctx context.Context,
    scope ScopeRef,
    def SettingDefinition,
) (SettingViewModel, error)
```

UI:

```go
ui.RenderSetting(vm)
```

Plugin:

```text
callback
   ↓
application
   ↓
settings service
   ↓
view model
   ↓
UI
```

Jauh lebih bersih.

---

# 24. Jangan mengabaikan error dengan `_`

Saya melihat beberapa pola seperti:

```go
_ = settings.RegisterDefaultDefinitions(...)
_ = callbackRouter.Register(...)
```

dan beberapa operasi serupa di bootstrap.

Untuk registration infrastructure, ini seharusnya **fail fast**.

Contoh:

```go
if err := settings.RegisterDefaultDefinitions(registry); err != nil {
    return nil, fmt.Errorf("register settings: %w", err)
}
```

Kalau callback handler gagal register:

```text
application should not start
```

karena UI kemudian akan punya tombol yang tidak memiliki handler.

---

# 25. `SettingChangedEvent` saat ini belum transactional

`Service.Set()`:

```text
Get old value
 ↓
repo.SetSetting()
 ↓
bus.Publish()
```

Masalah:

```text
DB sukses
EventBus gagal / consumer tidak jalan
```

maka:

```text
DB = new
runtime = old
```

Ini persis jenis bug yang tadi kita ingin hindari.

Target:

```text
Setting mutation
      │
      ├── DB transaction
      │
      └── durable event/outbox
```

atau minimal:

```text
DB commit
   ↓
event dispatch
   ↓
runtime reconcile
```

Untuk settings yang critical, saya lebih menyarankan **transactional outbox**.

---

# 26. `Import()` seharusnya transactionally atomic

Sekarang:

```go
for ns, kv := range data {
    for k, v := range kv {
        s.Set(...)
    }
}
```

Jika:

```text
100 setting
setting #57 gagal
```

maka:

```text
56 sudah berubah
44 belum
```

Ini tidak ideal.

Harus:

```go
Import(...)
   ↓
Validate ALL
   ↓
BEGIN TRANSACTION
   ↓
Apply ALL
   ↓
COMMIT
```

atau:

```text
ImportResult
 ├── applied
 ├── rejected
 └── validation errors
```

tergantung semantics yang diinginkan.

---

# 27. `Reset()` juga perlu memakai actor identity

`Set()` memiliki:

```go
updaterID
```

tetapi:

```go
Reset(...)
```

tidak menerima actor.

Kemudian event:

```go
ChangedBy: 0
```

Ini inkonsisten.

Harus:

```go
Reset(ctx, scope, target, actor)
```

supaya audit log benar.

---

# 28. Settings `Scope` sebaiknya typed end-to-end

Sekarang service masih sering melakukan:

```go
string(scope)
```

dan repository memakai string.

Ideal:

```text
Domain:
    SettingScope

Repository:
    SettingScope

DB:
    serialization to TEXT
```

Jangan:

```text
Domain enum
 ↓
string
 ↓
repository
 ↓
string
 ↓
DB
```

karena validation scope menjadi tersebar.

---

# 29. Resolver sebaiknya tidak melakukan 3–4 query sequential setiap kali

`Resolve()`:

```text
chat query
 ↓
user query
 ↓
global query
 ↓
registry default
```

Secara correctness oke, tetapi untuk feature-heavy userbot ini bisa dipanggil sangat sering.

Target:

```go
Resolve(ctx, scopeContext, key)
```

mengambil batch:

```text
GetEffectiveSettings(...)
```

atau caching:

```text
(chat,user,key)
       ↓
resolver cache
       ↓
DB
```

Dengan invalidation berdasarkan:

```text
SettingChangedEvent
```

Jangan caching tanpa invalidation.

---

# 30. Dispatcher juga terlalu banyak concern

`Dispatcher` sekarang menangani:

```text
updates
message handlers
commands
callbacks
inline
reactions
album
peer cache
shutdown
metrics
localization
resolver
permissions
executor
```

Ini sudah mulai menjadi **second God Object** setelah `App`.

Saya sarankan:

```text
telegram/
├── dispatcher/
│   ├── dispatcher.go
│   ├── message.go
│   ├── channel.go
│   ├── edit.go
│   ├── delete.go
│   └── reactions.go
│
├── command/
│   ├── executor.go
│   └── middleware.go
│
├── callback/
│   └── adapter.go
│
├── inline/
│   └── adapter.go
│
└── peer/
    └── cache_worker.go
```

`Dispatcher` hanya menjadi orchestrator:

```go
func (d *Dispatcher) Dispatch(update Update) {
    switch update.Type() {
    case Message:
        d.messages.Handle(...)
    case Callback:
        d.callbacks.Handle(...)
    case Inline:
        d.inline.Handle(...)
    }
}
```

---

# 31. Gunakan interface kecil, bukan interface besar

Misalnya service plugin jangan diberikan:

```go
*telegram.Client
```

kalau cuma membutuhkan:

```go
SendMessage()
EditMessage()
DeleteMessage()
```

Buat:

```go
type MessageSender interface {
    SendMessage(...)
}

type MessageEditor interface {
    EditMessage(...)
}
```

Ini akan:

* meningkatkan testability
* mengurangi coupling
* memudahkan mock
* mengurangi dependency graph

---

# 32. Hindari package saling mengetahui implementation detail

Target dependency:

```text
plugins
   ↓
application services
   ↓
domain
   ↓
repository interfaces
   ↓
database implementation
```

Bukan:

```text
plugin
 ↓
DB
 ↓
telegram
 ↓
settings
 ↓
ui
```

Plugin seharusnya tidak perlu tahu detail DB.

---

# 33. Gunakan "Use Case" untuk operasi yang dipakai command + button

Ini sangat penting untuk tujuan yang kita bahas sebelumnya.

Contoh filter:

Jangan:

```text
.filter command
    ↓
filter DB

filter button
    ↓
filter DB
```

Tetapi:

```text
             ┌── command
             │
Telegram ────┤
             │
             └── button
                  ↓
              Use Case
                  ↓
             FilterService
                  ↓
                  DB
```

Misalnya:

```go
type SetFilterUseCase struct {
    Filters FilterRepository
}

func (uc *SetFilterUseCase) Execute(...)
```

Command dan button sama-sama memanggilnya.

---

# 34. Standarkan flow seluruh plugin

Setiap plugin sebaiknya mengikuti:

```text
Plugin
 │
 ├── Definition
 │
 ├── Command Adapter
 │
 ├── Callback Adapter
 │
 ├── UI
 │
 └── Use Cases
```

Contoh:

```text
plugins/filters/
├── plugin.go
├── commands.go
├── callbacks.go
├── ui.go
└── usecase/
    ├── create.go
    ├── delete.go
    ├── update.go
    └── list.go
```

Bukan semua dalam:

```text
filters.go
```

yang akhirnya menjadi 2.000+ lines.

---

# 35. Buat convention kode yang ketat

Saya sangat menyarankan GoUltroid menetapkan aturan:

### Function

```text
<verb><noun>
```

Contoh:

```go
ResolveSetting()
SaveFilter()
DeleteNote()
RenderSettings()
```

hindari:

```go
Do()
Handle()
Process()
Manage()
```

kalau bisa lebih spesifik.

---

### Error wrapping

Semua boundary:

```go
return fmt.Errorf("save setting %s:%s: %w", ns, key, err)
```

Bagus.

Tetapi jangan menghasilkan error yang sama berkali-kali.

Buat sentinel/domain error:

```go
var (
    ErrSettingNotFound
    ErrInvalidSetting
    ErrUnauthorized
    ErrExpired
)
```

lalu UI mapping:

```go
errors.Is(err, ErrUnauthorized)
```

---

# 36. Jangan render error langsung dari domain

Sekarang ada pola:

```go
ui.AnswerErrorToast(ctx, "Failed: "+err.Error())
```

Jangan membuat domain error langsung menjadi UI string.

Buat:

```text
domain error
     ↓
error classifier
     ↓
UI message
```

Karena error:

```text
failed to save sqlite constraint
```

tidak cocok ditampilkan ke user.

---

# 37. Buat `OperationResult`

Untuk UI interaktif, saya sarankan:

```go
type Result struct {
    Status  Status
    Message string
    Data    any
    Retry   bool
}
```

atau domain-oriented:

```go
type OperationResult[T any] struct {
    Value       T
    Notification Notification
}
```

Sehingga UI tidak perlu menebak bagaimana menangani error.

---

# 38. Naming sekarang perlu distandarkan

Saya melihat beberapa pola yang bisa dibuat lebih konsisten:

```text
Service
Engine
Manager
Registry
Router
Store
Plugin
Client
```

Semua valid, tetapi harus ada rule.

Saya sarankan:

```text
Repository = persistence abstraction
Store      = state/cache storage
Service    = domain/application operation
Engine     = long-running processing loop
Manager    = lifecycle/collection management
Registry   = definitions/lookup
Router     = dispatch
Client     = external transport
Plugin     = feature module
```

Kalau suatu object tidak sesuai definition tersebut, jangan gunakan nama itu.

---

# 39. `Manager` dan `Engine` jangan menjadi tempat "apa saja"

Ini sering terjadi pada proyek yang makin besar.

Contoh:

```text
PluginManager
AddonManager
SchedulerEngine
InlineEngine
```

Pastikan masing-masing mempunyai satu responsibility.

Kalau suatu `Manager` mulai punya:

```text
load
save
validate
execute
render
authorize
cache
notify
```

berarti harus dipecah.

---

# 40. Buat architecture rule supaya masalah tidak kembali

Ini justru **lebih penting daripada refactor sekali**.

Tambahkan static checks:

### `internal/ui` tidak boleh import:

```text
database
plugins
scheduler
```

### `settings` tidak boleh import:

```text
plugins
ui
telegram
```

### domain/service tidak boleh import:

```text
tg.*
```

### plugin tidak boleh langsung:

```go
db.Exec(...)
```

kecuali memang repository-specific plugin tertentu.

### Telegram adapter tidak boleh mengandung business rules.

---

# 41. Gunakan `go vet` + lint architecture

Minimal:

```text
go vet ./...
go test ./...
staticcheck ./...
golangci-lint run
```

Tambahkan:

* `errcheck`
* `govet`
* `staticcheck`
* `unused`
* `ineffassign`
* `gocritic`
* `revive`
* `misspell`

Dan untuk architecture:

```text
import restrictions
dependency cycle detection
```

---

# 42. Test bukan hanya function test, tetapi architecture test

Tambahkan test yang memastikan:

```text
Settings.Set
 → DB
 → Event
```

dan:

```text
Command
 ┐
 ├→ same use case
 ┘
Button
```

dan:

```text
scope global
scope user
scope chat
```

dan:

```text
callback
 → wrong user = rejected
 → wrong chat = rejected
 → expired = rejected
 → consumed = rejected
 → valid = executed
```

---

# 43. Target architecture yang saya rekomendasikan

Kalau saya melakukan refactor GoUltroid sekarang, target akhirnya:

```text
internal/
│
├── app/
│   ├── bootstrap.go
│   ├── dependencies.go
│   ├── lifecycle.go
│   └── shutdown.go
│
├── domain/
│   ├── settings/
│   ├── moderation/
│   ├── scheduler/
│   ├── filters/
│   └── peers/
│
├── application/
│   ├── settings/
│   ├── moderation/
│   ├── scheduler/
│   └── interactions/
│
├── infrastructure/
│   ├── database/
│   ├── telegram/
│   ├── storage/
│   ├── logging/
│   └── metrics/
│
├── services/
│   ├── callback/
│   ├── inline/
│   ├── interaction/
│   ├── localization/
│   └── ratelimit/
│
├── ui/
│   ├── components/
│   ├── navigation/
│   ├── forms/
│   ├── feedback/
│   └── telegram/
│
└── plugin/
    ├── manager/
    ├── registry/
    └── runtime/
```

Plugin:

```text
plugins/
├── settings/
├── moderation/
├── filters/
├── scheduler/
├── notes/
└── ...
```

dengan dependency:

```text
                 Telegram
                    │
                    ▼
              Adapter Layer
                    │
                    ▼
               Interaction
                    │
          ┌─────────┴─────────┐
          ▼                   ▼
       Command              Button
          │                   │
          └─────────┬─────────┘
                    ▼
                Use Case
                    │
                    ▼
                Domain
                    │
                    ▼
              Repository
                    │
                    ▼
                    DB
```

---

# Prioritas refactor saya

Jangan refactor semuanya sekaligus. Urutannya:

### P0 — Wajib

1. **Pisahkan `app.go`**
2. **Pisahkan Dispatcher**
3. **Refactor callback pipeline**
4. **Hilangkan `Selected string`**
5. **Satukan setting mutation path**
6. **Perbaiki lifecycle `context.Background()`**
7. **Typed Scope**
8. **Typed callback actions**
9. **Hilangkan ignored errors pada bootstrap**
10. **Transactional settings import**

### P1 — Sangat disarankan

11. Typed `SettingValue`
12. Setting ViewModel
13. Setting Widget Factory
14. Interaction Service
15. Wizard Engine sebenarnya
16. Navigator pindah dari UI ke interaction
17. Telegram renderer dipisahkan dari UI model
18. Use-case layer untuk command/button
19. Interface dependency injection
20. Error → user-facing message mapper

### P2 — Quality / maintainability

21. Architecture tests
22. Import restrictions
23. Linter rules
24. Standard package conventions
25. Standard plugin structure
26. Standard callback conventions
27. Standard lifecycle conventions
28. Benchmark settings resolver/cache
29. Integration tests
30. Race tests (`go test -race ./...`)

---

## Kesimpulan audit terbaru

**GoUltroid sekarang sudah jauh lebih bagus dibanding struktur awalnya.** Bahkan dibanding pola plugin Ultroid klasik yang memang sangat decorator/plugin-centric, GoUltroid sudah punya fondasi infrastructure yang lebih formal: registry, callback state, settings registry/service, UI abstractions, scheduler, event bus, dan lifecycle. Ultroid sendiri secara resmi memusatkan extension pada command handler, inline handler, dan callback handler, sedangkan GoUltroid sekarang sudah mencoba membangun abstraction layer di atas pola tersebut. ([GitHub][1])

Tetapi sekarang muncul fase berikutnya:

> **bukan lagi kekurangan abstraction, melainkan terlalu banyak abstraction yang belum memiliki boundary dan ownership yang tegas.**

Yang paling saya khawatirkan kalau diteruskan tanpa refactor adalah:

```text
Helper
  ↓
Helper
  ↓
Service
  ↓
Plugin
  ↓
Dispatcher
  ↓
App
```

dan akhirnya setiap fitur baru membutuhkan:

```text
1 callback
1 state format
1 parser
1 renderer
1 service call
1 error handling
1 DB operation
1 UI refresh
```

yang semuanya ditulis ulang.

Target berikutnya harus berubah menjadi:

```text
                  ┌── Command
                  │
Telegram Update ──┤
                  │
                  └── Button / Wizard
                         │
                         ▼
                  Interaction Layer
                         │
                         ▼
                     Use Case
                         │
             ┌───────────┴──────────┐
             ▼                      ▼
          Domain                 Settings
             │                      │
             └───────────┬──────────┘
                         ▼
                    Repository
                         │
                         ▼
                         DB
                         │
                         ▼
                   Event / Outbox
                         │
                         ▼
                    Runtime update
```

**Kalau struktur ini diterapkan, fitur seperti toggle, enable/disable, increment/decrement, wizard, selector, scheduler UI, moderation UI, plugin settings, command, dan callback tidak lagi membutuhkan implementasi paralel.** Satu use-case bisa digunakan oleh semuanya.

Jadi, menurut saya **refactor berikutnya seharusnya bukan menambah UI feature lagi**. Prioritas sekarang adalah **merapikan ownership, dependency direction, state model, callback pipeline, settings mutation path, dan composition root**. Setelah itu baru menambah wizard/form/UI secara agresif. Itu akan jauh lebih aman dan mencegah GoUltroid berubah menjadi kumpulan abstraction/helper yang saling tumpang tindih.

[1]: https://github.com/TeamUltroid/Ultroid/wiki/Creating-Plugins?utm_source=chatgpt.com "Creating Plugins · TeamUltroid/Ultroid Wiki · GitHub"
