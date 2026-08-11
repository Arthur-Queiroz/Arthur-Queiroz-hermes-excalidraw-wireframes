package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderSVGHandlesDeletedOpacityMultilineAndBackground(t *testing.T) {
	document := json.RawMessage(`{
		"type":"excalidraw","version":2,
		"appState":{"viewBackgroundColor":"#abcdef"},
		"elements":[
			{"id":"deleted","type":"rectangle","x":100000,"y":100000,"width":500,"height":500,"isDeleted":true},
			{"id":"text","type":"text","x":10,"y":20,"text":"linha 1\nlinha 2","fontSize":20,"opacity":0},
			{"id":"line","type":"line","x":10,"y":80,"points":[[0,0],[100,0]],"opacity":50}
		]
	}`)
	svg, err := renderSVG(document)
	if err != nil {
		t.Fatal(err)
	}
	got := string(svg)
	for _, expected := range []string{`fill="#abcdef"`, `data-element-id="text"`, `opacity="0"`, `<tspan`, `linha 2`, `data-element-id="line"`, `opacity="0.5"`} {
		if !strings.Contains(got, expected) {
			t.Fatalf("SVG missing %q: %s", expected, got)
		}
	}
	if strings.Contains(got, "deleted") || strings.Contains(got, "100000") {
		t.Fatalf("deleted element affected SVG: %s", got)
	}
}

func TestLimitedBufferNeverAllocatesPastLimit(t *testing.T) {
	buffer := &limitedBuffer{limit: 8}
	if _, err := buffer.Write([]byte("1234567890123456")); err != nil {
		t.Fatal(err)
	}
	if !buffer.exceeded || buffer.buffer.Len() != 8 {
		t.Fatalf("exceeded=%v len=%d, want true and 8", buffer.exceeded, buffer.buffer.Len())
	}
}
