package appstore

// Phase identifies a stage of a download.
type Phase string

const (
	PhaseResolving   Phase = "resolving"
	PhasePurchasing  Phase = "purchasing"
	PhaseDownloading Phase = "downloading"
	PhaseDecrypting  Phase = "decrypting"
	PhasePatching    Phase = "patching"
)

// Progress receives updates about a download. A nil Progress disables reporting.
type Progress interface {
	// Phase is called when the download enters a new stage. It may repeat the current phase.
	Phase(phase Phase)
	// Bytes reports the bytes transferred so far in the current phase; total is negative when unknown.
	Bytes(current, total int64)
}

func reportPhase(progress Progress, phase Phase) {
	if progress != nil {
		progress.Phase(phase)
	}
}

type progressWriter struct {
	progress Progress
	current  int64
	total    int64
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.current += int64(len(p))
	w.progress.Bytes(w.current, w.total)

	return len(p), nil
}
