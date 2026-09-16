package macos

import (
	"bytes"
	"compress/gzip"
	"os"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return raw
}

// TestDecodeCatalogFindsFullInstallers checks both halves of the full-installer
// filter, using a product verified against Apple's live catalog.
func TestDecodeCatalogFindsFullInstallers(t *testing.T) {
	products, err := decodeCatalog(readFixture(t, "catalog.plist"))
	if err != nil {
		t.Fatalf("decodeCatalog: %v", err)
	}

	if len(products) != 1 {
		t.Fatalf("kept %d products, want only the full installer: %v", len(products), keys(products))
	}
	prod, ok := products["142-15488"]
	if !ok {
		t.Fatalf("product 142-15488 missing; got %v", keys(products))
	}

	installer, ok := prod.installer()
	if !ok {
		t.Fatal("no InstallAssistant.pkg in the product")
	}
	const wantURL = "https://swcdn.apple.com/content/downloads/01/05/142-15488-A_9XNIAXWVVL/" +
		"5ajxcy8xk014o98zz1uut0zrlaj2nx91ln/InstallAssistant.pkg"
	if installer.URL != wantURL {
		t.Errorf("installer URL = %q", installer.URL)
	}
	if installer.Size != 18400314350 {
		t.Errorf("size = %d, want 18400314350", installer.Size)
	}
	if installer.Digest != "d349196a226931596bfee58c323e4bb0fa7b8ae9" {
		t.Errorf("digest = %q", installer.Digest)
	}
	if got := prod.PostDate.UTC().Format("2006-01-02"); got != "2026-09-14" {
		t.Errorf("post date = %s", got)
	}
	if !strings.HasSuffix(prod.distURL(), "142-15488.English.dist") {
		t.Errorf("dist URL = %q", prod.distURL())
	}
}

// TestDecodeCatalogAcceptsGzip covers the edge nodes that really do gzip.
func TestDecodeCatalogAcceptsGzip(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(readFixture(t, "catalog.plist")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	products, err := decodeCatalog(buf.Bytes())
	if err != nil {
		t.Fatalf("gzipped catalog: %v", err)
	}
	if _, ok := products["142-15488"]; !ok {
		t.Error("gzipped catalog lost the installer")
	}
}

// TestCatalogURLPerChannel pins the filename shape Apple uses per channel.
func TestCatalogURLPerChannel(t *testing.T) {
	cases := map[string]string{
		"public":       "index-27-26-15-",
		"devseed":      "index-27seed-27-26-15-",
		"beta":         "index-27beta-27-26-15-",
		"customerseed": "index-27customerseed-27-26-15-",
	}
	for channel, prefix := range cases {
		got, err := CatalogURL(channel)
		if err != nil {
			t.Fatalf("%s: %v", channel, err)
		}
		if !strings.HasPrefix(got, catalogBase+prefix) {
			t.Errorf("%s URL = %q, want prefix %q", channel, got, catalogBase+prefix)
		}
		if !strings.HasSuffix(got, ".merged-1.sucatalog.gz") {
			t.Errorf("%s URL = %q", channel, got)
		}
	}
	if _, err := CatalogURL("nonsense"); err == nil {
		t.Error("an unknown channel should be rejected")
	}
}

func keys(m map[string]product) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
