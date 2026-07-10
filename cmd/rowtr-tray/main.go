// Command rowtr-tray is Rowtr's menu-bar app: it reads the usage database the
// proxy writes and shows how much Claude quota was conserved by serving
// requests locally — the number that matters on a flat-rate subscription.
//
// It is a separate binary because the tray library needs cgo; the proxy/CLI
// stay cgo-free. Build on the target OS.
package main

import (
	"fmt"
	"time"

	"fyne.io/systray"

	"github.com/connorhoulihan/rowtr/internal/config"
	"github.com/connorhoulihan/rowtr/internal/usage"
)

const modelSlots = 8 // fixed rows we fill/hide (systray items are created up front)

var store *usage.Store

func main() {
	systray.Run(onReady, func() {
		if store != nil {
			store.Close()
		}
	})
}

func onReady() {
	systray.SetTitle("Rowtr")
	systray.SetTooltip("Rowtr — usage kept off your Claude quota")

	if p, err := config.UsagePath(); err == nil {
		store, _ = usage.Open(p) // nil on error → menu shows a hint
	}

	mKept := systray.AddMenuItem("Kept off Claude: —", "Requests and tokens Rowtr served locally instead of Claude")
	mKept.Disable()
	mRate := systray.AddMenuItem("Offload rate: —", "Share of requests handled locally")
	mRate.Disable()
	mCache := systray.AddMenuItem("Prompt cache: —", "Share of Claude input served from Anthropic's prompt cache (~10× cheaper)")
	mCache.Disable()

	systray.AddSeparator()
	header := systray.AddMenuItem("By model", "Requests directed to each model")
	header.Disable()
	modelItems := make([]*systray.MenuItem, modelSlots)
	for i := range modelItems {
		modelItems[i] = systray.AddMenuItem("", "")
		modelItems[i].Disable()
		modelItems[i].Hide()
	}

	systray.AddSeparator()
	// Demoted: only meaningful on pay-per-token API pricing, not subscriptions.
	mMoney := systray.AddMenuItem("Est. $ saved: —", "Estimate for pay-per-token API pricing (irrelevant on a subscription)")
	mMoney.Disable()

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "")

	go func() {
		for {
			refresh(mKept, mRate, mCache, mMoney, modelItems)
			time.Sleep(3 * time.Second)
		}
	}()
	go func() {
		<-mQuit.ClickedCh
		systray.Quit()
	}()
}

func refresh(mKept, mRate, mCache, mMoney *systray.MenuItem, modelItems []*systray.MenuItem) {
	if store == nil {
		systray.SetTitle("Rowtr –")
		mKept.SetTitle("Usage DB not found — run `rowtr serve` first")
		return
	}
	sum, err := store.Summary()
	if err != nil {
		mKept.SetTitle("Usage read error")
		return
	}

	systray.SetTitle(fmt.Sprintf("%s tok saved", humanTokens(sum.LocalTokens)))
	mKept.SetTitle(fmt.Sprintf("Kept off Claude: %d requests · %s tokens", sum.Local, humanTokens(sum.LocalTokens)))

	rate := 0.0
	if sum.Total > 0 {
		rate = 100 * float64(sum.Local) / float64(sum.Total)
	}
	rateLine := fmt.Sprintf("Offload rate: %.0f%% of requests (%d/%d)", rate, sum.Local, sum.Total)
	if allTok := sum.LocalTokens + sum.FrontierTokens; allTok > 0 && sum.FrontierTokens > 0 {
		rateLine += fmt.Sprintf(" · %.0f%% of tokens", 100*float64(sum.LocalTokens)/float64(allTok))
	}
	mRate.SetTitle(rateLine)
	if sum.CacheReadTokens > 0 || sum.CacheWriteTokens > 0 {
		mCache.SetTitle(fmt.Sprintf("Prompt cache: %.0f%% of Claude input (%s read)",
			100*sum.CacheHitRate(), humanTokens(sum.CacheReadTokens)))
		mCache.Show()
	} else {
		mCache.SetTitle("Prompt cache: no data yet")
	}
	mMoney.SetTitle(fmt.Sprintf("Est. $ saved (API pricing): $%.4f", sum.SavedUSD))

	for i, it := range modelItems {
		if i < len(sum.ByModel) {
			m := sum.ByModel[i]
			it.SetTitle(fmt.Sprintf("%-24s %-9s %d", m.Model, "("+m.Tier+")", m.Count))
			it.Show()
		} else {
			it.Hide()
		}
	}
}

// humanTokens formats a token count compactly (687, 1.2k, 3.4M).
func humanTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}
