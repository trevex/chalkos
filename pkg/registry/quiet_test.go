package registry

import (
	"bytes"
	"testing"

	"github.com/sirupsen/logrus"
)

// TestQuiet drops the lines of requests the registry did not fail, and keeps those it failed
// and every line of no request.
func TestQuiet(t *testing.T) {
	var out bytes.Buffer
	l := logrus.New()
	l.SetOutput(&out)
	l.SetFormatter(quiet{&logrus.TextFormatter{DisableTimestamp: true}})
	l.WithFields(logrus.Fields{"http.request.id": "1", "http.response.status": 404}).Error("manifest unknown")
	l.WithField("http.request.id", "2").Error("error parsing reference from context")
	l.WithFields(logrus.Fields{"http.request.id": "3", "http.response.status": 500}).Error("storage failed")
	l.Warn("purging uploads")
	want := "level=error msg=\"storage failed\" http.request.id=3 http.response.status=500\nlevel=warning msg=\"purging uploads\"\n"
	if out.String() != want {
		t.Errorf("logged\n%s\nwant\n%s", out.String(), want)
	}
}

// TestErrorLog drops the server's lines of failed TLS handshakes alone.
func TestErrorLog(t *testing.T) {
	var out bytes.Buffer
	l := ErrorLog(&out)
	l.Print("http: TLS handshake error from 192.0.2.1:4000: EOF")
	l.Print("http: panic serving 192.0.2.1:4000: boom")
	if want := "http: panic serving 192.0.2.1:4000: boom\n"; out.String() != want {
		t.Errorf("logged %q, want %q", out.String(), want)
	}
}
