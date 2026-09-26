package myxl

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/callback"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/ui"
	"github.com/inipew/goultroid/internal/ui/render"
)

var (
	_ execution.CapabilityProvider = (*Plugin)(nil)
	_ callback.Handler             = (*Plugin)(nil)
	_ callback.HandlerWithOptions  = (*Plugin)(nil)
)

// Plugin provides MyXL account management and quota viewing commands.
type Plugin struct {
	repo       Repository
	client     *Client
	stateStore callback.StateWriter
	menuMgr    *MenuManager
	files      *filesystem.Scope
	tasks      tasks.Client
	qrTaskSeq  atomic.Uint64

	assistantMu      sync.RWMutex
	assistantRuntime interaction.DriverRuntime
}

const (
	myxlCallbackTTL   = 24 * time.Hour
	pendingQRISTTL    = 5 * time.Minute
	myxlQRSendTimeout = 10 * time.Second
)

type quotaRefreshState struct {
	MSISDN string
	Masked bool
}

type purchaseDraftState struct {
	MSISDN            string
	OptionCode        string
	PackageName       string
	Price             int64
	TokenConfirmation string
	Method            string
	WalletNumber      string
	OverwritePrice    int64
	HasOverwrite      bool
}

// New creates a new MyXL plugin instance.
func New(repo Repository, client *Client) *Plugin {
	if client == nil {
		client = NewClient(DefaultClientConfig(), repo, nil)
	}
	p := &Plugin{
		repo:   repo,
		client: client,
	}
	p.menuMgr = NewMenuManager(p)
	return p
}

// SetStateStore configures the callback state store.
func (p *Plugin) SetStateStore(store callback.StateWriter) {
	p.stateStore = store
}

// SetTaskClient configures staged TaskEngine access for scarce QR media sends.
func (p *Plugin) SetTaskClient(client tasks.Client) {
	p.tasks = client
}

// MenuManager returns the MenuManager instance.
func (p *Plugin) MenuManager() *MenuManager {
	return p.menuMgr
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "myxl"
}

// Namespace returns the callback routing namespace.
func (p *Plugin) Namespace() string {
	return "myxl"
}

// CallbackOptions configures immediate ack behavior for callback interactions.
func (p *Plugin) CallbackOptions() callback.CallbackHandlerOptions {
	// MyXL handlers return action-specific callback answers. Pre-answering here
	// would consume Telegram's single callback acknowledgement and turn the
	// handler's later Answer call into ErrCallbackAlreadyAnswered on Assistant.
	return callback.CallbackHandlerOptions{AutoAnswer: false}
}

// RequiresCallbackState fails closed only for transaction actions whose opaque
// id must resolve to a live single-use purchase draft. Other MyXL callbacks
// intentionally mix menu/session state with stateless navigation.
func (p *Plugin) RequiresCallbackState(action, _ string) bool {
	switch action {
	case "buy_confirm", "buy_cancel":
		return true
	default:
		return false
	}
}

// Description returns a summary of the plugin functionality.
func (p *Plugin) Description() string {
	return "MyXL account management and real-time quota visualizer"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// Capabilities declares the capabilities provided by this plugin across surfaces.
func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{
		{
			ID:          "myxl",
			Name:        "MyXL Account & Quota",
			Description: "Manage MyXL cellular accounts and inspect real-time internet quota",
			Category:    "Utility",
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
		},
	}
}

// InitPlugin initializes capability-gated platform accessors (network and secrets).
func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error {
	netSvc, err := pctx.HTTP()
	if err != nil {
		return err
	}
	if p.client != nil {
		p.client.SetHTTP(netSvc)
	}

	fs, err := pctx.Files()
	if err != nil {
		return fmt.Errorf("initialize MyXL filesystem: %w", err)
	}
	if fs == nil {
		return fmt.Errorf("initialize MyXL filesystem: filesystem scope is nil")
	}
	p.files = fs

	taskClient, err := pctx.TaskClient()
	if err != nil {
		return fmt.Errorf("initialize MyXL task client: %w", err)
	}
	p.tasks = taskClient

	secMgr, err := pctx.Secrets()
	if err != nil {
		return fmt.Errorf("initialize MyXL secrets: %w", err)
	}
	if secMgr == nil {
		return fmt.Errorf("initialize MyXL secrets: secret manager is nil")
	}
	if p.client != nil {
		p.client.UpdateConfig(func(cfg *ClientConfig) {
			if k, err := secMgr.Get("MYXL_API_KEY"); err == nil && strings.TrimSpace(k) != "" {
				cfg.APIKey = strings.TrimSpace(k)
			}
			if ba, err := secMgr.Get("MYXL_BASIC_AUTH"); err == nil && strings.TrimSpace(ba) != "" {
				cfg.BasicAuth = strings.TrimSpace(ba)
			}
			if xk, err := secMgr.Get("MYXL_XDATA_KEY"); err == nil && strings.TrimSpace(xk) != "" {
				cfg.XDataKey = strings.TrimSpace(xk)
			}
			if sec, err := secMgr.Get("MYXL_X_API_BASE_SECRET"); err == nil && strings.TrimSpace(sec) != "" {
				cfg.XAPIBaseSecret = strings.TrimSpace(sec)
			}
			if fp, err := secMgr.Get("MYXL_AX_FP_KEY"); err == nil && strings.TrimSpace(fp) != "" {
				cfg.AxFPKey = strings.TrimSpace(fp)
			}
			if sig, err := secMgr.Get("MYXL_AX_API_SIG_KEY"); err == nil && strings.TrimSpace(sig) != "" {
				cfg.AxAPISigKey = strings.TrimSpace(sig)
			}
			if pss, err := secMgr.Get("MYXL_PAYMENT_SIG_SECRET"); err == nil && strings.TrimSpace(pss) != "" {
				cfg.PaymentSigSecret = strings.TrimSpace(pss)
			}
			if ef, err := secMgr.Get("MYXL_ENCRYPTED_FIELD_KEY"); err == nil && strings.TrimSpace(ef) != "" {
				cfg.EncryptedFieldKey = strings.TrimSpace(ef)
			}
			if circle, err := secMgr.Get("MYXL_CIRCLE_MSISDN_KEY"); err == nil && strings.TrimSpace(circle) != "" {
				cfg.CircleMSISDNKey = strings.TrimSpace(circle)
			}
		})
	}
	return nil
}

