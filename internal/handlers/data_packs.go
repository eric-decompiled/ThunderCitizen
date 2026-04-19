package handlers

import (
	"net/http"

	"thundercitizen/internal/muni"
	"thundercitizen/internal/munisign"
	"thundercitizen/internal/views"
	"thundercitizen/templates/pages"
)

// AttachMuni wires the shared trust store and async apply status into
// the handler set. Called once in main after the server constructs its
// munisign.Trust and muni.Status singletons.
func (h *Handlers) AttachMuni(trust *munisign.Trust, status *muni.Status) {
	h.trust = trust
	h.muniStatus = status
}

// DataPacks renders /data — a read-only view of installed packs,
// bundle apply status, and the embedded trust store. No actions;
// changing what's trusted or applied requires a release.
func (h *Handlers) DataPacks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	packs, err := muni.ListPacks(ctx, h.db)
	if err != nil {
		log.Warn("muni pack list failed", "err", err)
	}

	var approved, revoked []munisign.TrustedKey
	signerFile := make(map[string]string)
	if h.trust != nil {
		approved, revoked = h.trust.Summary()
		for _, k := range approved {
			signerFile[k.Fingerprint] = k.Filename
		}
	}

	status := muni.StatusSnapshot{}
	if h.muniStatus != nil {
		status = h.muniStatus.Snapshot()
	}

	vm := views.NewDataPacksViewModel(status, packs, approved, revoked, signerFile)
	renderPage(w, r, pages.DataPacksPartial(vm), pages.DataPacks(vm))
}
