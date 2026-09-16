package myxl

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/callback"
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
	stateStore *callback.StateStore
}

const myxlCallbackTTL = 10 * time.Minute

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
	return &Plugin{
		repo:   repo,
		client: client,
	}
}

// SetStateStore configures the callback state store.
func (p *Plugin) SetStateStore(store *callback.StateStore) {
	p.stateStore = store
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
	return callback.CallbackHandlerOptions{AutoAnswer: true}
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

	if secMgr, err := pctx.Secrets(); err == nil && secMgr != nil && p.client != nil {
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
			if pss, err := secMgr.Get("MYXL_PAYMENT_SIG_SECRET"); err == nil && strings.TrimSpace(pss) != "" {
				cfg.PaymentSigSecret = strings.TrimSpace(pss)
			}
			if ef, err := secMgr.Get("MYXL_ENCRYPTED_FIELD_KEY"); err == nil && strings.TrimSpace(ef) != "" {
				cfg.EncryptedFieldKey = strings.TrimSpace(ef)
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
			Description: "MyXL account manager and utilities",
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

func isGroupChat(chat *core.Chat) bool {
	if chat == nil {
		return false
	}
	return chat.Type == "group" || chat.Type == "supergroup" || chat.Type == "channel"
}

func (p *Plugin) handleMyXL(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		return ctx.EditOrReply(
			"📱 <b>MyXL Plugin Menu</b>\n\n" +
				"• <code>.myxl login &lt;nomor&gt;</code> - Minta kode OTP SMS\n" +
				"• <code>.myxl otp &lt;nomor&gt; &lt;kode&gt;</code> - Masukkan kode OTP dan simpan akun\n" +
				"• <code>.myxl refresh [nomor/alias]</code> - Force refresh token CIAM\n" +
				"• <code>.myxl accounts</code> - Daftar semua akun tersimpan\n" +
				"• <code>.myxl use &lt;nomor/alias&gt;</code> - Ganti akun aktif\n" +
				"• <code>.myxl alias &lt;nomor&gt; &lt;nama_alias&gt;</code> - Berikan nama alias akun\n" +
				"• <code>.myxl status</code> - Cek status akun aktif saat ini\n" +
				"• <code>.myxl del &lt;nomor/alias&gt;</code> - Hapus akun tersimpan\n" +
				"• <code>.myxl kuota</code> - Cek sisa kuota dan pulsa\n" +
				"• <code>.myxl family &lt;family_code&gt;</code> - Cari daftar paket dalam family\n" +
				"• <code>.myxl paket &lt;option_code&gt;</code> - Cek rincian detail paket\n" +
				"• <code>.myxl saved [list|add|del|buy]</code> - Kelola / beli paket tersimpan\n" +
				"• <code>.myxl buy &lt;option_code&gt; [metode] [harga] [nomor]</code> - Beli paket langsung\n" +
				"• <code>.kuota</code> - Shortcut cepat periksa kuota",
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
	default:
		return ctx.EditOrReply(fmt.Sprintf("⚠️ Subcommand <code>%s</code> tidak dikenal. Ketik <code>.myxl</code> untuk bantuan.", html.EscapeString(subCmd)))
	}
}

func (p *Plugin) handleLogin(ctx *core.Context, args []string) error {
	if len(args) == 0 {
		return ctx.EditOrReply("⚠️ Format salah! Gunakan: <code>.myxl login &lt;nomor_hp&gt;</code>\nContoh: <code>.myxl login 081912345678</code>")
	}

	rawMSISDN := args[0]
	msisdn, err := NormalizeMSISDN(rawMSISDN)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Nomor HP tidak valid: %v", err))
	}

	_ = ctx.EditOrReply(fmt.Sprintf("⏳ Mengirim permintaan OTP ke <code>%s</code>...", html.EscapeString(msisdn)))

	cCtx, cancel := context.WithTimeout(getContext(ctx), 20*time.Second)
	defer cancel()

	subID, err := p.client.RequestOTP(cCtx, msisdn)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Gagal meminta OTP:\n<code>%s</code>", html.EscapeString(err.Error())))
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

	return ctx.EditOrReply(
		fmt.Sprintf("✅ <b>Kode OTP telah dikirimkan via SMS</b> ke <code>%s</code>!\n\n"+
			"Segera masukkan kode OTP dengan perintah:\n"+
			"<code>.myxl otp %s &lt;kode_otp&gt;</code>",
			html.EscapeString(msisdn), html.EscapeString(msisdn)),
	)
}

func (p *Plugin) handleOTP(ctx *core.Context, args []string) error {
	if len(args) < 2 {
		return ctx.EditOrReply("⚠️ Format salah! Gunakan: <code>.myxl otp &lt;nomor_hp&gt; &lt;kode_otp&gt;</code>\nContoh: <code>.myxl otp 081912345678 123456</code>")
	}

	rawMSISDN := args[0]
	code := args[1]

	msisdn, err := NormalizeMSISDN(rawMSISDN)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Nomor HP tidak valid: %v", err))
	}

	_ = ctx.EditOrReply(fmt.Sprintf("⏳ Memverifikasi kode OTP untuk <code>%s</code>...", html.EscapeString(msisdn)))

	cCtx, cancel := context.WithTimeout(getContext(ctx), 25*time.Second)
	defer cancel()

	tokens, err := p.client.SubmitOTP(cCtx, msisdn, code)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Verifikasi OTP gagal:\n<code>%s</code>", html.EscapeString(err.Error())))
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
		return ctx.EditOrReply(fmt.Sprintf("⚠️ Login berhasil di CIAM tetapi gagal menyimpan ke database: %v", err))
	}

	_ = p.repo.SetActive(cCtx, msisdn)

	return ctx.EditOrReply(
		fmt.Sprintf("🎉 <b>Login Berhasil!</b>\n\n"+
			"Nomor <code>%s</code> telah tersimpan dan dijadikan sebagai <b>akun aktif</b>.\n\n"+
			"Gunakan <code>.kuota</code> untuk memeriksa sisa kuota dan pulsa Anda.",
			html.EscapeString(msisdn)),
	)
}