// Commands returns the commands registered by the MyXL plugin.
func (p *Plugin) Commands() []core.Command {
	return []core.Command{
		{
			Name:        "myxl",
			Aliases:     []string{"xlcli"},
			Description: "MyXL account manager and interactive menu",
			Usage:       ".myxl [login|otp|refresh|accounts|use|alias|status|del|kuota|family|paket|saved|buy]",
			Category:    "Utility",
			Permission:  core.PermissionOwner,
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
			Handler:     p.handleMyXL,
		},
		{
			Name:        "kuota",
			Aliases:     []string{"myquota", "xl"},
			Description: "Check balance and active quota for MyXL",
			Usage:       ".kuota [msisdn/alias]",
			Category:    "Utility",
			Permission:  core.PermissionOwner,
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
			Handler:     p.handleKuotaShortcut,
		},
		{
			Name:        "beli",
			Aliases:     []string{"buy"},
			Description: "Beli paket MyXL langsung",
			Usage:       ".beli <option_code> [metode] [overwrite_harga] [nomor_ewallet]",
			Category:    "Utility",
			Permission:  core.PermissionOwner,
			Surfaces:    execution.SurfaceUserbot | execution.SurfaceAssistant,
			Handler:     p.handleBuyShortcut,
		},
	}
}

func getContext(ctx *core.Context) context.Context {
	if ctx != nil && ctx.Ctx != nil {
		return ctx.Ctx
	}
	return context.Background()
}

func normalizeAlias(alias string) (string, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return "", errors.New("alias tidak boleh kosong")
	}
	if len([]rune(alias)) > 24 {
		return "", errors.New("panjang alias maksimal 24 karakter")
	}
	for _, r := range alias {
		if unicode.IsControl(r) {
			return "", errors.New("alias mengandung karakter kontrol")
		}
	}
	return alias, nil
}

func isGroupChat(chat *core.Chat) bool {
	if chat == nil {
		return false
	}
	return chat.Type == "group" || chat.Type == "supergroup" || chat.Type == "channel"
}

func (p *Plugin) handleMyXL(ctx *core.Context) error {
	if len(ctx.Args) == 0 || (len(ctx.Args) == 1 && strings.EqualFold(ctx.Args[0], "menu")) {
		if ctx.IsAssistant() {
			if isGroupChat(ctx.Chat) {
				return ctx.EditOrReply("🔒 Menu interaktif MyXL hanya tersedia di chat pribadi karena memuat nomor akun, OTP, dan tindakan pembelian.")
			}
			return p.openAssistant(ctx)
		}
		return ctx.EditOrReply(
			"📱 <b>MyXL Plugin Menu</b>\n\n" +
				"• <code>.myxl login &lt;nomor&gt;</code> - Minta kode OTP SMS\n" +
				"• <code>.myxl otp &lt;nomor&gt; &lt;kode&gt;</code> - Verifikasi OTP dan simpan akun\n" +
				"• <code>.myxl refresh [nomor/alias]</code> - Force refresh token CIAM\n" +
				"• <code>.myxl accounts</code> - Daftar akun tersimpan\n" +
				"• <code>.myxl use &lt;nomor/alias&gt;</code> - Ganti akun aktif\n" +
				"• <code>.myxl alias &lt;nomor&gt; &lt;alias&gt;</code> - Ubah alias akun\n" +
				"• <code>.myxl status</code> - Status akun aktif\n" +
				"• <code>.myxl del &lt;nomor/alias&gt;</code> - Hapus akun\n" +
				"• <code>.myxl kuota</code> - Cek kuota dan pulsa\n" +
				"• <code>.myxl family &lt;family_code&gt;</code> - Cari paket family\n" +
				"• <code>.myxl paket &lt;option_code&gt;</code> - Detail paket\n" +
				"• <code>.myxl saved [list|add|del|buy]</code> - Kelola favorit\n" +
				"• <code>.myxl buy &lt;option_code&gt; [metode] [harga] [nomor]</code> - Beli paket\n" +
				"• <code>.myxl qris [cancel]</code> - Cek/batalkan QRIS aktif\n" +
				"• <code>.kuota</code> - Shortcut cek kuota",
		)
	}

	subCmd := strings.ToLower(ctx.Args[0])
	args := ctx.Args[1:]

	switch subCmd {
	case "login":
		return p.handleLogin(ctx, args)
	case "otp":
		return p.handleOTP(ctx, args)
	case "refresh":
		return p.handleRefreshToken(ctx, args)
	case "accounts", "list":
		return p.handleListAccounts(ctx)
	case "use", "switch":
		return p.handleUseAccount(ctx, args)
	case "alias":
		return p.handleSetAlias(ctx, args)
	case "status", "info":
		return p.handleStatus(ctx)
	case "del", "delete", "rm":
		return p.handleDeleteAccount(ctx, args)
	case "kuota", "quota", "balance":
		return p.handleShowQuota(ctx, args)
	case "family", "cari", "search":
		return p.handleSearchFamily(ctx, args)
	case "paket", "package", "detail":
		return p.handlePackageDetail(ctx, args)
	case "saved", "bookmark", "bm":
		return p.handleSavedPackages(ctx, args)
	case "buy", "beli":
		return p.handleBuy(ctx, args)
	case "qris", "pending":
		return p.handlePendingQRIS(ctx, args)
	default:
		return ctx.Status(fmt.Sprintf("Subcommand <code>%s</code> tidak dikenal. Ketik <code>.myxl</code> untuk bantuan.", html.EscapeString(subCmd)))
	}
}

func (p *Plugin) handleLogin(ctx *core.Context, args []string) error {
	if len(args) == 0 {
		return ctx.Status("Format salah! Gunakan: <code>.myxl login &lt;nomor_hp&gt;</code>\nContoh: <code>.myxl login 081912345678</code>")
	}

	rawMSISDN := args[0]
	msisdn, err := NormalizeMSISDN(rawMSISDN)
	if err != nil {
		return ctx.Fail(err, "Nomor HP tidak valid.")
	}

	_ = ctx.Progress(fmt.Sprintf("Mengirim permintaan OTP ke <code>%s</code>...", html.EscapeString(msisdn)))

	cCtx, cancel := context.WithTimeout(getContext(ctx), 20*time.Second)
	defer cancel()

	subID, err := p.client.RequestOTP(cCtx, msisdn)
	if err != nil {
		return ctx.Fail(err, "Gagal meminta OTP. Silakan coba lagi.")
	}

	// Save or update placeholder account with subscriber_id if present
	existing, _ := p.repo.GetByMSISDN(cCtx, msisdn)
	if existing == nil {
		existing = &Account{
			MSISDN:       msisdn,
			SubscriberID: subID,
		}
	} else if subID != "" {
		existing.SubscriberID = subID
	}
	_ = p.repo.Save(cCtx, existing)

	return ctx.Success(
		fmt.Sprintf("<b>Kode OTP telah dikirimkan via SMS</b> ke <code>%s</code>!\n\n"+
			"Segera masukkan kode OTP dengan perintah:\n"+
			"<code>.myxl otp %s &lt;kode_otp&gt;</code>",
			html.EscapeString(msisdn), html.EscapeString(msisdn)),
	)
}

