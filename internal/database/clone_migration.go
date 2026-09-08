package database

func init() {
	migrations = append(migrations, migration{
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
	})
}