func (p *Plugin) handleSetAlias(ctx *core.Context, args []string) error {
	if len(args) < 2 {
		return ctx.EditOrReply("⚠️ Format: <code>.myxl alias &lt;nomor/alias_lama&gt; &lt;alias_baru&gt;</code>\nContoh: <code>.myxl alias 081912345678 Utama</code>")
	}

	target := args[0]
	if norm, err := NormalizeMSISDN(target); err == nil {
		target = norm
	}
	newAlias := strings.TrimSpace(args[1])

	cCtx := getContext(ctx)
	if err := p.repo.SetAlias(cCtx, target, newAlias); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Gagal mengatur alias: %v", err))
	}

	return ctx.EditOrReply(fmt.Sprintf("✅ Alias untuk <code>%s</code> berhasil diatur menjadi <b>%s</b>.", html.EscapeString(target), html.EscapeString(newAlias)))
}

func (p *Plugin) handleStatus(ctx *core.Context) error {
	cCtx := getContext(ctx)
	acc, err := p.repo.GetActive(cCtx)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Error database: %v", err))
	}
	if acc == nil {
		return ctx.EditOrReply("⚠️ Tidak ada akun MyXL aktif saat ini. Gunakan <code>.myxl login &lt;nomor&gt;</code> untuk login.")
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
		return ctx.EditOrReply(fmt.Sprintf("❌ Gagal memuat akun: %v", err))
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

	return ctx.EditOrReply(b.String())
}

func (p *Plugin) handleUseAccount(ctx *core.Context, args []string) error {
	if len(args) == 0 {
		return ctx.EditOrReply("⚠️ Masukkan nomor atau alias akun! Contoh: <code>.myxl use 081912345678</code> atau <code>.myxl use Utama</code>")
	}

	target := args[0]
	if norm, err := NormalizeMSISDN(target); err == nil {
		target = norm
	}

	cCtx := getContext(ctx)
	if err := p.repo.SetActive(cCtx, target); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Gagal mengganti akun aktif: %v", err))
	}

	return ctx.EditOrReply(fmt.Sprintf("✅ Akun aktif berhasil diubah ke <code>%s</code>.", html.EscapeString(target)))
}

