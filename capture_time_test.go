package main

import (
	"bytes"
	"os"
	"testing"

	rawexif "github.com/rwcarlsen/goexif/exif"
)

func TestRawCaptureTime(t *testing.T) {
	data, err := os.ReadFile("testdata/orientation6.jpg")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, replacement, want string }{
		{"valid", "2020:03:04 12:13:14", "2020:03:04 12:13:14"},
		{"ISO editor date", "2020-03-04 12:13:14", "2020:03:04 12:13:14"},
		{"invalid day", "2020:02:31 12:13:14", ""},
		{"empty", "                   ", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := bytes.ReplaceAll(data, []byte("2020:03:04 12:13:14"), []byte(tc.replacement))
			raw, err := rawexif.Decode(bytes.NewReader(input))
			if err != nil {
				t.Fatal(err)
			}
			got := rawCaptureTime(raw)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("unexpected capture date %s", *got)
				}
			} else if got == nil || *got != tc.want {
				t.Fatalf("capture date %v, want %s", got, tc.want)
			}
		})
	}
	if rawCaptureTime(nil) != nil {
		t.Fatal("missing EXIF must not produce a date")
	}
}
