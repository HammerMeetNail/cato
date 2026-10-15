package v1

import (
	"log/slog"
	"net/http"
)

// reloadReport is what the app sends when it starts within a few seconds
// of its own in-app navigation in the same tab: the browser reloaded the
// page under it (a crashed or killed page, an extension, a gesture). It
// only goes to the server log, to find out why on devices we can't test.
type reloadReport struct {
	NavType    string `json:"nav_type"`     // PerformanceNavigationTiming.type
	SinceNavMS int64  `json:"since_nav_ms"` // from the last in-app navigation to this start
	CleanExit  bool   `json:"clean_exit"`   // the previous page saw pagehide
	From       string `json:"from"`         // hash before that navigation
	To         string `json:"to"`           // hash it navigated to
	UptimeMS   int64  `json:"uptime_ms"`    // how long the previous page had been open
}

const maxReportText = 200

func (a *API) reportReload(w http.ResponseWriter, r *http.Request) {
	var rep reloadReport
	if !decode(w, r, &rep) {
		return
	}
	for _, s := range []string{rep.NavType, rep.From, rep.To} {
		if len(s) > maxReportText {
			writeError(w, http.StatusBadRequest, "invalid_report", "Report fields are too long", nil)
			return
		}
	}
	if rep.SinceNavMS < 0 || rep.UptimeMS < 0 {
		writeError(w, http.StatusBadRequest, "invalid_report", "Timings can't be negative", nil)
		return
	}
	slog.WarnContext(r.Context(), "unexpected reload",
		"nav_type", rep.NavType, "since_nav_ms", rep.SinceNavMS, "clean_exit", rep.CleanExit,
		"from", rep.From, "to", rep.To, "uptime_ms", rep.UptimeMS, "ua", userAgent(r))
	w.WriteHeader(http.StatusNoContent)
}

// swipeReport is the outcome of one sideways gesture (features/swipe.js):
// whether it navigated, or why not. Server log only.
type swipeReport struct {
	Outcome string `json:"outcome"`
	DX      int64  `json:"dx"`
	DY      int64  `json:"dy"`
	StartX  int64  `json:"start_x"`
	Width   int64  `json:"width"`
	MS      int64  `json:"ms"`
}

func (a *API) reportSwipe(w http.ResponseWriter, r *http.Request) {
	var rep swipeReport
	if !decode(w, r, &rep) {
		return
	}
	if len(rep.Outcome) > maxReportText {
		writeError(w, http.StatusBadRequest, "invalid_report", "Report fields are too long", nil)
		return
	}
	slog.InfoContext(r.Context(), "swipe gesture",
		"outcome", rep.Outcome, "dx", rep.DX, "dy", rep.DY, "start_x", rep.StartX,
		"width", rep.Width, "ms", rep.MS, "ua", userAgent(r))
	w.WriteHeader(http.StatusNoContent)
}

func userAgent(r *http.Request) string {
	ua := r.UserAgent()
	if len(ua) > 2*maxReportText {
		ua = ua[:2*maxReportText]
	}
	return ua
}
