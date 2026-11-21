package pdcmd

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestTableAlignsTerminalCells(t *testing.T) {
	file := usabilityFile(t)
	rows := dynamicpb.NewMessage(file.Messages().ByName("PlainList"))
	require.NoError(t, protojson.Unmarshal([]byte(`{"results":[
 {"result":"admin","count":"1","ready":true},
 {"result":"관리자 (가상)","count":"12"},
 {"result":"시스템 엔지니어 (가상)","count":"3"},
 {"result":"e\u0301","count":"4"},
 {"result":"👩🏽‍💻","count":"5"},
 {"result":"가","count":"6"}
 ]}`), rows))
	var out bytes.Buffer
	require.NoError(t, Table(false).Print(&out, rows))
	// The widest text occupies 22 terminal cells. These explicit spaces are a
	// visual-column oracle, independent of whichever width library prints it.
	want := "RESULT" + strings.Repeat(" ", 19) + "COUNT   READY\n" +
		"admin" + strings.Repeat(" ", 20) + "1       true\n" +
		"관리자 (가상)" + strings.Repeat(" ", 12) + "12      false\n" +
		"시스템 엔지니어 (가상)   3       false\n" +
		"e\u0301" + strings.Repeat(" ", 24) + "4       false\n" +
		"👩🏽‍💻" + strings.Repeat(" ", 23) + "5       false\n" +
		"가" + strings.Repeat(" ", 23) + "6       false\n"
	require.Equal(t, want, out.String())
	require.NotContains(t, out.String(), "\t")
	require.ErrorIs(t, Table(false).Print(failedWidthWriter{}, rows), io.ErrClosedPipe)
}

type failedWidthWriter struct{}

func (failedWidthWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
