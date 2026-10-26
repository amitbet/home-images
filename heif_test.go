package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestHEIFWithoutExternalTools(t *testing.T) {
	l, _, root := fixture(t)
	l.config.FFmpeg = ""
	data, e := os.ReadFile("testdata/sample.heic")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "native.heic"), data, 0644); e != nil {
		t.Fatal(e)
	}
	p, e := l.metadata(context.Background(), "native.heic")
	if e != nil {
		t.Fatal(e)
	}
	if p.Width != 96 || p.Height != 64 {
		t.Fatal(p.Width, p.Height)
	}
	if p.TakenAt == nil || *p.TakenAt != "2020:03:04 12:13:14" || p.Exif["Make"] != "Synthetic Camera" {
		t.Fatalf("HEIF EXIF extraction failed: %+v", p)
	}
}

func makeBox(kind string, payload []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(payload)+8))
	b.WriteString(kind)
	b.Write(payload)
	return b.Bytes()
}
func TestHEIFPrimaryAssociationAndRotation(t *testing.T) {
	spatial := func(w, h uint32) []byte {
		b := make([]byte, 12)
		binary.BigEndian.PutUint32(b[4:8], w)
		binary.BigEndian.PutUint32(b[8:12], h)
		return makeBox("ispe", b)
	}
	properties := append(spatial(10, 10), spatial(96, 64)...)
	properties = append(properties, makeBox("irot", []byte{1})...)
	// primary item=2; associate it with ispe property #2 and rotation #3.
	associations := []byte{0, 0, 0, 0, 0, 0, 0, 1, 0, 2, 2, 2, 3}
	iprp := makeBox("iprp", append(makeBox("ipco", properties), makeBox("ipma", associations)...))
	meta := makeBox("meta", append(append([]byte{0, 0, 0, 0}, makeBox("pitm", []byte{0, 0, 0, 0, 0, 2})...), iprp...))
	f, e := os.CreateTemp(t.TempDir(), "container")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if _, e = f.Write(meta); e != nil {
		t.Fatal(e)
	}
	w, h, e := heifDimensions(f)
	if e != nil || w != 64 || h != 96 {
		t.Fatalf("primary/rotation: %dx%d %v", w, h, e)
	}
	if _, e = f.WriteAt([]byte{0x7f, 0xff, 0xff, 0xff}, 0); e != nil {
		t.Fatal(e)
	}
	if _, _, e = heifDimensions(f); e == nil {
		t.Fatal("invalid box length accepted")
	}
}