func (p *Plugin) handleOTP(ctx *core.Context, args []string) error {
	if len(args) < 2 {
		return ctx.Status("Format salah! Gunakan: <code>.myxl otp &lt;nomor_hp&gt; &lt;kode_otp&gt;</code>\nContoh: <code>.myxl otp 081912345678 123456</code>")
	}

	rawMSISDN := args[0]
	code := args[1]

	msisdn, err := NormalizeMSISDN(rawMSISDN)
	if err != nil {
		return ctx.Fail(err, "Nomor HP tidak valid.")
	}

	_ = ctx.Progress(fmt.Sprintf("Memverifikasi kode OTP untuk <code>%s</code>...", html.EscapeString(msisdn)))

	cCtx, cancel := context.WithTimeout(getContext(ctx), 25*time.Second)
	defer cancel()

	tokens, err := p.client.SubmitOTP(cCtx, msisdn, code)
	if err != nil {
		return ctx.Fail(err, "Verifikasi OTP gagal. Periksa kode lalu coba lagi.")
	}

	acc, _ := p.repo.GetByMSISDN(cCtx, msisdn)
	if acc == nil {
		acc = &Account{
			MSISDN: msisdn,
		}
	}
	acc.AccessToken = tokens.AccessToken
	acc.IDToken = tokens.IDToken
	acc.RefreshToken = tokens.RefreshToken
	if tokens.ExpiresIn > 0 {
		acc.TokenExpiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	} else {
		acc.TokenExpiresAt = time.Now().Add(DefaultTokenExpiryFallback)
	}
	acc.IsActive = true

	if err := p.repo.Save(cCtx, acc); err != nil {
		return ctx.Fail(err, "Login berhasil, tetapi akun gagal disimpan. Periksa log sebelum mencoba ulang.")
	}

	return ctx.Success(
		fmt.Sprintf("<b>Login Berhasil!</b>\n\n"+
			"Nomor <code>%s</code> telah tersimpan dan dijadikan sebagai <b>akun aktif</b>.\n\n"+
			"Gunakan <code>.kuota</code> untuk memeriksa sisa kuota dan pulsa Anda.",
			html.EscapeString(msisdn)),
	)
}

func (p *Plugin) handleSetAlias(ctx *core.Context, args []string) error {
	if len(args) < 2 {
		return ctx.Status("Format: <code>.myxl alias &lt;nomor/alias_lama&gt; &lt;alias_baru&gt;</code>\nContoh: <code>.myxl alias 081912345678 Utama</code>")
	}

	target := args[0]
	if norm, err := NormalizeMSISDN(target); err == nil {
		target = norm
	}
	newAlias, err := normalizeAlias(strings.Join(args[1:], " "))
	if err != nil {
		return ctx.Fail(err, "Alias tidak valid. Gunakan nama alias yang lebih sederhana.")
	}

	cCtx := getContext(ctx)
	if err := p.repo.SetAlias(cCtx, target, newAlias); err != nil {
		return ctx.Fail(err, "Gagal mengatur alias.")
	}

	return ctx.Success(fmt.Sprintf("Alias untuk <code>%s</code> berhasil diatur menjadi <b>%s</b>.", html.EscapeString(target), html.EscapeString(newAlias)))
}

func (p *Plugin) handleStatus(ctx *core.Context) error {
	cCtx := getContext(ctx)
	acc, err := p.repo.GetActive(cCtx)
	if err != nil {
		return ctx.Fail(err, "Error database.")
	}
	if acc == nil {
		return ctx.Status("Tidak ada akun MyXL aktif saat ini. Gunakan <code>.myxl login &lt;nomor&gt;</code> untuk login.")
	}

	maskMSISDN := isGroupChat(ctx.Chat)
	displayNum := acc.MSISDN
	if maskMSISDN {
		displayNum = MaskMSISDN(acc.MSISDN)
	}

	tokenState := "🟢 Terautentikasi"
	if acc.IDToken == "" && acc.RefreshToken == "" {
		tokenState = "🔴 Belum Login"
	}

	var b strings.Builder
	b.WriteString("ℹ️ <b>Status Akun MyXL Aktif</b>\n\n")
	b.WriteString(fmt.Sprintf("• <b>Nomor:</b> <code>%s</code>\n", html.EscapeString(displayNum)))
	if acc.Alias != "" {
		b.WriteString(fmt.Sprintf("• <b>Alias:</b> <i>%s</i>\n", html.EscapeString(acc.Alias)))
	}
	b.WriteString(fmt.Sprintf("• <b>Status Sesi:</b> %s\n", tokenState))
	if acc.SubscriberID != "" {
		b.WriteString(fmt.Sprintf("• <b>Subscriber ID:</b> <code>%s</code>\n", html.EscapeString(acc.SubscriberID)))
	}
	b.WriteString(fmt.Sprintf("• <b>Terdaftar Sejak:</b> <code>%s</code>\n", acc.CreatedAt.In(time.FixedZone("WIB", 7*3600)).Format("02 Jan 2006 15:04 WIB")))

	return ctx.EditOrReply(b.String())
}

