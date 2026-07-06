// Command rowtr-tray is Rowtr's menu-bar app. It reads the usage database the
// proxy writes and shows, right in the menu bar, how much Claude quota you've
// conserved by serving requests locally — the value that matters on a flat-rate
// subscription. A per-token dollar estimate is a secondary line for API users.
//
// This is a separate binary from `rowtr` because the tray library uses native
// GUI APIs (cgo); the proxy/CLI stay cgo-free. Build on the target OS:
//
//	go build -o rowtr-tray ./cmd/rowtr-tray
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
			refresh(mKept, mRate, mMoney, modelItems)
			time.Sleep(3 * time.Second)
		}
	}()
	go func() {
		<-mQuit.ClickedCh
		systray.Quit()
	}()
}

func refresh(mKept, mRate, mMoney *systray.MenuItem, modelItems []*systray.MenuItem) {
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

	// Menu-bar headline = tokens kept off your Claude quota.
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
