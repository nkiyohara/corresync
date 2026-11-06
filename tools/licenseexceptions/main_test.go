package main

import "testing"

func TestChangedOrMissingLicenseFailsClosed(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("MIT No Attribution"), []byte("synthetic changed license")} {
		if err := checkLicense(data); err == nil {
			t.Fatal("unreviewed license was accepted")
		}
	}
}
