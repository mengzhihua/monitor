//go:build (!linux && !darwin) || android

package collect

import "fmt"

func queryODBCNative(dsn, query string) ([]byte, error) {
	return nil, fmt.Errorf("odbc: driver manager is not linked on this OS")
}
