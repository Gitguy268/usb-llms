// Package sys answers the few host questions USB LLMS needs: free space, RAM and GPUs.
package sys

import "fmt"

// GB formats a byte count the way the catalogue and prompts show it.
func GB(n uint64) string {
	return fmt.Sprintf("%.1f GB", float64(n)/1e9)
}