func (p *Plugin) handleListAccounts(ctx *core.Context) error {
	cCtx := getContext(ctx)
	accounts, err := p.repo.List(cCtx)
	if err != nil {
		return ctx.Fail(err, "Gagal memuat akun.")
	}

	if len(accounts) == 0 {
		return ctx.EditOrReply("<i>Belum ada akun MyXL yang tersimpan. Gunakan <code>.myxl login &lt;nomor&gt;</code> untuk login.</i>")
	}

	maskMSISDN := isGroupChat(ctx.Chat)

	var b strings.Builder
	b.WriteString("📱 <b>Daftar Akun MyXL Tersimpan:</b>\n\n")

	for i, acc := range accounts {
		status := "▫️"
		if acc.IsActive {
			status = "🟢 [AKTIF]"
		}

		num := acc.MSISDN
		if maskMSISDN {
			num = MaskMSISDN(acc.MSISDN)
		}

		b.WriteString(fmt.Sprintf("%d. %s <code>%s</code>", i+1, status, html.EscapeString(num)))
		if acc.Alias != "" {
			b.WriteString(fmt.Sprintf(" (<i>%s</i>)", html.EscapeString(acc.Alias)))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n💡 <i>Ganti akun aktif:</i> <code>.myxl use &lt;nomor/alias&gt;</code>\n")
	b.WriteString("🏷️ <i>Beri nama alias:</i> <code>.myxl alias &lt;nomor&gt; &lt;nama&gt;</code>\n")
	b.WriteString("🗑️ <i>Hapus akun:</i> <code>.myxl del &lt;nomor/alias&gt;</code>")

	return deliverHTML(ctx, b.String())
}

func (p *Plugin) handleUseAccount(ctx *core.Context, args []string) error {
	if len(args) == 0 {
		return ctx.Status("Masukkan nomor atau alias akun! Contoh: <code>.myxl use 081912345678</code> atau <code>.myxl use Utama</code>")
	}

	target := args[0]
	if norm, err := NormalizeMSISDN(target); err == nil {
		target = norm
	}

	cCtx := getContext(ctx)
	if err := p.repo.SetActive(cCtx, target); err != nil {
		return ctx.Fail(err, "Gagal mengganti akun aktif.")
	}

	return ctx.Success(fmt.Sprintf("Akun aktif berhasil diubah ke <code>%s</code>.", html.EscapeString(target)))
}

func (p *Plugin) handleDeleteAccount(ctx *core.Context, args []string) error {
	if len(args) == 0 {
		return ctx.Status("Masukkan nomor atau alias akun! Contoh: <code>.myxl del 081912345678</code>")
	}

	target := args[0]
	if norm, err := NormalizeMSISDN(target); err == nil {
		target = norm
	}

	cCtx := getContext(ctx)
	if err := p.repo.Delete(cCtx, target); err != nil {
		return ctx.Fail(err, "Gagal menghapus akun.")
	}

	return ctx.EditOrReply(fmt.Sprintf("🗑️ Akun <code>%s</code> berhasil dihapus dari database.", html.EscapeString(target)))
}

func (p *Plugin) handleKuotaShortcut(ctx *core.Context) error {
	return p.handleShowQuota(ctx, ctx.Args)
}

func (p *Plugin) handleShowQuota(ctx *core.Context, args []string) error {
	var acc *Account
	var err error

	cCtx := getContext(ctx)
	if len(args) > 0 {
		target := args[0]
		if norm, err := NormalizeMSISDN(target); err == nil {
			target = norm
		}
		acc, err = p.repo.GetByMSISDN(cCtx, target)
	} else {
		acc, err = p.repo.GetActive(cCtx)
	}

	if err != nil {
		return ctx.Fail(err, "Error database.")
	}

	if acc == nil {
		return ctx.Status("Tidak ada akun MyXL aktif. Silakan login terlebih dahulu dengan <code>.myxl login &lt;nomor&gt;</code>")
	}

	if acc.IDToken == "" && acc.RefreshToken == "" {
		return ctx.Status(fmt.Sprintf("Akun <code>%s</code> belum terautentikasi. Silakan jalankan <code>.myxl login %s</code>", html.EscapeString(acc.MSISDN), html.EscapeString(acc.MSISDN)))
	}

	maskMSISDN := isGroupChat(ctx.Chat)
	_ = ctx.Progress(fmt.Sprintf("Mengambil data kuota untuk <code>%s</code>...", html.EscapeString(condMask(acc.MSISDN, maskMSISDN))))

	queryCtx, cancel := context.WithTimeout(cCtx, 25*time.Second)
	defer cancel()

	balance, bErr := p.client.GetBalance(queryCtx, acc)
	quota, qErr := p.client.GetQuotaDetails(queryCtx, acc)

	if bErr != nil && qErr != nil {
		return ctx.Fail(errors.Join(bErr, qErr), "Gagal mengambil data pulsa dan kuota MyXL. Silakan coba lagi.")
	}

	respText := FormatQuotaResponse(acc, balance, quota, maskMSISDN)

	// Attach an interactive refresh callback button
	if p.stateStore != nil && ctx.SenderID() > 0 {
		markup := p.buildRefreshMarkup(quotaRefreshState{MSISDN: acc.MSISDN, Masked: maskMSISDN}, callback.StateScope{
			UserID: ctx.SenderID(), ChatID: ctx.ChatID(), Namespace: p.Namespace(),
		})
		if markup != nil {
			chunks := core.SplitTelegramHTML(respText, myxlTelegramMessageRunes)
			if len(chunks) == 1 && ctx.LastResponseID > 0 {
				if err := ctx.Messages().EditMarkup(chunks[0], markup); err == nil {
					return nil
				}
			}
			if err := deliverHTMLWithMarkup(ctx, respText, markup); err == nil {
				return nil
			}
		}
	}

	return deliverHTML(ctx, respText)
}

func condMask(s string, mask bool) string {
	if mask {
		return MaskMSISDN(s)
	}
	return s
}

func (p *Plugin) buildRefreshMarkup(state quotaRefreshState, scope callback.StateScope) tg.ReplyMarkupClass {
	if p.stateStore == nil || scope.UserID <= 0 {
		return nil
	}
	oid := p.stateStore.StoreWithScope(state, scope, myxlCallbackTTL)
	if oid == "" {
		return nil
	}
	row := ui.ButtonRow{
		ui.NewCallbackButton("🔄 Perbarui Kuota", callback.EncodeCallbackData("myxl", "refresh", oid)),
	}
	return render.ToTelegramMarkup(ui.Markup{Rows: []ui.ButtonRow{row}})
}

// HandleCallback handles inline button interactions (e.g. Refresh Kuota).
func (p *Plugin) HandleCallback(cbCtx *callback.CallbackContext) error {
	if cbCtx == nil {
		return nil
	}
	switch cbCtx.Action {
	case "refresh":
		state, ok := cbCtx.State.(quotaRefreshState)
		if !ok || state.MSISDN == "" {
			return cbCtx.Answer("Tombol refresh tidak valid atau sudah kedaluwarsa", true)
		}
		cCtx, cancel := context.WithTimeout(cbCtx.Ctx, 25*time.Second)
		defer cancel()
		acc, err := p.repo.GetByMSISDN(cCtx, state.MSISDN)
		if err != nil || acc == nil {
			return cbCtx.Answer("Akun tidak ditemukan", true)
		}
		balance, balanceErr := p.client.GetBalance(cCtx, acc)
		quota, quotaErr := p.client.GetQuotaDetails(cCtx, acc)
		if balanceErr != nil && quotaErr != nil {
			return cbCtx.Answer(fmt.Sprintf("Gagal update: %v", balanceErr), true)
		}
		text := FormatQuotaResponse(acc, balance, quota, state.Masked)
		markup := p.buildRefreshMarkup(state, callback.StateScope{
			UserID: cbCtx.UserID, ChatID: cbCtx.ChatID, MessageID: cbCtx.Target.MessageID,
			Namespace: p.Namespace(),
		})
		return cbCtx.Edit(text, markup)

	case "buy_confirm":
		state, ok := cbCtx.State.(purchaseDraftState)
		if !ok || state.MSISDN == "" || state.OptionCode == "" || state.TokenConfirmation == "" {
			return cbCtx.Answer("Draft pembelian tidak valid atau sudah kedaluwarsa", true)
		}
		return p.confirmPurchase(cbCtx, state)

	case "buy_cancel":
		_ = cbCtx.Answer("Pembelian dibatalkan", false)
		return cbCtx.Edit("✅ Pembelian dibatalkan.", nil)

	default:
		return cbCtx.Answer("Interaksi menu MyXL lama sudah tidak didukung. Buka ulang MyXL.", true)
	}
}

func (p *Plugin) handleRefreshToken(ctx *core.Context, args []string) error {
	cCtx, cancel := context.WithTimeout(getContext(ctx), 30*time.Second)
	defer cancel()

	var acc *Account
	var err error
	if len(args) > 0 {
		target := args[0]
		if norm, nErr := NormalizeMSISDN(target); nErr == nil {
			target = norm
		}
		acc, err = p.repo.GetByMSISDN(cCtx, target)
	} else {
		acc, err = p.repo.GetActive(cCtx)
	}
	if err != nil {
		return ctx.Fail(err, "Gagal membaca akun.")
	}
	if acc == nil {
		return ctx.Status("Tidak ada akun MyXL yang aktif. Silakan login terlebih dahulu.")
	}

	_ = ctx.Progress(fmt.Sprintf("Me-refresh token CIAM untuk <code>%s</code>...", html.EscapeString(acc.MSISDN)))

	tokens, err := p.client.ForceRefreshToken(cCtx, acc.MSISDN)
	if err != nil {
		return ctx.Fail(err, "Gagal memperbarui sesi MyXL. Silakan coba lagi.")
	}

	return ctx.Success(
		fmt.Sprintf(
			"<b>Token CIAM Berhasil Diperbarui!</b>\n\n"+
				"<b>Akun:</b> <code>%s</code>\n"+
				"<b>Access Token:</b> <code>%s...</code>\n"+
				"<b>ID Token:</b> <code>%s...</code>\n"+
				"<b>Refresh Token:</b> <code>%s...</code>\n"+
				"<b>Waktu:</b> %s",
			html.EscapeString(acc.MSISDN),
			html.EscapeString(truncateString(tokens.AccessToken, 20)),
			html.EscapeString(truncateString(tokens.IDToken, 20)),
			html.EscapeString(truncateString(tokens.RefreshToken, 20)),
			time.Now().Format("2006-01-02 15:04:05 WIB"),
		),
	)
}

func (p *Plugin) handleSearchFamily(ctx *core.Context, args []string) error {
	if len(args) == 0 {
		return ctx.Status("Format salah! Gunakan: <code>.myxl family &lt;family_code&gt;</code>")
	}
	cCtx, cancel := context.WithTimeout(getContext(ctx), 30*time.Second)
	defer cancel()

	acc, err := p.repo.GetActive(cCtx)
	if err != nil || acc == nil {
		return ctx.Status("Tidak ada akun MyXL yang aktif. Silakan login terlebih dahulu.")
	}

	familyCode := strings.TrimSpace(args[0])
	_ = ctx.Progress(fmt.Sprintf("Mencari daftar paket dalam family <code>%s</code>...", html.EscapeString(familyCode)))

	res, err := p.client.GetPackagesByFamily(cCtx, acc, familyCode)
	if err != nil {
		return ctx.Fail(err, "Gagal memuat daftar paket MyXL. Silakan coba lagi.")
	}

	return deliverHTML(ctx, FormatFamilyPackages(res))
}

func (p *Plugin) handlePackageDetail(ctx *core.Context, args []string) error {
	if len(args) == 0 {
		return ctx.Status("Format salah! Gunakan: <code>.myxl paket &lt;option_code&gt;</code>")
	}
	cCtx, cancel := context.WithTimeout(getContext(ctx), 30*time.Second)
	defer cancel()

	acc, err := p.repo.GetActive(cCtx)
	if err != nil || acc == nil {
		return ctx.Status("Tidak ada akun MyXL yang aktif. Silakan login terlebih dahulu.")
	}

	optionCode := strings.TrimSpace(args[0])
	_ = ctx.Progress(fmt.Sprintf("Memuat rincian paket <code>%s</code>...", html.EscapeString(optionCode)))

	details, err := p.client.GetPackageDetails(cCtx, acc, optionCode)
	if err != nil {
		return ctx.Fail(err, "Gagal memuat detail paket MyXL. Silakan coba lagi.")
	}

	return ctx.Result(FormatPackageDetails(details))
}

func (p *Plugin) handleSavedPackages(ctx *core.Context, args []string) error {
	cCtx, cancel := context.WithTimeout(getContext(ctx), 30*time.Second)
	defer cancel()

	acc, err := p.repo.GetActive(cCtx)
	if err != nil || acc == nil {
		return ctx.Status("Tidak ada akun MyXL yang aktif. Silakan login terlebih dahulu.")
	}

	action := "list"
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}

	switch action {
	case "list", "show":
		pkgs, err := p.repo.GetSavedPackages(cCtx, acc.MSISDN)
		if err != nil {
			return ctx.Fail(err, "Gagal membaca bookmark.")
		}
		return deliverHTML(ctx, FormatSavedPackages(pkgs))

	case "add", "save":
		if len(args) < 2 {
			return ctx.Status("Format salah! Gunakan: <code>.myxl saved add &lt;option_code&gt; [nama] [harga]</code>")
		}
		optCode := strings.TrimSpace(args[1])
		_ = ctx.Progress(fmt.Sprintf("Mengambil data paket <code>%s</code>...", html.EscapeString(optCode)))

		pkgName := optCode
		var price int64
		familyCode := ""

		if details, err := p.client.GetPackageDetails(cCtx, acc, optCode); err == nil && details.PackageOption != nil {
			pkgName = details.PackageOption.Name
			price = int64(details.PackageOption.Price)
			familyCode = details.PackageFamily.Name
		}
		if len(args) >= 3 {
			pkgName = args[2]
		}
		if len(args) >= 4 {
			if pr, err := strconv.ParseInt(args[3], 10, 64); err == nil {
				price = pr
			}
		}

		item := &SavedPackage{
			MSISDN:     acc.MSISDN,
			OptionCode: optCode,
			Name:       pkgName,
			Price:      price,
			FamilyCode: familyCode,
		}
		if err := p.repo.SavePackage(cCtx, item); err != nil {
			return ctx.Fail(err, "Gagal menyimpan bookmark.")
		}
		return ctx.Success(fmt.Sprintf("Paket <b>%s</b> (<code>%s</code>) berhasil disimpan ke bookmark!", html.EscapeString(pkgName), html.EscapeString(optCode)))

	case "del", "delete", "rm":
		if len(args) < 2 {
			return ctx.Status("Format salah! Gunakan: <code>.myxl saved del &lt;option_code&gt;</code>")
		}
		optCode := strings.TrimSpace(args[1])
		if err := p.repo.DeleteSavedPackage(cCtx, acc.MSISDN, optCode); err != nil {
			return ctx.Fail(err, "Gagal menghapus bookmark.")
		}
		return ctx.Success(fmt.Sprintf("Paket <code>%s</code> berhasil dihapus dari bookmark.", html.EscapeString(optCode)))

	case "buy", "beli":
		if len(args) < 2 {
			return ctx.Status("Format salah! Gunakan: <code>.myxl saved buy &lt;option_code&gt; [metode] [overwrite_harga] [nomor]</code>")
		}
		return p.handleBuy(ctx, args[1:])

	default:
		pkgs, err := p.repo.GetSavedPackages(cCtx, acc.MSISDN)
		if err != nil {
			return ctx.Fail(err, "Gagal membaca bookmark.")
		}
		return deliverHTML(ctx, FormatSavedPackages(pkgs))
	}
}

func (p *Plugin) handleBuyShortcut(ctx *core.Context) error {
	return p.handleBuy(ctx, ctx.Args)
}

func (p *Plugin) handlePendingQRIS(ctx *core.Context, args []string) error {
	cCtx, cancel := context.WithTimeout(getContext(ctx), 15*time.Second)
	defer cancel()

	acc, err := p.repo.GetActive(cCtx)
	if err != nil || acc == nil {
		return ctx.Status("Tidak ada akun MyXL yang aktif. Login terlebih dahulu.")
	}

	if len(args) > 0 {
		sub := strings.ToLower(strings.TrimSpace(args[0]))
		if sub == "cancel" || sub == "batal" || sub == "del" {
			pending, err := p.repo.GetPendingQRIS(cCtx, acc.MSISDN)
			if err != nil || pending == nil {
				return ctx.Status("Tidak ada transaksi QRIS aktif yang dapat dibatalkan.")
			}
			if err := p.repo.DeletePendingQRIS(cCtx, pending.TransactionCode); err != nil {
				return ctx.Fail(err, "Gagal membatalkan transaksi QRIS.")
			}
			return ctx.Success("Transaksi QRIS berhasil dibatalkan dan dihapus dari penyimpanan.")
		}
	}

	pending, err := p.repo.GetPendingQRIS(cCtx, acc.MSISDN)
	if err != nil {
		return ctx.Fail(err, "Gagal memeriksa transaksi QRIS.")
	}
	if pending == nil {
		return ctx.Status("Tidak ada transaksi QRIS aktif yang menunggu pembayaran.\nTransaksi QRIS otomatis kedaluwarsa setelah 5 menit.")
	}

	remaining := time.Until(pending.ExpiresAt)
	if remaining <= 0 {
		return ctx.Status("Transaksi QRIS ini sudah kedaluwarsa (lebih dari 5 menit). Silakan lakukan pemesanan ulang.")
	}

	var sb strings.Builder
	sb.WriteString("📱 <b>TRANSAKSI QRIS AKTIF</b>\n\n")
	displayMSISDN := pending.MSISDN
	if isGroupChat(ctx.Chat) {
		displayMSISDN = MaskMSISDN(displayMSISDN)
	}
	sb.WriteString(fmt.Sprintf("• <b>Nomor:</b> <code>%s</code>\n", html.EscapeString(displayMSISDN)))
	sb.WriteString(fmt.Sprintf("• <b>Paket:</b> %s\n", html.EscapeString(pending.PackageName)))
	sb.WriteString(fmt.Sprintf("• <b>Total Bayar:</b> Rp %s\n", formatRupiah(pending.Price)))
	if pending.TransactionCode != "" {
		sb.WriteString(fmt.Sprintf("• <b>ID Transaksi:</b> <code>%s</code>\n", html.EscapeString(pending.TransactionCode)))
	}
	sb.WriteString(fmt.Sprintf("• <b>Batas Waktu:</b> %s (sisa <b>%s</b>)\n\n", FormatWIBClock(pending.ExpiresAt), FormatRemainingDuration(remaining)))
	qrPayload, qrErr := normalizeQRPayload(pending.QRCode)
	if qrErr != nil {
		sb.WriteString("⚠️ <i>Payload QRIS tersimpan tidak valid sehingga tidak dapat ditampilkan atau dibuat menjadi gambar.</i>")
	} else {
		preview, truncated := inlineQRPreview(qrPayload)
		sb.WriteString("<b>Kode QRIS:</b>\n")
		sb.WriteString(fmt.Sprintf("<code>%s</code>\n\n", html.EscapeString(preview)))
		if truncated {
			sb.WriteString("💡 <i>String dipersingkat agar aman untuk Telegram; gunakan gambar QR untuk pembayaran. Ketik <code>.myxl qris cancel</code> untuk membatalkan.</i>")
		} else {
			sb.WriteString("💡 <i>Salin string QRIS di atas atau scan gambar QR yang dikirimkan. QRIS hanya berlaku 5 menit. Ketik <code>.myxl qris cancel</code> untuk membatalkan.</i>")
		}
	}

	if err := deliverHTML(ctx, sb.String()); err != nil {
		return err
	}

	if qrErr == nil && p.files != nil && p.tasks != nil && ctx.Svc != nil && ctx.PeerID != nil {
		if err := p.sendQRPhoto(ctx.Ctx, ctx.Svc, ctx.PeerID, qrPayload, pending.PackageName, pending.Price); err != nil {
			_ = ctx.Reply("⚠️ Detail QRIS tersedia, tetapi gambar QR gagal dikirim. Gunakan string QRIS di pesan sebelumnya.")
		}
	}
	return nil
}

func (p *Plugin) handleBuy(ctx *core.Context, args []string) error {
	if len(args) == 0 {
		return ctx.EditOrReply(
			"⚠️ Format salah! Gunakan:\n" +
				"<code>.myxl buy &lt;option_code&gt; [metode] [overwrite_harga] [nomor_ewallet]</code>\n\n" +
				"<b>Pilihan metode:</b>\n" +
				"• <code>pulsa</code> / <code>balance</code> (default)\n" +
				"• <code>qris</code>\n" +
				"• <code>gopay</code> / <code>ovo</code> / <code>dana</code> / <code>shopeepay</code>\n" +
				"• <code>decoy_balance</code> / <code>decoy_qris</code> / <code>decoy_qris0</code>\n\n" +
				"<b>Contoh:</b>\n" +
				"• <code>.myxl buy OPT12345 pulsa 0</code> (beli pulsa rewrite Rp 0)\n" +
				"• <code>.myxl buy OPT12345 qris 1000</code>\n" +
				"• <code>.myxl buy OPT12345 decoy_balance 0</code>\n" +
				"• <code>.myxl buy OPT12345 dana 0 0812345678</code>",
		)
	}

	cCtx, cancel := context.WithTimeout(getContext(ctx), 45*time.Second)
	defer cancel()

	acc, err := p.repo.GetActive(cCtx)
	if err != nil || acc == nil {
		return ctx.Status("Tidak ada akun MyXL yang aktif. Login terlebih dahulu.")
	}

	optionCode := strings.TrimSpace(args[0])

	method := "balance"
	var overwritePrice *int64
	walletNumber := acc.MSISDN

	for _, a := range args[1:] {
		low := strings.ToLower(strings.TrimSpace(a))
		if low == "pulsa" || low == "balance" || low == "qris" || low == "qris0" ||
			low == "gopay" || low == "ovo" || low == "dana" || low == "shopeepay" ||
			low == "decoy_balance" || low == "decoy-balance" || low == "decoy-pulsa" || low == "decoy_pulsa" ||
			low == "decoy_qris" || low == "decoy-qris" || low == "decoy_qris0" || low == "decoy-qris0" {
			if strings.Contains(low, "pulsa") {
				low = strings.ReplaceAll(low, "pulsa", "balance")
			}
			low = strings.ReplaceAll(low, "-", "_")
			method = low
			continue
		}

		if strings.HasPrefix(low, "08") || strings.HasPrefix(low, "628") || strings.HasPrefix(low, "+62") {
			if clean, err := NormalizeMSISDN(low); err == nil {
				walletNumber = clean
				continue
			}
		}

		if val, err := strconv.ParseInt(low, 10, 64); err == nil {
			if val < 0 {
				return ctx.Error("Nominal overwrite tidak boleh negatif.")
			}
			overwritePrice = &val
			continue
		}
	}

	_ = ctx.Progress(fmt.Sprintf("Menyiapkan pembelian paket <code>%s</code> (metode: <code>%s</code>)...", html.EscapeString(optionCode), html.EscapeString(method)))

	// Fetch package details to get confirmation token and real price
	details, err := p.client.GetPackageDetails(cCtx, acc, optionCode)
	if err != nil {
		return ctx.Fail(err, "Gagal memuat detail paket MyXL. Silakan coba lagi.")
	}
	if details.TokenConfirmation == "" {
		return ctx.Error("Token konfirmasi paket tidak ditemukan.")
	}

	pkgName := optionCode
	var price int64
	if details.PackageOption != nil {
		pkgName = details.PackageOption.Name
		price = int64(details.PackageOption.Price)
	}
	targetItem := PurchaseItem{
		ItemCode:          optionCode,
		ItemPrice:         price,
		ItemName:          pkgName,
		TokenConfirmation: details.TokenConfirmation,
	}

	effectivePrice := price
	if overwritePrice != nil {
		effectivePrice = *overwritePrice
	}

	if p.stateStore == nil || ctx.SenderID() <= 0 {
		return ctx.Error("Konfirmasi pembelian tidak tersedia pada sesi ini. Transaksi tidak dijalankan.")
	}

	draft := purchaseDraftState{
		MSISDN: acc.MSISDN, OptionCode: optionCode, PackageName: pkgName, Price: price,
		TokenConfirmation: targetItem.TokenConfirmation, Method: method, WalletNumber: walletNumber,
		HasOverwrite: overwritePrice != nil,
	}
	if overwritePrice != nil {
		draft.OverwritePrice = *overwritePrice
	}
	oid := p.stateStore.StoreWithScope(draft, callback.StateScope{
		UserID: ctx.SenderID(), ChatID: ctx.ChatID(), Namespace: p.Namespace(), SingleUse: true,
	}, 5*time.Minute)
	if oid == "" {
		return ctx.Error("Gagal membuat sesi konfirmasi. Transaksi tidak dijalankan.")
	}

	preview := fmt.Sprintf(
		"⚠️ <b>Konfirmasi Pembelian MyXL</b>\n\n<b>Paket:</b> %s\n<b>Kode:</b> <code>%s</code>\n<b>Metode:</b> <code>%s</code>\n<b>Nominal:</b> Rp %s\n\nTekan <b>Konfirmasi</b> untuk menjalankan transaksi satu kali.",
		html.EscapeString(pkgName), html.EscapeString(optionCode), html.EscapeString(strings.ToUpper(method)), formatRupiah(effectivePrice),
	)
	markup := render.ToTelegramMarkup(ui.Markup{Rows: []ui.ButtonRow{{
		ui.NewCallbackButton("✅ Konfirmasi", callback.EncodeCallbackData("myxl", "buy_confirm", oid)),
		ui.NewCallbackButton("❌ Batal", callback.EncodeCallbackData("myxl", "buy_cancel", oid)),
	}}})
	return ctx.Messages().EditMarkup(preview, markup)
}

func (p *Plugin) confirmPurchase(cbCtx *callback.CallbackContext, draft purchaseDraftState) error {
	cCtx, cancel := context.WithTimeout(cbCtx.Ctx, 45*time.Second)
	defer cancel()

	acc, err := p.repo.GetByMSISDN(cCtx, draft.MSISDN)
	if err != nil || acc == nil {
		return cbCtx.Edit("❌ Akun untuk draft pembelian tidak ditemukan.", nil)
	}

	tokenKey := draft.TokenConfirmation
	if tokenKey == "" {
		tokenKey = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	tokenHash := sha256.Sum256([]byte(tokenKey))
	key := fmt.Sprintf("%s:%s:%x", draft.MSISDN, draft.OptionCode, tokenHash[:16])

	reserved, err := p.repo.ReservePurchase(cCtx, key, draft.MSISDN, draft.OptionCode, draft.Method)
	if err != nil {
		return cbCtx.Edit("❌ Gagal mengamankan transaksi. Pembelian tidak dijalankan.", nil)
	}
	if !reserved {
		return cbCtx.Edit("⏳ Transaksi sedang diproses atau baru saja dikonfirmasi. Mohon tunggu sejenak untuk mencegah saldo/pulsa terpotong dua kali.", nil)
	}

	item := PurchaseItem{
		ItemCode: draft.OptionCode, ItemPrice: draft.Price, ItemName: draft.PackageName,
		TokenConfirmation: draft.TokenConfirmation,
	}
	var overwrite *int64
	if draft.HasOverwrite {
		overwrite = &draft.OverwritePrice
	}

	var result *SettlementResult
	switch draft.Method {
	case "balance":
		result, err = p.client.SettlementBalance(cCtx, acc, item, overwrite)
	case "qris":
		result, err = p.client.SettlementQRIS(cCtx, acc, item, overwrite)
	case "gopay", "ovo", "dana", "shopeepay":
		result, err = p.client.SettlementMultipayment(cCtx, acc, item, strings.ToUpper(draft.Method), draft.WalletNumber, overwrite)
	case "decoy_balance":
		result, err = p.client.SettlementDecoy(cCtx, acc, item, "balance", overwrite)
	case "decoy_qris":
		result, err = p.client.SettlementDecoy(cCtx, acc, item, "qris", overwrite)
	case "decoy_qris0":
		result, err = p.client.SettlementDecoy(cCtx, acc, item, "qris0", overwrite)
	default:
		err = fmt.Errorf("unsupported payment method %q", draft.Method)
	}

	if err != nil {
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(cCtx), 5*time.Second)
		_ = p.repo.FinishPurchase(persistCtx, key, "UNKNOWN", "", err.Error())
		persistCancel()
		return cbCtx.Edit("⚠️ Hasil transaksi tidak dapat dipastikan. Transaksi tidak akan diulang otomatis; periksa riwayat MyXL sebelum mencoba lagi.", nil)
	}
	if result == nil {
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(cCtx), 5*time.Second)
		_ = p.repo.FinishPurchase(persistCtx, key, "UNKNOWN", "", "empty settlement result")
		persistCancel()
		return cbCtx.Edit("⚠️ Hasil transaksi kosong dan tidak dapat dipastikan. Periksa riwayat MyXL sebelum mencoba lagi.", nil)
	}
	status := "FAILED"
	if result.IsSuccess {
		status = "SUCCESS"
	}
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(cCtx), 5*time.Second)
	finishErr := p.repo.FinishPurchase(persistCtx, key, status, result.TransactionCode, result.Message)
	persistCancel()
	if finishErr != nil {
		return cbCtx.Edit("⚠️ Transaksi selesai tetapi hasilnya gagal dicatat. Periksa riwayat MyXL sebelum mencoba lagi.", nil)
	}

	effectivePrice := draft.Price
	if draft.HasOverwrite {
		effectivePrice = draft.OverwritePrice
	}

	var qrWarning string
	if result.QRCode != "" {
		qrPayload, qrErr := normalizeQRPayload(result.QRCode)
		if qrErr != nil {
			qrWarning = "Payload QRIS dari operator tidak valid; gambar QR tidak dibuat."
			result.QRCode = ""
		} else {
			result.QRCode = qrPayload
			if p.repo != nil {
				now := time.Now().UTC()
				pending := &PendingQRIS{
					TransactionCode: result.TransactionCode,
					IdempotencyKey:  key,
					MSISDN:          draft.MSISDN,
					OptionCode:      draft.OptionCode,
					PackageName:     draft.PackageName,
					Price:           effectivePrice,
					QRCode:          qrPayload,
					Status:          "PENDING",
					CreatedAt:       now,
					ExpiresAt:       now.Add(pendingQRISTTL),
				}
				if err := p.repo.SavePendingQRIS(cCtx, pending); err != nil {
					qrWarning = "QRIS berhasil dibuat tetapi gagal disimpan untuk dilihat kembali."
				}
			}
		}
	}

	resText := FormatPurchaseResult(result, draft.PackageName, effectivePrice, strings.ToUpper(draft.Method))
	if err := cbCtx.Edit(resText, nil); err != nil {
		return err
	}
	if result.QRCode != "" && p.files != nil && p.tasks != nil && cbCtx.Service != nil && cbCtx.Target.Peer != nil {
		if err := p.sendQRPhoto(cbCtx.Ctx, cbCtx.Service, cbCtx.Target.Peer, result.QRCode, draft.PackageName, effectivePrice); err != nil && qrWarning == "" {
			qrWarning = "Transaksi selesai tetapi foto QRIS gagal dikirim."
		}
	}
	if qrWarning != "" {
		_ = cbCtx.Answer(qrWarning, true)
	}
	return nil
}

