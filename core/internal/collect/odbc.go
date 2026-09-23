package collect

import (
	"context"
	"fmt"
	"strings"
)

// queryODBC runs one statement through the ODBC driver manager (libodbc / odbc32)
// when the DSN is a connection string. Other DSNs stay on the CLI path.
func queryODBC(ctx context.Context, dsn, query string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	low := strings.ToLower(dsn)
	if !strings.Contains(low, "driver=") && !strings.Contains(low, "dsn=") {
		return nil, fmt.Errorf("odbc: not a connection string")
	}
	return queryODBCNative(dsn, query)
}
