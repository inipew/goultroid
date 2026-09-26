package localization

func (s *Service) loadUserbotUXTranslations() {
	s.AddTranslations(LocaleEnglish, map[string]string{
		"settings.cli.usage": "⚙️ <b>GoUltroid CLI Configuration Subsystem</b>\n\n<b>Usage:</b>\n• <code>.config get &lt;namespace:key&gt;</code>\n• <code>.config set &lt;namespace:key&gt; &lt;value&gt;</code>\n• <code>.config reset &lt;namespace:key&gt;</code>\n• <code>.config list [category]</code>\n• <code>.config history &lt;namespace:key&gt;</code>\n• <code>.config export</code>\n",
		"settings.cli.usage_get": "Usage: <code>.config get &lt;namespace:key&gt;</code>",
		"settings.cli.usage_set": "Usage: <code>.config set &lt;namespace:key&gt; &lt;value&gt;</code>",
		"settings.cli.usage_reset": "Usage: <code>.config reset &lt;namespace:key&gt;</code>",
		"settings.cli.usage_history": "Usage: <code>.config history &lt;namespace:key&gt;</code>",
		"settings.cli.value": "⚙️ <b>%s:%s</b> = <code>%s</code>",
		"settings.cli.set_failed": "Failed to set <b>%s:%s</b>: %s",
		"settings.cli.updated": "Setting updated:\n<code>%s:%s</code> = <code>%s</code>",
		"settings.cli.reset_failed": "Failed to reset <b>%s:%s</b>: %s",
		"settings.cli.reset_done": "Setting <code>%s:%s</code> reset to default.",
		"settings.cli.none": "No settings found.",
		"settings.cli.schema_title": "📋 <b>GoUltroid Configuration Schema</b>",
		"settings.cli.schema_item": "• <code>%s:%s</code> = <code>%s</code> (default: <code>%s</code>) [%s]\n  <i>%s</i>\n",
		"settings.cli.history_failed": "Error retrieving history: %s",
		"settings.cli.history_none": "No change history found for <code>%s:%s</code>.",
		"settings.cli.history_title": "📜 <b>Change History for</b> <code>%s:%s</code>",
		"settings.cli.history_item": "• <code>%s</code> ➔ <code>%s</code> by user <code>%d</code> at <code>%s</code>\n",
		"settings.cli.export_failed": "Export failed: %s",
		"settings.cli.export_result": "📤 <b>Global Settings Export:</b>\n<pre><code class=\"language-json\">%s</code></pre>",
		"settings.cli.unknown_action": "Unknown action <code>%s</code>. Use <code>.config</code> to see available commands.",
	})

	s.AddTranslations(LocaleIndonesian, map[string]string{
		"settings.cli.usage": "⚙️ <b>Subsistem Konfigurasi CLI GoUltroid</b>\n\n<b>Penggunaan:</b>\n• <code>.config get &lt;namespace:key&gt;</code>\n• <code>.config set &lt;namespace:key&gt; &lt;value&gt;</code>\n• <code>.config reset &lt;namespace:key&gt;</code>\n• <code>.config list [category]</code>\n• <code>.config history &lt;namespace:key&gt;</code>\n• <code>.config export</code>\n",
		"settings.cli.usage_get": "Penggunaan: <code>.config get &lt;namespace:key&gt;</code>",
		"settings.cli.usage_set": "Penggunaan: <code>.config set &lt;namespace:key&gt; &lt;value&gt;</code>",
		"settings.cli.usage_reset": "Penggunaan: <code>.config reset &lt;namespace:key&gt;</code>",
		"settings.cli.usage_history": "Penggunaan: <code>.config history &lt;namespace:key&gt;</code>",
		"settings.cli.value": "⚙️ <b>%s:%s</b> = <code>%s</code>",
		"settings.cli.set_failed": "Gagal mengatur <b>%s:%s</b>: %s",
		"settings.cli.updated": "Pengaturan diperbarui:\n<code>%s:%s</code> = <code>%s</code>",
		"settings.cli.reset_failed": "Gagal mereset <b>%s:%s</b>: %s",
		"settings.cli.reset_done": "Pengaturan <code>%s:%s</code> dikembalikan ke nilai default.",
		"settings.cli.none": "Tidak ada pengaturan yang ditemukan.",
		"settings.cli.schema_title": "📋 <b>Skema Konfigurasi GoUltroid</b>",
		"settings.cli.schema_item": "• <code>%s:%s</code> = <code>%s</code> (default: <code>%s</code>) [%s]\n  <i>%s</i>\n",
		"settings.cli.history_failed": "Gagal mengambil riwayat: %s",
		"settings.cli.history_none": "Tidak ada riwayat perubahan untuk <code>%s:%s</code>.",
		"settings.cli.history_title": "📜 <b>Riwayat Perubahan untuk</b> <code>%s:%s</code>",
		"settings.cli.history_item": "• <code>%s</code> ➔ <code>%s</code> oleh pengguna <code>%d</code> pada <code>%s</code>\n",
		"settings.cli.export_failed": "Ekspor gagal: %s",
		"settings.cli.export_result": "📤 <b>Ekspor Pengaturan Global:</b>\n<pre><code class=\"language-json\">%s</code></pre>",
		"settings.cli.unknown_action": "Aksi <code>%s</code> tidak dikenal. Gunakan <code>.config</code> untuk melihat perintah yang tersedia.",
	})
}
