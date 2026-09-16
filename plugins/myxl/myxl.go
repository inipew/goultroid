package myxl

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
)

// Plugin provides MyXL account management and quota viewing commands.
type Plugin struct {
	repo   Repository
	client *Client
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

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return "myxl"
}

// Description returns a summary of the plugin functionality.
func (p *Plugin) Description() string {
	return "MyXL account management and real-time quota visualizer"
}

// Init initializes the plugin.
func (p *Plugin) Init() error {
	return nil
}

// InitPlugin initializes capability-gated platform accessors.
func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error {
	netSvc, err := pctx.HTTP()
	if err != nil {
		return err
	}
	if p.client != nil {
		p.client.SetHTTP(netSvc)
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
			Usage:       ".myxl [login|otp|accounts|use|del|kuota]",
			Category:    "Utility",
			Permission:  core.PermissionOwner,
			Handler:     p.handleMyXL,
		},
		{
			Name:        "kuota",
			Aliases:     []string{"myquota", "xl"},
			Description: "Check balance and active quota for MyXL",
			Usage:       ".kuota [msisdn/alias]",
			Category:    "Utility",
			Permission:  core.PermissionOwner,
			Handler:     p.handleKuotaShortcut,
		},
	}
}

func (p *Plugin) handleMyXL(ctx *core.Context) error {
	if len(ctx.Args) == 0 {
		return ctx.EditOrReply(
			"📱 <b>MyXL Plugin Menu</b>\n\n" +
				"• <code>.myxl login &lt;nomor&gt;</code> - Minta kode OTP SMS\n" +
				"• <code>.myxl otp &lt;nomor&gt; &lt;kode&gt;</code> - Masukkan kode OTP dan login\n" +
				"• <code>.myxl accounts</code> - Daftar semua akun tersimpan\n" +
				"• <code>.myxl use &lt;nomor/alias&gt;</code> - Ganti akun aktif\n" +
				"• <code>.myxl del &lt;nomor/alias&gt;</code> - Hapus akun tersimpan\n" +
				"• <code>.myxl kuota</code> - Cek sisa kuota dan pulsa\n" +
				"• <code>.kuota</code> - Shortcut cepat cek kuota",
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
	case "del", "delete", "rm":
		return p.handleDeleteAccount(ctx, args)
	case "kuota", "quota", "balance":
		return p.handleShowQuota(ctx, args)
	default:
		return ctx.EditOrReply(fmt.Sprintf("⚠️ Subcommand <code>%s</code> tidak dikenal. Ketik <code>.myxl</code> untuk bantuan.", html.EscapeString(subCmd)))
	}
}

func getContext(ctx *core.Context) context.Context {
	if ctx != nil && ctx.Ctx != nil {
		return ctx.Ctx
	}
	return context.Background()
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

	cCtx := getContext(ctx)
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

	cCtx := getContext(ctx)
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
			"Nomor <code>%s</code> telah berhasil login dan dijadikan sebagai <b>akun aktif</b>.\n\n"+
			"Ketik <code>.kuota</code> untuk memeriksa sisa kuota dan pulsa Anda.",
			html.EscapeString(msisdn)),
	)
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

	var b strings.Builder
	b.WriteString("📱 <b>Daftar Akun MyXL Tersimpan:</b>\n\n")

	for i, acc := range accounts {
		status := "▫️"
		if acc.IsActive {
			status = "🟢 [AKTIF]"
		}

		b.WriteString(fmt.Sprintf("%d. %s <code>%s</code>", i+1, status, html.EscapeString(acc.MSISDN)))
		if acc.Alias != "" {
			b.WriteString(fmt.Sprintf(" (<i>%s</i>)", html.EscapeString(acc.Alias)))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n💡 <i>Ganti akun aktif:</i> <code>.myxl use &lt;nomor&gt;</code>\n")
	b.WriteString("🗑️ <i>Hapus akun:</i> <code>.myxl del &lt;nomor&gt;</code>")

	return ctx.EditOrReply(b.String())
}

func (p *Plugin) handleUseAccount(ctx *core.Context, args []string) error {
	if len(args) == 0 {
		return ctx.EditOrReply("⚠️ Masukkan nomor atau alias akun! Contoh: <code>.myxl use 081912345678</code>")
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

	_ = ctx.EditOrReply(fmt.Sprintf("⏳ Mengambil data kuota untuk <code>%s</code>...", html.EscapeString(acc.MSISDN)))

	balance, bErr := p.client.GetBalance(cCtx, acc)
	quota, qErr := p.client.GetQuotaDetails(cCtx, acc)

	if bErr != nil && qErr != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Gagal mengambil data MyXL:\nPulsa: <code>%s</code>\nKuota: <code>%s</code>",
			html.EscapeString(bErr.Error()), html.EscapeString(qErr.Error())))
	}

	resp := FormatQuotaResponse(acc, balance, quota)
	return ctx.EditOrReply(resp)
}

// Module registration helpers
func (p *Plugin) Repo() Repository {
	return p.repo
}

func (p *Plugin) SetClient(client *Client) {
	p.client = client
}
