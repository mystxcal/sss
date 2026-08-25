package local

import (
	"encoding/json"
	"io"
)

func writeJSON(w io.Writer, body any) {
	_ = json.NewEncoder(w).Encode(body)
}
