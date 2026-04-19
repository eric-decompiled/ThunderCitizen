package muni

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"testing/fstest"
)

func sha256hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func testBOD(datasets map[string][]byte) fstest.MapFS {
	fs := fstest.MapFS{}

	// Build BOD.tsv
	bod := "dataset\tplugin\ttable\tsource_url\tsource_doc\tdescription\tcollected\tlicense\tprocessor\trows\tsha256\n"
	for name, data := range datasets {
		fs[name] = &fstest.MapFile{Data: data}
		bod += name + "\ttest_plugin\ttest_table\thttps://example.com\thttps://example.com/doc\tTest dataset\t2026-04-12T00:00:00Z\tpublic-record\ttest\t1\t" + sha256hex(data) + "\n"
	}
	fs[BODFile] = &fstest.MapFile{Data: []byte(bod)}
	return fs
}

func TestParseBOD_Valid(t *testing.T) {
	data := []byte("col1\tcol2\nval1\tval2\n")
	fs := testBOD(map[string][]byte{"test.tsv": data})

	datasets, err := ParseBOD(fs)
	if err != nil {
		t.Fatalf("ParseBOD: %v", err)
	}
	if len(datasets) != 1 {
		t.Fatalf("expected 1 dataset, got %d", len(datasets))
	}
	ds := datasets[0]
	if ds.File != "test.tsv" {
		t.Errorf("File: %q", ds.File)
	}
	if ds.Plugin != "test_plugin" {
		t.Errorf("Plugin: %q", ds.Plugin)
	}
	if ds.Table != "test_table" {
		t.Errorf("Table: %q", ds.Table)
	}
	if ds.Rows != 1 {
		t.Errorf("Rows: %d", ds.Rows)
	}
	if ds.SHA256 != sha256hex(data) {
		t.Errorf("SHA256 mismatch")
	}
}

func TestParseBOD_HashMismatch(t *testing.T) {
	fs := fstest.MapFS{
		"test.tsv": &fstest.MapFile{Data: []byte("real data")},
		BODFile: &fstest.MapFile{Data: []byte(
			"dataset\tplugin\ttable\tsource_url\tsource_doc\tdescription\tcollected\tlicense\tprocessor\trows\tsha256\n" +
				"test.tsv\tplug\ttbl\thttps://x\thttps://x\tdesc\t2026-01-01T00:00:00Z\tpublic\ttest\t1\twronghash\n",
		)},
	}

	_, err := ParseBOD(fs)
	if err == nil {
		t.Fatal("expected error for hash mismatch")
	}
}

func TestParseBOD_MissingFile(t *testing.T) {
	fs := fstest.MapFS{
		BODFile: &fstest.MapFile{Data: []byte(
			"dataset\tplugin\ttable\tsource_url\tsource_doc\tdescription\tcollected\tlicense\tprocessor\trows\tsha256\n" +
				"missing.tsv\tplug\ttbl\thttps://x\thttps://x\tdesc\t2026-01-01T00:00:00Z\tpublic\ttest\t1\tabc123\n",
		)},
	}

	_, err := ParseBOD(fs)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestParseBOD_Empty(t *testing.T) {
	fs := fstest.MapFS{
		BODFile: &fstest.MapFile{Data: []byte(
			"dataset\tplugin\ttable\tsource_url\tsource_doc\tdescription\tcollected\tlicense\tprocessor\trows\tsha256\n",
		)},
	}

	_, err := ParseBOD(fs)
	if err == nil {
		t.Fatal("expected error for empty BOD")
	}
}

func TestParseBOD_V2Schema(t *testing.T) {
	data := []byte("col1\tcol2\nval1\tval2\n")
	fs := fstest.MapFS{
		"budget.tsv": &fstest.MapFile{Data: data},
		BODFile: &fstest.MapFile{Data: []byte(
			"dataset\tplugin\ttable\tsource_url\tsource_doc\tdescription\tcollected\tlicense\tprocessor\trows\tsha256\tpack_id\tunit_kind\tunit_start\tunit_end\n" +
				"budget.tsv\tbudget\tbudget_ledger\thttps://x\thttps://x\tFY2026\t2026-04-12T00:00:00Z\tpublic-record\tpg_dump\t1\t" + sha256hex(data) + "\tbudget-2026\tbudget_year\t2026-01-01\t2026-12-31\n",
		)},
	}

	datasets, err := ParseBOD(fs)
	if err != nil {
		t.Fatalf("ParseBOD v2: %v", err)
	}
	if len(datasets) != 1 {
		t.Fatalf("expected 1 dataset, got %d", len(datasets))
	}
	ds := datasets[0]
	if ds.PackID != "budget-2026" {
		t.Errorf("PackID: %q", ds.PackID)
	}
	if ds.UnitKind != UnitBudgetYear {
		t.Errorf("UnitKind: %q", ds.UnitKind)
	}
	if ds.UnitStart.Format("2006-01-02") != "2026-01-01" {
		t.Errorf("UnitStart: %v", ds.UnitStart)
	}
	if ds.UnitEnd.Format("2006-01-02") != "2026-12-31" {
		t.Errorf("UnitEnd: %v", ds.UnitEnd)
	}
	if !ds.HasPack() {
		t.Error("HasPack should be true")
	}
}

func TestParseBOD_V1BackwardCompat(t *testing.T) {
	// Old 11-column BODs still parse — pack metadata defaults to global.
	data := []byte("col1\tcol2\nval1\tval2\n")
	fs := testBOD(map[string][]byte{"test.tsv": data})
	datasets, err := ParseBOD(fs)
	if err != nil {
		t.Fatalf("ParseBOD v1: %v", err)
	}
	if len(datasets) != 1 {
		t.Fatalf("expected 1 dataset, got %d", len(datasets))
	}
	ds := datasets[0]
	if ds.UnitKind != UnitGlobal {
		t.Errorf("UnitKind default: got %q, want global", ds.UnitKind)
	}
	if ds.HasPack() {
		t.Error("v1 rows should not report HasPack")
	}
}

func TestIsProvenance(t *testing.T) {
	if !IsProvenance("council_meetings.sources.tsv") {
		t.Error("expected true for .sources.tsv")
	}
	if IsProvenance("council_meetings.tsv") {
		t.Error("expected false for regular .tsv")
	}
}

func TestIsDirectURL(t *testing.T) {
	if !IsDirectURL("https://thunderbay.ca/budget") {
		t.Error("expected true for https URL")
	}
	if IsDirectURL("council_meetings.sources.tsv") {
		t.Error("expected false for relative path")
	}
}