func (p *Plugin) handleDeleteAccount(ctx *core.Context, args []string) error {
	if len(args) == 0 {
		return ctx.EditOrReply("⚠️ Masukkan nomor atau alias akun! Contoh: <code>.myxl del 081912345678</code>")
	}

	target := args[0]
	if norm, err := NormalizeMSISDN(target); err == nil {
		target = norm
	}

	cCtx := getContext(ctx)
	if err := p.repo.Delete(cCtx, target); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Gagal menghapus akun: %v", err))
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
		return ctx.EditOrReply(fmt.Sprintf("❌ Error database: %v", err))
	}

	if acc == nil {
		return ctx.EditOrReply("⚠️ Tidak ada akun MyXL aktif. Silakan login terlebih dahulu dengan <code>.myxl login &lt;nomor&gt;</code>")
	}

	if acc.IDToken == "" && acc.RefreshToken == "" {
		return ctx.EditOrReply(fmt.Sprintf("⚠️ Akun <code>%s</code> belum terautentikasi. Silakan jalankan <code>.myxl login %s</code>", html.EscapeString(acc.MSISDN), html.EscapeString(acc.MSISDN)))
	}

	maskMSISDN := isGroupChat(ctx.Chat)
	_ = ctx.EditOrReply(fmt.Sprintf("⏳ Mengambil data kuota untuk <code>%s</code>...", html.EscapeString(condMask(acc.MSISDN, maskMSISDN))))

	queryCtx, cancel := context.WithTimeout(cCtx, 25*time.Second)
	defer cancel()

	balance, bErr := p.client.GetBalance(queryCtx, acc)
	quota, qErr := p.client.GetQuotaDetails(queryCtx, acc)

	if bErr != nil && qErr != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Gagal mengambil data MyXL:\nPulsa: <code>%s</code>\nKuota: <code>%s</code>",
			html.EscapeString(bErr.Error()), html.EscapeString(qErr.Error())))
	}

	respText := FormatQuotaResponse(acc, balance, quota, maskMSISDN)

	// Attach an interactive refresh callback button
	if p.stateStore != nil && ctx.SenderID() > 0 {
		markup := p.buildRefreshMarkup(quotaRefreshState{MSISDN: acc.MSISDN, Masked: maskMSISDN}, callback.StateScope{
			UserID: ctx.SenderID(), ChatID: ctx.ChatID(), Namespace: p.Namespace(),
		})
		if markup == nil {
			return ctx.EditOrReply(respText)
		}
		if err := ctx.Messages().ReplyMarkup(respText, markup); err == nil {
			return nil
		}
	}

	return ctx.EditOrReply(respText)
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
			return cbCtx.Answer("Tombol tidak valid atau sudah kedaluwarsa", true)
		}
		cCtx, cancel := context.WithTimeout(cbCtx.Ctx, 25*time.Second)
		defer cancel()

		acc, err := p.repo.GetByMSISDN(cCtx, state.MSISDN)
		if acc == nil || err != nil {
			return cbCtx.Answer("Akun tidak ditemukan", true)
		}

		balance, bErr := p.client.GetBalance(cCtx, acc)
		quota, qErr := p.client.GetQuotaDetails(cCtx, acc)
		if bErr != nil && qErr != nil {
			return cbCtx.Answer(fmt.Sprintf("Gagal update: %v", bErr), true)
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
		if _, ok := cbCtx.State.(purchaseDraftState); !ok {
			return cbCtx.Answer("Draft pembelian tidak valid atau sudah kedaluwarsa", true)
		}
		return cbCtx.Edit("✅ Pembelian dibatalkan.", nil)

	default:
		return nil
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
		return ctx.EditOrReply(fmt.Sprintf("❌ Gagal membaca akun: %v", err))
	}
	if acc == nil {
		return ctx.EditOrReply("⚠️ Tidak ada akun MyXL yang aktif. Silakan login terlebih dahulu.")
	}

	_ = ctx.EditOrReply(fmt.Sprintf("⏳ Me-refresh token CIAM untuk <code>%s</code>...", html.EscapeString(acc.MSISDN)))

	tokens, err := p.client.ForceRefreshToken(cCtx, acc.MSISDN)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Gagal me-refresh token:\n<code>%s</code>", html.EscapeString(err.Error())))
	}

	return ctx.EditOrReply(
		fmt.Sprintf(
			"<b>✅ Token CIAM Berhasil Diperbarui!</b>\n\n"+
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
		return ctx.EditOrReply("⚠️ Format salah! Gunakan: <code>.myxl family &lt;family_code&gt;</code>")
	}
	cCtx, cancel := context.WithTimeout(getContext(ctx), 30*time.Second)
	defer cancel()

	acc, err := p.repo.GetActive(cCtx)
	if err != nil || acc == nil {
		return ctx.EditOrReply("⚠️ Tidak ada akun MyXL yang aktif. Silakan login terlebih dahulu.")
	}

	familyCode := strings.TrimSpace(args[0])
	_ = ctx.EditOrReply(fmt.Sprintf("⏳ Mencari daftar paket dalam family <code>%s</code>...", html.EscapeString(familyCode)))

	res, err := p.client.GetPackagesByFamily(cCtx, acc, familyCode)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Gagal memuat paket family:\n<code>%s</code>", html.EscapeString(err.Error())))
	}

	return ctx.EditOrReply(FormatFamilyPackages(res))
}

