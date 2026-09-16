package myxl

import (
	"context"
	"fmt"
	"html"
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
			Usage:       ".myxl [login|otp|accounts|use|alias|status|del|kuota]",
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
				"• <code>.myxl login &lt;nomor&gt;</code> - Minta kode OTP SMS (PM saja)\n" +
				"• <code>.myxl otp &lt;nomor&gt; &lt;kode&gt;</code> - Masukkan kode OTP dan simpan akun (PM saja)\n" +
				"• <code>.myxl accounts</code> - Daftar semua akun tersimpan\n" +
				"• <code>.myxl use &lt;nomor/alias&gt;</code> - Ganti akun aktif\n" +
				"• <code>.myxl alias &lt;nomor&gt; &lt;nama_alias&gt;</code> - Berikan nama alias akun\n" +
				"• <code>.myxl status</code> - Cek status akun aktif saat ini\n" +
				"• <code>.myxl del &lt;nomor/alias&gt;</code> - Hapus akun tersimpan\n" +
				"• <code>.myxl kuota</code> - Cek sisa kuota dan pulsa\n" +
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
	default:
		return ctx.EditOrReply(fmt.Sprintf("⚠️ Subcommand <code>%s</code> tidak dikenal. Ketik <code>.myxl</code> untuk bantuan.", html.EscapeString(subCmd)))
	}
}

func (p *Plugin) handleLogin(ctx *core.Context, args []string) error {
	if isGroupChat(ctx.Chat) {
		return ctx.EditOrReply("⚠️ <b>Perhatian Keamanan:</b> Perintah login hanya dapat dilakukan di <b>Private Message (PM)</b> untuk melindungi kerahasiaan nomor Anda.")
	}

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
	if isGroupChat(ctx.Chat) {
		return ctx.EditOrReply("⚠️ <b>Perhatian Keamanan:</b> Verifikasi kode OTP hanya boleh dilakukan di <b>Private Message (PM)</b> demi keamanan akun Anda.")
	}

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

	// In private chats, attach an interactive refresh callback button
	if !isGroupChat(ctx.Chat) && p.stateStore != nil {
		markup := buildRefreshMarkup(acc.MSISDN)
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

func buildRefreshMarkup(msisdn string) tg.ReplyMarkupClass {
	row := ui.ButtonRow{
		ui.NewCallbackButton("🔄 Perbarui Kuota", callback.EncodeCallbackData("myxl", "refresh", msisdn)),
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
		targetMSISDN := cbCtx.OpaqueID
		cCtx, cancel := context.WithTimeout(cbCtx.Ctx, 25*time.Second)
		defer cancel()

		var acc *Account
		var err error
		if targetMSISDN != "" && targetMSISDN != callback.ActionNoop {
			acc, err = p.repo.GetByMSISDN(cCtx, targetMSISDN)
		}
		if acc == nil || err != nil {
			acc, err = p.repo.GetActive(cCtx)
		}
		if acc == nil || err != nil {
			return cbCtx.Answer("Akun tidak ditemukan", true)
		}

		balance, bErr := p.client.GetBalance(cCtx, acc)
		quota, qErr := p.client.GetQuotaDetails(cCtx, acc)
		if bErr != nil && qErr != nil {
			return cbCtx.Answer(fmt.Sprintf("Gagal update: %v", bErr), true)
		}

		text := FormatQuotaResponse(acc, balance, quota, false)
		markup := buildRefreshMarkup(acc.MSISDN)
		return cbCtx.Edit(text, markup)

	default:
		return nil
	}
}

// Module registration helpers
func (p *Plugin) Repo() Repository {
	return p.repo
}

func (p *Plugin) SetClient(client *Client) {
	p.client = client
}
