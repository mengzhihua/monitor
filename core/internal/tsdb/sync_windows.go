package tsdb

// Windows does not expose directory fsync through os.File.Sync.
// Block contents are synced before rename; directory durability is OS-managed.
func syncBlockDir(string) error { return nil }