func (p *Plugin) getFiles() *filesystem.Scope {
	return p.files
}

func (p *Plugin) sendQRPhoto(ctx context.Context, svc core.TelegramServicer, peer tg.InputPeerClass, qrCode, pkgName string, price int64) error {
	if svc == nil || peer == nil || strings.TrimSpace(qrCode) == "" {
		return nil
	}
	if p.tasks == nil {
		return fmt.Errorf("%w: MyXL task runtime unavailable", core.ErrUnavailable)
	}

	qrCode, err := normalizeQRPayload(qrCode)
	if err != nil {
		return err
	}
	pngBytes, err := GenerateQRPNG(qrCode)
	if err != nil {
		return err
	}

	files := p.getFiles()
	if files == nil {
		return fmt.Errorf("%w: MyXL filesystem unavailable", core.ErrUnavailable)
	}

	tmpFile, err := files.CreateTempFile("qris-*.png")
	if err != nil {
		return err
	}
	path := tmpFile.Name()
	defer func() { _ = files.RemoveTempFile(path) }()

	if _, err := tmpFile.Write(pngBytes); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	caption := fmt.Sprintf("📱 <b>QRIS Pembayaran MyXL</b>\n<b>Paket:</b> %s\n<b>Nominal:</b> Rp %s\n<i>Scan atau upload gambar ini dari galeri aplikasi e-wallet / mobile banking.</i>",
		html.EscapeString(pkgName),
		formatRupiah(price),
	)

	var sendErr error
	taskID := tasks.TaskID(fmt.Sprintf("myxl:qr:%d:%d", time.Now().UnixNano(), p.qrTaskSeq.Add(1)))
	ticket, err := p.tasks.Submit(ctx, tasks.WorkSpec{
		ID:               taskID,
		Pool:             tasks.PoolID("general"),
		Class:            tasks.PriorityInteractive,
		ExecutionTimeout: myxlQRSendTimeout,
		Resources:        []tasks.ResourceRequirement{{Name: "media", Amount: 1}},
		Handler: func(taskCtx context.Context) error {
			_, sendErr = svc.SendMedia(taskCtx, peer, "photo", path, caption)
			return sendErr
		},
	})
	if err != nil {
		return fmt.Errorf("submit QR media task: %w", err)
	}
	result, err := ticket.Wait(ctx)
	if err != nil {
		return fmt.Errorf("wait QR media task: %w", err)
	}
	if sendErr != nil {
		return sendErr
	}
	if !result.IsSuccess() {
		message := strings.TrimSpace(result.Failure.Message)
		if message == "" {
			message = fmt.Sprintf("task ended with %s", result.Outcome)
		}
		return fmt.Errorf("QR media task failed: %s", message)
	}
	return nil
}

func truncateString(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// Module registration helpers
func (p *Plugin) Repo() Repository {
	return p.repo
}

func (p *Plugin) SetClient(client *Client) {
	p.client = client
}
