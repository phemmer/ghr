package ghr

import (
	"compress/gzip"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
)

type Recorder struct {
	Dir string
	http.RoundTripper
}
type RecorderWriter struct {
	Recorder Recorder
	fileName string
	tmpFile  *os.File
}

func InstallDefault(dir string) {
	rt := http.DefaultClient.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	http.DefaultClient.Transport = Recorder{
		Dir:          dir,
		RoundTripper: rt,
	}
}

func (rcd Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	os.Mkdir(rcd.Dir, 0750) // ignore error, we'll blow up on file create instead

	s := sha1.New()
	fmt.Fprintf(s, "%s %s\n", req.Method, req.URL.String())
	req.Header.Write(s)
	id := base64.RawURLEncoding.EncodeToString(s.Sum(nil))

	if f, err := os.Open(path.Join(rcd.Dir, id)); err == nil {
		//return rcd.roundTripCached(req, f)
		return NewRecorderReader(rcd, req, f)
	}
	//return rcd.roundTripNew(req, id)
	return NewRecorderWriter(rcd, req, id)
}

func NewRecorderWriter(rcd Recorder, req *http.Request, id string) (*http.Response, error) {
	rcdw := &RecorderWriter{
		Recorder: rcd,
	}

	resp, err := rcd.RoundTripper.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	rcdw.fileName = path.Join(rcd.Dir, id)
	rcdw.tmpFile, err = ioutil.TempFile(rcd.Dir, id+".")
	if err != nil {
		if resp.Body != nil {
			//io.Copy(ioutil.Discard, resp.Body)
			resp.Body.Close()
		}
		return nil, err
	}

	reqHdr := req.Method + " " + req.URL.String() + "\n"
	reqHdrBuf := make([]byte, 8)
	binary.PutUvarint(reqHdrBuf, uint64(len(reqHdr)))
	fmt.Fprintf(rcdw.tmpFile, "%s%s", reqHdrBuf, reqHdr)

	hdrMap := map[string]interface{}{
		"Status":           resp.Status,
		"StatusCode":       resp.StatusCode,
		"Proto":            resp.Proto,
		"ProtoMajor":       resp.ProtoMajor,
		"ProtoMinor":       resp.ProtoMinor,
		"Header":           resp.Header,
		"ContentLength":    resp.ContentLength,
		"TransferEncoding": resp.TransferEncoding,
		"Close":            resp.Close,
		"Uncompressed":     resp.Uncompressed,
		"Trailer":          resp.Trailer,
	}
	hdr, _ := json.Marshal(hdrMap)
	hdr = append(hdr, '\n')
	buf := make([]byte, 8)
	binary.PutUvarint(buf, uint64(len(hdr)))
	rcdw.tmpFile.Write(buf)
	rcdw.tmpFile.Write(hdr)
	gz := WriterMultiCloser{rcdw, gzip.NewWriter(rcdw.tmpFile)}
	resp.Body = NewTeeReadCloser(resp.Body, gz)

	return resp, nil
}

func NewRecorderReader(rcd Recorder, req *http.Request, f *os.File) (*http.Response, error) {
	// read the reqHdr
	reqHdrBuf := make([]byte, 8)
	io.ReadFull(f, reqHdrBuf)
	reqHdrLen, _ := binary.Uvarint(reqHdrBuf)
	reqHdr := make([]byte, reqHdrLen)
	io.ReadFull(f, reqHdr)

	if req.Body != nil {
		io.Copy(ioutil.Discard, req.Body)
		req.Body.Close() //TODO buffer body use it to generate hash
	}
	resp := &http.Response{
		Request: req,
	}
	buf := make([]byte, 8)
	io.ReadFull(f, buf)
	n, _ := binary.Uvarint(buf)
	hdr := make([]byte, n)
	io.ReadFull(f, hdr)
	if err := json.Unmarshal(hdr, resp); err != nil {
		return nil, err
	}
	var err error
	if resp.Body, err = gzip.NewReader(f); err != nil {
		f.Close()
		return nil, err
	}
	return resp, nil
}

func (rcdw *RecorderWriter) Close() error {
	if err := rcdw.tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(rcdw.tmpFile.Name(), rcdw.fileName); err != nil {
		return err
	}
	return nil
}

type WriterMultiCloser struct {
	Closer io.Closer
	Writer io.WriteCloser
}

func (wmc WriterMultiCloser) Write(p []byte) (int, error) {
	return wmc.Writer.Write(p)
}
func (wmc WriterMultiCloser) Close() error {
	wmc.Writer.Close()
	return wmc.Closer.Close()
}

type TeeReadCloser struct {
	r io.ReadCloser
	w io.WriteCloser
	io.Reader
}

func NewTeeReadCloser(r io.ReadCloser, w io.WriteCloser) io.ReadCloser {
	return &TeeReadCloser{
		r:      r,
		w:      w,
		Reader: io.TeeReader(r, w),
	}
}

func (trc TeeReadCloser) Close() error {
	trc.r.Close()
	return trc.w.Close()
}
