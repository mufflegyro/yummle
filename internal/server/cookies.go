package server

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"kapsel/internal/download"
)

// youtubeCookiesSettingsCheck reports the readiness state of the YouTube
// cookies file. Not configured warns (mark-watched on save is unavailable and
// some videos fail signed-out); configured but unusable is an error; auth
// cookies within 14 days of expiry warn.
func youtubeCookiesSettingsCheck(status download.CookiesStatus) settingsReadinessCheck {
	const id = "youtube_cookies"
	if !status.Configured {
		return settingsReadinessCheck{
			ID: id, Label: "YouTube cookies", State: "warn",
			Summary: "KAPSEL_YTDLP_COOKIES_FILE is not set.",
			Detail:  "Anonymous downloads may fail YouTube's bot check, and mark-watched on save is unavailable.",
		}
	}
	if !status.Readable {
		return settingsReadinessCheck{
			ID: id, Label: "YouTube cookies", State: "error",
			Summary: "The YouTube cookies file is unreadable.",
			Detail:  status.ParseError,
		}
	}
	if !status.AuthCookies {
		return settingsReadinessCheck{
			ID: id, Label: "YouTube cookies", State: "error",
			Summary: "The YouTube cookies file has no signed-in session.",
			Detail:  "No signed-in session cookie was found. Re-export the cookies with scripts/export-cookies.sh while signed in to YouTube.",
		}
	}

	detail := "A signed-in session cookie is present."
	if status.Expiry != nil && !status.Expiry.IsZero() {
		if days := max64(int64(time.Until(*status.Expiry)/(24*time.Hour)), 0); days < int64(download.CookiesExpiryWarnWindow/(24*time.Hour)) {
			return settingsReadinessCheck{
				ID: id, Label: "YouTube cookies", State: "warn",
				Summary: "The YouTube session cookies expire soon.",
				Detail:  fmt.Sprintf("Earliest signed-in cookie expires in %d day(s); re-export soon.", days),
			}
		}
	}
	if status.Verified {
		detail = "Verified against YouTube."
	}
	if status.ProbeError != "" {
		detail = "Latest probe did not pass: " + status.ProbeError
	}

	return settingsReadinessCheck{
		ID: id, Label: "YouTube cookies", State: "pass",
		Summary: "A signed-in YouTube session is configured for yt-dlp.",
		Detail:  detail,
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}

	return b
}

// capabilityResponse is the shape of GET /api/capabilities. Keep it cheap:
// static cookie check only — no storage scan, no diskspace walk, no probe.
type capabilityResponse struct {
	MarkWatched capabilityMarkWatched `json:"mark_watched"`
}

type capabilityMarkWatched struct {
	Available bool   `json:"available"`
	Reason    *string `json:"reason"`
}

func markWatchedCapability(status download.CookiesStatus) capabilityMarkWatched {
	if status.Available() {
		return capabilityMarkWatched{Available: true}
	}
	reason := status.UnavailableReason()

	return capabilityMarkWatched{Reason: &reason}
}

func getCapabilities(config config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, capabilityResponse{
			MarkWatched: markWatchedCapability(download.CheckCookies(config.cookiesFile)),
		})
	}
}

func testYouTubeCookies(config config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		if allowed, retryAfter := config.cookiesProbe.Allow(now); !allowed {
			seconds := int(retryAfter.Seconds())
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			http.Error(w, fmt.Sprintf("cookies probe rate limited; retry in %d seconds", seconds), http.StatusTooManyRequests)
			return
		}
		status := download.CheckCookiesLive(r.Context(), config.ytdlpPath, config.cookiesFile, config.cookiesRunner)
		writeJSON(w, http.StatusOK, config.cookiesProbe.Record(status, now))
	}
}
