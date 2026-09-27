package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func uploadCmd() *cobra.Command {
	var server, caseID, token string
	var headers []string
	var chunkMiB int
	c := &cobra.Command{
		Use:   "upload BUNDLE",
		Short: "Upload a bundle to a hosted Flightbay desk, resuming after interruptions",
		Long: `Uploads a bundle in chunks. If the connection drops, it asks the server how much
arrived and continues from there. Support can open the workspace as soon as the
first chunk lands. Authentication is a bearer token accepted by the SSO proxy in
front of the desk (--token or FLIGHTBAY_TOKEN).`,
		Example: `  flightbay upload flightbay-01234567-20260927T1012Z.tar.zst.age --server https://flightbay.support.example --case 01234567`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				token = os.Getenv("FLIGHTBAY_TOKEN")
			}
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			st, err := f.Stat()
			if err != nil {
				return err
			}
			base := strings.TrimRight(server, "/")
			do := func(method, u string, body io.Reader, hdr map[string]string) (*http.Response, error) {
				req, err := http.NewRequest(method, u, body)
				if err != nil {
					return nil, err
				}
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				for _, h := range headers {
					if k, v, ok := strings.Cut(h, ":"); ok {
						req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
					}
				}
				for k, v := range hdr {
					req.Header.Set(k, v)
				}
				return (&http.Client{Timeout: 10 * time.Minute}).Do(req)
			}
			resp, err := do("POST", base+"/api/uploads?"+url.Values{"case": {caseID}, "name": {filepath.Base(args[0])}}.Encode(), nil, nil)
			if err != nil {
				return err
			}
			var up struct{ ID, Upload, Workspace, Error string }
			_ = json.NewDecoder(resp.Body).Decode(&up)
			resp.Body.Close()
			if resp.StatusCode != http.StatusCreated {
				return fmt.Errorf("server refused the upload: %s %s", resp.Status, up.Error)
			}
			out := cmd.ErrOrStderr()
			fmt.Fprintf(out, "Uploading %s (%d bytes) to case %s. Workspace: %s%s\n", filepath.Base(args[0]), st.Size(), caseID, base, up.Workspace)
			chunk := int64(chunkMiB) << 20
			buf := make([]byte, chunk)
			var off int64
			failures := 0
			for off < st.Size() {
				n, err := f.ReadAt(buf[:min(chunk, st.Size()-off)], off)
				if err != nil && err != io.EOF {
					return err
				}
				last := off+int64(n) >= st.Size()
				resp, err := do("PUT", base+up.Upload, bytes.NewReader(buf[:n]), map[string]string{"Upload-Offset": strconv.FormatInt(off, 10), "Upload-Complete": strconv.FormatBool(last)})
				if err == nil && resp.StatusCode == http.StatusOK {
					resp.Body.Close()
					off += int64(n)
					failures = 0
					fmt.Fprintf(out, "  %d%%\n", off*100/st.Size())
					continue
				}
				if err == nil {
					b, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					if resp.StatusCode != http.StatusConflict && resp.StatusCode < 500 {
						return fmt.Errorf("upload failed: %s %s", resp.Status, strings.TrimSpace(string(b)))
					}
				}
				failures++
				if failures > 8 {
					return fmt.Errorf("upload failed after repeated retries at byte %d: %v", off, err)
				}
				time.Sleep(time.Duration(failures) * time.Second)
				if h, err := do("HEAD", base+up.Upload, nil, nil); err == nil {
					if v, err := strconv.ParseInt(h.Header.Get("Upload-Offset"), 10, 64); err == nil {
						off = v // resume from what the server has
					}
					h.Body.Close()
				}
				fmt.Fprintf(out, "  connection problem, resuming at byte %d\n", off)
			}
			fmt.Fprintf(out, "Uploaded. Support can open %s%s\n", base, up.Workspace)
			return nil
		},
	}
	c.Flags().StringVar(&server, "server", "", "hosted desk URL")
	c.Flags().StringVar(&caseID, "case", "", "support case number")
	c.Flags().StringVar(&token, "token", "", "bearer token for the SSO proxy (default $FLIGHTBAY_TOKEN)")
	c.Flags().StringSliceVar(&headers, "header", nil, "extra request header, e.g. 'X-Api-Key: ...'")
	c.Flags().IntVar(&chunkMiB, "chunk-mib", 8, "chunk size in MiB")
	_ = c.MarkFlagRequired("server")
	_ = c.MarkFlagRequired("case")
	return c
}
