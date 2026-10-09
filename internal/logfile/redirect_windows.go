package logfile

import "os"

// RedirectStdio is a no-op on Windows: no service manager here hands the
// loop a log file, and the File itself still receives every event.
func RedirectStdio(*os.File) error { return nil }
