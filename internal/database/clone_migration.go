package database

func init() {
	migrations = append(migrations,
		migration{
			version:     15,
			description: "Persistent clone profile snapshots",
			statements: []string{
				`CREATE TABLE IF NOT EXISTS clone_state (
					owner_id INTEGER PRIMARY KEY,
					original_first_name TEXT NOT NULL DEFAULT '',
					original_last_name TEXT NOT NULL DEFAULT '',
					original_bio TEXT NOT NULL DEFAULT '',
					original_photo_path TEXT NOT NULL DEFAULT '',
					active BOOLEAN NOT NULL DEFAULT 0,
					updated_at DATETIME NOT NULL
				);`,
			},
			legacyChecksums: []string{
				"f23db64a47cbd211704582fac2557e3ad87386c35a61589080fac2f699989182",
				"11a72db9e774e1043891bc77806785699c662e84f04aa1dbd8ba57b5f7c8c0da",
			},
		},
		migration{
			version:     16,
			description: "Clone snapshot photo mutation tracking",
			statements: []string{
				`ALTER TABLE clone_state ADD COLUMN cloned_photo BOOLEAN NOT NULL DEFAULT 0;`,
			},
		},
	)
}
