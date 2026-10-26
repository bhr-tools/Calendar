package feeds

import (
	"strings"
	"testing"
	"time"
)

// An excerpt of an ADE timetable (edt.inp-toulouse.fr), with its folded lines.
const ade = "BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nPRODID:-//ADE/version 6.0\r\nVERSION:2.0\r\nCALSCALE:GREGORIAN\r\n" +
	"BEGIN:VEVENT\r\nDTSTAMP:%s\r\nDTSTART:20260910T060000Z\r\nDTEND:20260910T074500Z\r\n" +
	"SUMMARY:N7EN12A - Cours - 2SN-BML - Optimisation numérique\r\nLOCATION:A002 (135)\r\n" +
	"DESCRIPTION:\\n\\n\\n2SN-B-S1 (31)\\nGratton Se\r\n rge\\n(Exporté le:%s)\\n\r\n" +
	"UID:ADE604e37323032362d323032372d323934302d302d30\r\nCREATED:19700101T000000Z\r\nLAST-MODIFIED:%s\r\nSEQUENCE:2142533632\r\nEND:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func feed(stamp string) string {
	return strings.NewReplacer("%s", stamp).Replace(ade)
}

func TestSplitIsStableAcrossExports(t *testing.T) {
	paris, _ := time.LoadLocation("Europe/Paris")
	first, err := Split(feed("20261026T101219Z"), paris)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Split(feed("20261027T080000Z"), paris)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("got %d and %d objects", len(first), len(second))
	}
	if first[0].Data != second[0].Data {
		t.Errorf("an unchanged event changed between exports:\n%s\n---\n%s", first[0].Data, second[0].Data)
	}
	if strings.Contains(first[0].Data, "Export") {
		t.Errorf("export stamp kept:\n%s", first[0].Data)
	}
	if !strings.Contains(first[0].Search, "gratton serge") {
		t.Errorf("search text = %q", first[0].Search)
	}
	start := time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC).Unix()
	if first[0].FirstStart != start || first[0].LastEnd != start+105*60 {
		t.Errorf("bounds = %d..%d", first[0].FirstStart, first[0].LastEnd)
	}
}