func (p *Plugin) handlePackageDetail(ctx *core.Context, args []string) error {
	if len(args) == 0 {
		return ctx.EditOrReply("⚠️ Format salah! Gunakan: <code>.myxl paket &lt;option_code&gt;</code>")
	}
	cCtx, cancel := context.WithTimeout(getContext(ctx), 30*time.Second)
	defer cancel()

	acc, err := p.repo.GetActive(cCtx)
	if err != nil || acc == nil {
		return ctx.EditOrReply("⚠️ Tidak ada akun MyXL yang aktif. Silakan login terlebih dahulu.")
	}

	optionCode := strings.TrimSpace(args[0])
	_ = ctx.EditOrReply(fmt.Sprintf("⏳ Memuat rincian paket <code>%s</code>...", html.EscapeString(optionCode)))

	details, err := p.client.GetPackageDetails(cCtx, acc, optionCode)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Gagal memuat detail paket:\n<code>%s</code>", html.EscapeString(err.Error())))
	}

	return ctx.EditOrReply(FormatPackageDetails(details))
}

func (p *Plugin) handleSavedPackages(ctx *core.Context, args []string) error {
	cCtx, cancel := context.WithTimeout(getContext(ctx), 30*time.Second)
	defer cancel()

	acc, err := p.repo.GetActive(cCtx)
	if err != nil || acc == nil {
		return ctx.EditOrReply("⚠️ Tidak ada akun MyXL yang aktif. Silakan login terlebih dahulu.")
	}

	action := "list"
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}

	switch action {
	case "list", "show":
		pkgs, err := p.repo.GetSavedPackages(cCtx, acc.MSISDN)
		if err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Gagal membaca bookmark: %v", err))
		}
		return ctx.EditOrReply(FormatSavedPackages(pkgs))

	case "add", "save":
		if len(args) < 2 {
			return ctx.EditOrReply("⚠️ Format salah! Gunakan: <code>.myxl saved add &lt;option_code&gt; [nama] [harga]</code>")
		}
		optCode := strings.TrimSpace(args[1])
		_ = ctx.EditOrReply(fmt.Sprintf("⏳ Mengambil data paket <code>%s</code>...", html.EscapeString(optCode)))

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
			return ctx.EditOrReply(fmt.Sprintf("❌ Gagal menyimpan bookmark: %v", err))
		}
		return ctx.EditOrReply(fmt.Sprintf("✅ Paket <b>%s</b> (<code>%s</code>) berhasil disimpan ke bookmark!", html.EscapeString(pkgName), html.EscapeString(optCode)))

	case "del", "delete", "rm":
		if len(args) < 2 {
			return ctx.EditOrReply("⚠️ Format salah! Gunakan: <code>.myxl saved del &lt;option_code&gt;</code>")
		}
		optCode := strings.TrimSpace(args[1])
		if err := p.repo.DeleteSavedPackage(cCtx, acc.MSISDN, optCode); err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Gagal menghapus bookmark: %v", err))
		}
		return ctx.EditOrReply(fmt.Sprintf("✅ Paket <code>%s</code> berhasil dihapus dari bookmark.", html.EscapeString(optCode)))

	case "buy", "beli":
		if len(args) < 2 {
			return ctx.EditOrReply("⚠️ Format salah! Gunakan: <code>.myxl saved buy &lt;option_code&gt; [metode] [overwrite_harga] [nomor]</code>")
		}
		return p.handleBuy(ctx, args[1:])

	default:
		pkgs, err := p.repo.GetSavedPackages(cCtx, acc.MSISDN)
		if err != nil {
			return ctx.EditOrReply(fmt.Sprintf("❌ Gagal membaca bookmark: %v", err))
		}
		return ctx.EditOrReply(FormatSavedPackages(pkgs))
	}
}

