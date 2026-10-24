// Command update serves a fake GitHub release of Companion on localhost so
// the in-app updater can be exercised end to end (OC-75). It packs the
// binaries from -bin into the release tarball layout, writes
// checksums.txt, signs it as the release workflow on a private Sigstore
// instance and answers the GitHub API calls the updater makes.
//
//	go build -ldflags "-X github.com/xlsft/companion/internal/buildinfo.Version=0.2.0" -o /tmp/v2/ ./cmd/companion ./cmd/stt-sidecar
//	cp bin/libsherpa-onnx-c-api.so bin/libonnxruntime.so /tmp/v2/  # from make build-stt
//	update -version 0.2.0 -bin /tmp/v2 -dir /tmp/release
//	COMPANION_UPDATE_URL=http://127.0.0.1:8765/repos/xlsft/companion \
//	COMPANION_UPDATE_TRUSTED_ROOT=/tmp/release/trusted_root.json companion
//
// The private instance's root is valid for an hour.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xlsft/companion/internal/update"
	"github.com/xlsft/companion/internal/update/updatetest"
)

func main() {
	version := flag.String("version", "0.2.0", "version of the fake release")
	bin := flag.String("bin", "", "directory with the new companion, stt-sidecar and the sherpa-onnx libraries")
	dir := flag.String("dir", "", "where the release files and trusted_root.json are written")
	addr := flag.String("addr", "127.0.0.1:8765", "listen address")
	identity := flag.String("identity", "", "certificate identity (default: the release workflow at tag v<version>)")
	flag.Parse()
	if *bin == "" || *dir == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *identity == "" {
		*identity = "https://github.com/xlsft/companion/.github/workflows/release.yml@refs/tags/v" + *version
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatal(err)
	}
	tarName := fmt.Sprintf("companion-%s-linux-x86_64.tar.gz", *version)
	if err := pack(filepath.Join(*dir, tarName), *bin, *version); err != nil {
		log.Fatal(err)
	}
	sum, err := fileSum(filepath.Join(*dir, tarName))
	if err != nil {
		log.Fatal(err)
	}
	sums := []byte(sum + "  " + tarName + "\n")
	signer, err := updatetest.NewSigner()
	if err != nil {
		log.Fatal(err)
	}
	bundle, err := signer.SignBundle(*identity, update.ReleaseIssuer, sums)
	if err != nil {
		log.Fatal(err)
	}
	rootJSON, err := signer.TrustedRootJSON()
	if err != nil {
		log.Fatal(err)
	}
	for name, b := range map[string][]byte{update.ChecksumsName: sums, update.SignatureName: bundle, "trusted_root.json": rootJSON} {
		if err := os.WriteFile(filepath.Join(*dir, name), b, 0o644); err != nil {
			log.Fatal(err)
		}
	}

	base := "http://" + *addr
	asset := func(name string) map[string]any {
		st, _ := os.Stat(filepath.Join(*dir, name))
		return map[string]any{"name": name, "size": st.Size(), "browser_download_url": base + "/download/" + name, "url": base + "/download/" + name}
	}
	rel := map[string]any{
		"tag_name": "v" + *version, "name": "Companion " + *version, "html_url": base + "/notes",
		"draft": false, "prerelease": false, "published_at": time.Now().UTC().Format(time.RFC3339),
		"assets": []any{asset(tarName), asset(update.ChecksumsName), asset(update.SignatureName)},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/xlsft/companion/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		w.Header().Set("ETag", `"`+*version+`"`)
		if r.Header.Get("If-None-Match") == `"`+*version+`"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		json.NewEncoder(w).Encode(rel)
	})
	mux.HandleFunc("/repos/xlsft/companion/releases", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		json.NewEncoder(w).Encode([]any{rel})
	})
	mux.Handle("/download/", logged(http.StripPrefix("/download/", http.FileServer(http.Dir(*dir)))))
	mux.HandleFunc("/notes", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "<h1>Companion %s</h1><p>Fake release notes from the local release server.</p>", *version)
	})
	fmt.Printf("COMPANION_UPDATE_URL=%s/repos/xlsft/companion\nCOMPANION_UPDATE_TRUSTED_ROOT=%s\n", base, filepath.Join(*dir, "trusted_root.json"))
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func logged(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s %s", r.Method, r.URL.Path, r.Header.Get("Range"))
		h.ServeHTTP(w, r)
	})
}

// pack writes the release tarball: companion-<v>-linux-x86_64/ with both
// binaries and a README, like packaging/dist-linux.sh.
func pack(out, bin, version string) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	top := fmt.Sprintf("companion-%s-linux-x86_64/", version)
	if err := tw.WriteHeader(&tar.Header{Name: top, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		return err
	}
	// The same files the updater replaces, so a fake release installs
	// like a real one.
	for _, name := range update.DefaultBinaries() {
		b, err := os.ReadFile(filepath.Join(bin, name))
		if err != nil {
			return err
		}
		mode := int64(0o755)
		if strings.HasSuffix(name, ".so") {
			mode = 0o644
		}
		if err := tw.WriteHeader(&tar.Header{Name: top + name, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(b))}); err != nil {
			return err
		}
		if _, err := tw.Write(b); err != nil {
			return err
		}
	}
	readme := []byte("Fake release " + version + " from cmd/spikes/update.\n")
	if err := tw.WriteHeader(&tar.Header{Name: top + "README.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(readme))}); err != nil {
		return err
	}
	if _, err := tw.Write(readme); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func fileSum(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}
