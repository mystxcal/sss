// Package archive streams a committed payload as ZIP or TAR. Archives are
// generated on the fly: no second copy of the payload is ever written to disk.
package archive

import (
	"archive/tar"
	"archive/zip"
	"context"
	"io"
	"os"
	"time"

	"github.com/sss/sss/internal/platform"
	"github.com/sss/sss/internal/protocol"
)

const copyBufferSize = 1 << 20

// StreamZip writes the payload as a ZIP archive.
func StreamZip(ctx context.Context, w io.Writer, payloadDir string, m protocol.Manifest) error {
	zw := zip.NewWriter(w)
	buf := make([]byte, copyBufferSize)
	for _, e := range m.SortedEntries() {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := e.Path
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Unix(0, e.MTimeUnixNS)}
		if e.Type == protocol.EntryDirectory {
			hdr.Name = name + "/"
			hdr.Method = zip.Store
			hdr.SetMode(os.FileMode(0o755) | os.ModeDir)
			if _, err := zw.CreateHeader(hdr); err != nil {
				return err
			}
			continue
		}
		mode := os.FileMode(0o644)
		if e.Mode&0o111 != 0 {
			mode = 0o755
		}
		hdr.SetMode(mode)
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if err := copyEntry(payloadDir, e.Path, fw, buf); err != nil {
			return err
		}
	}
	return zw.Close()
}

// StreamTar writes the payload as an uncompressed TAR archive.
func StreamTar(ctx context.Context, w io.Writer, payloadDir string, m protocol.Manifest) error {
	tw := tar.NewWriter(w)
	buf := make([]byte, copyBufferSize)
	for _, e := range m.SortedEntries() {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr := &tar.Header{
			Name:    e.Path,
			ModTime: time.Unix(0, e.MTimeUnixNS),
			Format:  tar.FormatPAX,
		}
		if e.Type == protocol.EntryDirectory {
			hdr.Typeflag = tar.TypeDir
			hdr.Name = e.Path + "/"
			hdr.Mode = 0o755
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			continue
		}
		hdr.Typeflag = tar.TypeReg
		hdr.Size = e.Size
		hdr.Mode = 0o644
		if e.Mode&0o111 != 0 {
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if err := copyEntry(payloadDir, e.Path, tw, buf); err != nil {
			return err
		}
	}
	return tw.Close()
}

func copyEntry(payloadDir, rel string, w io.Writer, buf []byte) error {
	path, err := platform.SafeJoin(payloadDir, rel)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.CopyBuffer(w, f, buf)
	return err
}
