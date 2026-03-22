package api

// KpiItem is a single KPI key-value pair.
type KpiItem struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// KpisResponse is the KPI dashboard response.
type KpisResponse struct {
	HasData bool      `json:"has_data"`
	Kpis    []KpiItem `json:"kpis"`
}