func (p *Plugin) handleBuyShortcut(ctx *core.Context) error {
	return p.handleBuy(ctx, ctx.Args)
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
		return ctx.EditOrReply("⚠️ Tidak ada akun MyXL yang aktif. Login terlebih dahulu.")
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
				return ctx.EditOrReply("❌ Nominal overwrite tidak boleh negatif.")
			}
			overwritePrice = &val
			continue
		}
	}

	_ = ctx.EditOrReply(fmt.Sprintf("⏳ Menyiapkan pembelian paket <code>%s</code> (metode: <code>%s</code>)...", html.EscapeString(optionCode), html.EscapeString(method)))

	// Fetch package details to get confirmation token and real price
	details, err := p.client.GetPackageDetails(cCtx, acc, optionCode)
	if err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Gagal memuat detail paket:\n<code>%s</code>", html.EscapeString(err.Error())))
	}
	if details.TokenConfirmation == "" {
		return ctx.EditOrReply("❌ Token konfirmasi paket tidak ditemukan.")
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
		return ctx.EditOrReply("❌ Konfirmasi pembelian tidak tersedia pada sesi ini. Transaksi tidak dijalankan.")
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
		return ctx.EditOrReply("❌ Gagal membuat sesi konfirmasi. Transaksi tidak dijalankan.")
	}

	preview := fmt.Sprintf(
		"⚠️ <b>Konfirmasi Pembelian MyXL</b>\n\n<b>Paket:</b> %s\n<b>Kode:</b> <code>%s</code>\n<b>Metode:</b> <code>%s</code>\n<b>Nominal:</b> Rp %s\n\nTekan <b>Konfirmasi</b> untuk menjalankan transaksi satu kali.",
		html.EscapeString(pkgName), html.EscapeString(optionCode), html.EscapeString(strings.ToUpper(method)), formatRupiah(effectivePrice),
	)
	markup := render.ToTelegramMarkup(ui.Markup{Rows: []ui.ButtonRow{{
		ui.NewCallbackButton("✅ Konfirmasi", callback.EncodeCallbackData("myxl", "buy_confirm", oid)),
		ui.NewCallbackButton("❌ Batal", callback.EncodeCallbackData("myxl", "buy_cancel", oid)),
	}}})
	return ctx.Messages().ReplyMarkup(preview, markup)
}

func (p *Plugin) confirmPurchase(cbCtx *callback.CallbackContext, draft purchaseDraftState) error {
	cCtx, cancel := context.WithTimeout(cbCtx.Ctx, 45*time.Second)
	defer cancel()

	acc, err := p.repo.GetByMSISDN(cCtx, draft.MSISDN)
	if err != nil || acc == nil {
		return cbCtx.Edit("❌ Akun untuk draft pembelian tidak ditemukan.", nil)
	}

	key := fmt.Sprintf("%s:%s:%s", draft.MSISDN, draft.OptionCode, time.Now().UTC().Format("2006-01-02"))
	reserved, err := p.repo.ReservePurchase(cCtx, key, draft.MSISDN, draft.OptionCode, draft.Method)
	if err != nil {
		return cbCtx.Edit("❌ Gagal mengamankan transaksi. Pembelian tidak dijalankan.", nil)
	}
	if !reserved {
		return cbCtx.Edit("⏳ Pembelian paket ini sudah dikonfirmasi hari ini dan tidak dijalankan ulang.", nil)
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
	return cbCtx.Edit(FormatPurchaseResult(result, draft.PackageName, effectivePrice, strings.ToUpper(draft.Method)), nil)
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Module registration helpers
func (p *Plugin) Repo() Repository {
	return p.repo
}

func (p *Plugin) SetClient(client *Client) {
	p.client = client
}
