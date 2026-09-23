//go:build linux || darwin

package collect

import (
	"fmt"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego"
)

func queryODBCNative(dsn, query string) (out []byte, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("odbc: %v", rec)
		}
	}()
	handle, err := purego.Dlopen("libodbc.so.2", purego.RTLD_LAZY)
	if err != nil {
		handle, err = purego.Dlopen("libodbc.so", purego.RTLD_LAZY)
		if err != nil {
			return nil, fmt.Errorf("odbc: %w", err)
		}
	}
	var (
		alloc   func(handleType int16, input uintptr, output *uintptr) int16
		setEnv  func(env uintptr, attr int32, value uintptr, length int32) int16
		connect func(dbc uintptr, hwnd uintptr, dsn *byte, dsnLen int16, out *byte, outMax int16, outLen *int16, completion uint16) int16
		exec    func(stmt uintptr, query *byte, length int32) int16
		fetch   func(stmt uintptr) int16
		getData func(stmt uintptr, col int16, ctype int16, buf *byte, buflen int64, ind *int64) int16
		freeH   func(typ int16, h uintptr) int16
	)
	purego.RegisterLibFunc(&alloc, handle, "SQLAllocHandle")
	purego.RegisterLibFunc(&setEnv, handle, "SQLSetEnvAttr")
	purego.RegisterLibFunc(&connect, handle, "SQLDriverConnect")
	purego.RegisterLibFunc(&exec, handle, "SQLExecDirect")
	purego.RegisterLibFunc(&fetch, handle, "SQLFetch")
	purego.RegisterLibFunc(&getData, handle, "SQLGetData")
	purego.RegisterLibFunc(&freeH, handle, "SQLFreeHandle")

	const (
		sqlHandleEnv   int16 = 1
		sqlHandleDBC   int16 = 2
		sqlHandleStmt  int16 = 3
		sqlSuccess     int16 = 0
		sqlSuccessInfo int16 = 1
		sqlNoData      int16 = 100
		sqlNTS         int32 = -3
	)
	ok := func(rc int16) bool { return rc == sqlSuccess || rc == sqlSuccessInfo }
	var env, dbc, stmt uintptr
	if !ok(alloc(sqlHandleEnv, 0, &env)) {
		return nil, fmt.Errorf("odbc: alloc env")
	}
	defer freeH(sqlHandleEnv, env)
	// SQL_ATTR_ODBC_VERSION = 200, SQL_OV_ODBC3 = 3
	if !ok(setEnv(env, 200, 3, 0)) {
		return nil, fmt.Errorf("odbc: set version")
	}
	if !ok(alloc(sqlHandleDBC, env, &dbc)) {
		return nil, fmt.Errorf("odbc: alloc dbc")
	}
	defer freeH(sqlHandleDBC, dbc)
	dsnC := append([]byte(dsn), 0)
	var outLen int16
	if !ok(connect(dbc, 0, &dsnC[0], int16(sqlNTS), nil, 0, &outLen, 0)) {
		return nil, fmt.Errorf("odbc: connect")
	}
	if !ok(alloc(sqlHandleStmt, dbc, &stmt)) {
		return nil, fmt.Errorf("odbc: alloc stmt")
	}
	defer freeH(sqlHandleStmt, stmt)
	q := append([]byte(query), 0)
	if !ok(exec(stmt, &q[0], sqlNTS)) {
		return nil, fmt.Errorf("odbc: exec")
	}
	var rows []string
	for {
		rc := fetch(stmt)
		if rc == sqlNoData {
			break
		}
		if !ok(rc) {
			return nil, fmt.Errorf("odbc: fetch %d", rc)
		}
		var cols []string
		for col := int16(1); col <= 32; col++ {
			buf := make([]byte, 256)
			var ind int64
			rc := getData(stmt, col, 1, &buf[0], int64(len(buf)), &ind)
			if !ok(rc) {
				break
			}
			if ind < 0 {
				cols = append(cols, "")
				continue
			}
			n := int(ind)
			if n > len(buf) {
				n = len(buf)
			}
			cols = append(cols, strings.TrimSpace(string(buf[:n])))
		}
		if len(cols) > 0 {
			rows = append(rows, strings.Join(cols, " "))
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("odbc: empty")
	}
	return []byte(strings.Join(rows, "\n")), nil
}

// Keep unsafe available for pointer conversions if a driver needs them.
var _ = unsafe.Sizeof(0)
